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
	"song-similarity/internal/tags"
)

type TrackSearcher interface {
	SearchTrack(ctx context.Context, artist, title string) (*provider.Track, error)
}

// Handlers holds the dependencies used by the HTTP handlers.
type Handlers struct {
	searcher   TrackSearcher
	normalizer *tags.Normalizer
}

func NewHandlers(searcher TrackSearcher, normalizer *tags.Normalizer) *Handlers {
	return &Handlers{searcher: searcher, normalizer: normalizer}
}

type songRequest struct {
	Artist string `json:"artist"`
	Title  string `json:"title"`
}

type compareRequest struct {
	SongA songRequest `json:"song_a"`
	SongB songRequest `json:"song_b"`
}

// compareResponse is the POST /compare payload. tags_a and tags_b are the
// tags exactly as MusicBrainz spells them; the scores and the shared/only
// lists are computed from the normalized tags (synonyms merged, stop-listed
// tags dropped). tag_source_a and tag_source_b say where each song's tags came
// from: "recording", "artist" or "mixed". low_data is true when either song
// ends up with fewer than lowDataTags normalized tags, i.e. the scores rest on
// too little to be trusted.
type compareResponse struct {
	GenreScore    float64  `json:"genre_score"`
	WeightedScore float64  `json:"weighted_score"`
	TagsA         []string `json:"tags_a"`
	TagsB         []string `json:"tags_b"`
	SharedTags    []string `json:"shared_tags"`
	OnlyA         []string `json:"only_a"`
	OnlyB         []string `json:"only_b"`
	TagSourceA    string   `json:"tag_source_a"`
	TagSourceB    string   `json:"tag_source_b"`
	LowData       bool     `json:"low_data"`
}

// lowDataTags is the number of normalized tags a song needs for its side of
// the comparison to be considered well founded.
const lowDataTags = 3

type errorResponse struct {
	Error string `json:"error"`
}

// Compare handles POST /compare.
func (h *Handlers) Compare(w http.ResponseWriter, r *http.Request) {
	var req compareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.fail(w, r, http.StatusBadRequest, "", errors.New("invalid JSON body"))
		return
	}

	if req.SongA.Artist == "" || req.SongA.Title == "" {
		h.fail(w, r, http.StatusBadRequest, "song_a", errors.New("song_a requires both artist and title"))
		return
	}
	if req.SongB.Artist == "" || req.SongB.Title == "" {
		h.fail(w, r, http.StatusBadRequest, "song_b", errors.New("song_b requires both artist and title"))
		return
	}

	addLogAttr(r.Context(), slog.String("artist_a", req.SongA.Artist))
	addLogAttr(r.Context(), slog.String("title_a", req.SongA.Title))
	addLogAttr(r.Context(), slog.String("artist_b", req.SongB.Artist))
	addLogAttr(r.Context(), slog.String("title_b", req.SongB.Title))

	trackA, err := h.lookupTrack(r.Context(), req.SongA)
	if err != nil {
		h.fail(w, r, statusFor(err), "song_a", err)
		return
	}

	trackB, err := h.lookupTrack(r.Context(), req.SongB)
	if err != nil {
		h.fail(w, r, statusFor(err), "song_b", err)
		return
	}

	addLogAttr(r.Context(), slog.String("cache_a", cacheStatus(trackA)))
	addLogAttr(r.Context(), slog.String("cache_b", cacheStatus(trackB)))
	addLogAttr(r.Context(), slog.String("tags_from_a", trackA.TagSource))
	addLogAttr(r.Context(), slog.String("tags_from_b", trackB.TagSource))

	profileA, profileB := h.profile(trackA), h.profile(trackB)
	result := similarity.Compare(profileA, profileB)
	lowData := len(profileA) < lowDataTags || len(profileB) < lowDataTags

	addLogAttr(r.Context(), slog.Float64("genre_score", result.GenreScore))
	addLogAttr(r.Context(), slog.Float64("weighted_score", result.WeightedScore))
	addLogAttr(r.Context(), slog.Bool("low_data", lowData))

	writeJSON(w, http.StatusOK, compareResponse{
		GenreScore:    result.GenreScore,
		WeightedScore: result.WeightedScore,
		TagsA:         trackA.Tags,
		TagsB:         trackB.Tags,
		SharedTags:    result.Shared,
		OnlyA:         result.OnlyA,
		OnlyB:         result.OnlyB,
		TagSourceA:    trackA.TagSource,
		TagSourceB:    trackB.TagSource,
		LowData:       lowData,
	})
}

// fail answers with an error and records its text, and which song it was
// about (if any), on the request's access-log line.
func (h *Handlers) fail(w http.ResponseWriter, r *http.Request, status int, song string, err error) {
	addLogAttr(r.Context(), slog.String("error", err.Error()))
	if song != "" {
		addLogAttr(r.Context(), slog.String("failed_song", song))
	}
	writeError(w, status, err.Error())
}

func cacheStatus(track *provider.Track) string {
	if track.Cached {
		return "hit"
	}
	return "miss"
}

// profile turns a track's tags into normalized tag -> weight. Recording tags
// and artist tags are each reduced to shares of their own votes, so that the
// two lists are comparable whatever their vote totals, and artist tags then
// count for provider.ArtistWeight of a recording tag.
func (h *Handlers) profile(track *provider.Track) map[string]float64 {
	profile := make(map[string]float64)
	h.addShares(profile, track.RecordingTags, 1)
	h.addShares(profile, track.ArtistTags, provider.ArtistWeight)
	return profile
}

// addShares normalizes the tags, sums the votes of those that normalize to
// the same name ("hip hop", "hip-hop") and adds each one's share of the total,
// scaled by weight, to profile. A tag nobody voted for counts as one vote.
func (h *Handlers) addShares(profile map[string]float64, tagList []provider.Tag, weight float64) {
	votes := make(map[string]int, len(tagList))
	total := 0
	for _, t := range tagList {
		name, ok := h.normalizer.Normalize(t.Name)
		if !ok {
			continue
		}
		count := max(t.Count, 1)
		votes[name] += count
		total += count
	}
	for name, count := range votes {
		profile[name] += weight * float64(count) / float64(total)
	}
}

type healthResponse struct {
	Status string `json:"status"`
}

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
