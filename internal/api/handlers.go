// Package api implements the HTTP layer of the song-similarity service.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"song-similarity/internal/provider"
	"song-similarity/internal/similarity"
)

// TrackSearcher is the dependency handlers need from a metadata provider.
// It is satisfied by *provider.Client.
type TrackSearcher interface {
	SearchTrack(ctx context.Context, artist, title string) (*provider.Track, error)
}

// Handlers holds the dependencies used by the HTTP handlers.
type Handlers struct {
	searcher TrackSearcher
}

// NewHandlers builds a Handlers using the given track searcher.
func NewHandlers(searcher TrackSearcher) *Handlers {
	return &Handlers{searcher: searcher}
}

type songRequest struct {
	Artist string `json:"artist"`
	Title  string `json:"title"`
}

type compareRequest struct {
	SongA songRequest `json:"song_a"`
	SongB songRequest `json:"song_b"`
}

type compareResponse struct {
	GenreScore float64  `json:"genre_score"`
	TagsA      []string `json:"tags_a"`
	TagsB      []string `json:"tags_b"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// Compare handles POST /compare.
func (h *Handlers) Compare(w http.ResponseWriter, r *http.Request) {
	var req compareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.SongA.Artist == "" || req.SongA.Title == "" {
		writeError(w, http.StatusBadRequest, "song_a requires both artist and title")
		return
	}
	if req.SongB.Artist == "" || req.SongB.Title == "" {
		writeError(w, http.StatusBadRequest, "song_b requires both artist and title")
		return
	}

	addLogAttr(r.Context(), slog.String("artist_a", req.SongA.Artist))
	addLogAttr(r.Context(), slog.String("title_a", req.SongA.Title))
	addLogAttr(r.Context(), slog.String("artist_b", req.SongB.Artist))
	addLogAttr(r.Context(), slog.String("title_b", req.SongB.Title))

	trackA, err := h.lookupTrack(r.Context(), req.SongA)
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}

	trackB, err := h.lookupTrack(r.Context(), req.SongB)
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}

	score := similarity.CompareGenres(trackA.Tags, trackB.Tags)
	addLogAttr(r.Context(), slog.Float64("genre_score", score))

	writeJSON(w, http.StatusOK, compareResponse{
		GenreScore: score,
		TagsA:      trackA.Tags,
		TagsB:      trackB.Tags,
	})
}

type healthResponse struct {
	Status string `json:"status"`
}

// Health handles GET /health.
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

var errNoTags = errors.New("no genre tags available")

func (h *Handlers) lookupTrack(ctx context.Context, song songRequest) (*provider.Track, error) {
	track, err := h.searcher.SearchTrack(ctx, song.Artist, song.Title)
	if err != nil {
		if errors.Is(err, provider.ErrNotFound) {
			return nil, fmt.Errorf("%w: track not found for %q by %q", provider.ErrNotFound, song.Title, song.Artist)
		}
		return nil, fmt.Errorf("%w: lookup failed for %q by %q: %v", provider.ErrUpstream, song.Title, song.Artist, err)
	}

	if len(track.Tags) == 0 {
		return nil, fmt.Errorf("%w: %q by %q", errNoTags, song.Title, song.Artist)
	}

	return track, nil
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, provider.ErrNotFound), errors.Is(err, errNoTags), errors.Is(err, provider.ErrUpstream):
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}
