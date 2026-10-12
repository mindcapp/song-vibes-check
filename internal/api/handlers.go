// Package api implements the HTTP layer of the song-similarity service.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	timeout    time.Duration
}

// NewHandlers builds the handlers. timeout bounds how long POST /compare may
// spend looking both songs up before it gives up with a 502.
func NewHandlers(searcher TrackSearcher, normalizer *tags.Normalizer, timeout time.Duration) *Handlers {
	return &Handlers{searcher: searcher, normalizer: normalizer, timeout: timeout}
}

const (
	// maxBodyBytes caps the request body; a valid request is a few hundred bytes.
	maxBodyBytes = 64 << 10
	// statusClientClosedRequest is logged (nginx's convention) when the client
	// went away before the answer was ready. Nobody receives it.
	statusClientClosedRequest = 499
	// defaultRetryAfter is suggested to clients on a 502 when MusicBrainz did
	// not name a delay itself; maxRetryAfter caps what is passed on.
	defaultRetryAfter = time.Second
	maxRetryAfter     = time.Minute
)

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
//
// Errors: 400 for a malformed body or missing fields, 404 when a song is not
// in MusicBrainz, 422 when it is there but has no usable tags, 502 (with
// Retry-After) when MusicBrainz is unavailable or too slow.
func (h *Handlers) Compare(w http.ResponseWriter, r *http.Request) {
	var req compareRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		h.fail(w, r, &apiError{status: http.StatusBadRequest, message: "invalid JSON body", cause: err})
		return
	}

	req.SongA, req.SongB = req.SongA.trimmed(), req.SongB.trimmed()
	if req.SongA.Artist == "" || req.SongA.Title == "" {
		h.fail(w, r, &apiError{status: http.StatusBadRequest, song: "song_a", message: "song_a requires both artist and title"})
		return
	}
	if req.SongB.Artist == "" || req.SongB.Title == "" {
		h.fail(w, r, &apiError{status: http.StatusBadRequest, song: "song_b", message: "song_b requires both artist and title"})
		return
	}

	addLogAttr(r.Context(), slog.String("artist_a", req.SongA.Artist))
	addLogAttr(r.Context(), slog.String("title_a", req.SongA.Title))
	addLogAttr(r.Context(), slog.String("artist_b", req.SongB.Artist))
	addLogAttr(r.Context(), slog.String("title_b", req.SongB.Title))

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	trackA, profileA, apiErr := h.lookup(ctx, "song_a", req.SongA)
	if apiErr != nil {
		h.fail(w, r, apiErr)
		return
	}
	trackB, profileB, apiErr := h.lookup(ctx, "song_b", req.SongB)
	if apiErr != nil {
		h.fail(w, r, apiErr)
		return
	}

	addLogAttr(r.Context(), slog.String("cache_a", cacheStatus(trackA)))
	addLogAttr(r.Context(), slog.String("cache_b", cacheStatus(trackB)))
	addLogAttr(r.Context(), slog.String("tags_from_a", trackA.TagSource))
	addLogAttr(r.Context(), slog.String("tags_from_b", trackB.TagSource))

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

// apiError is a failed request: what the client is told and what is logged.
type apiError struct {
	status  int
	message string // sent to the client
	song    string // "song_a" or "song_b", when the error is about one of them
	// retryAfter, when positive, is sent as a Retry-After header.
	retryAfter time.Duration
	// cause is the underlying error; it is logged but not sent.
	cause error
}

// fail answers with the error and records it on the request's access-log
// line: the client-facing message, which song it was about, and the cause.
func (h *Handlers) fail(w http.ResponseWriter, r *http.Request, e *apiError) {
	text := e.message
	if e.cause != nil {
		text += ": " + e.cause.Error()
	}
	addLogAttr(r.Context(), slog.String("error", text))
	if e.song != "" {
		addLogAttr(r.Context(), slog.String("failed_song", e.song))
	}

	if e.retryAfter > 0 {
		seconds := int(math.Ceil(e.retryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
	}
	writeError(w, e.status, e.message)
}

// lookup finds one song and builds its tag profile.
func (h *Handlers) lookup(ctx context.Context, field string, song songRequest) (*provider.Track, map[string]float64, *apiError) {
	track, err := h.searcher.SearchTrack(ctx, song.Artist, song.Title)
	if err != nil {
		return nil, nil, lookupError(ctx, field, song, err)
	}

	profile := h.profile(track)
	if len(profile) == 0 {
		return nil, nil, &apiError{
			status:  http.StatusUnprocessableEntity,
			song:    field,
			message: fmt.Sprintf("%q by %q was found but has no usable genre tags", song.Title, song.Artist),
		}
	}
	return track, profile, nil
}

// lookupError maps a failed lookup to a response.
func lookupError(ctx context.Context, field string, song songRequest, err error) *apiError {
	what := fmt.Sprintf("%q by %q", song.Title, song.Artist)

	switch {
	case errors.Is(err, provider.ErrNotFound):
		return &apiError{status: http.StatusNotFound, song: field, message: "track not found: " + what}

	case errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled:
		// The client went away. Nobody reads this response; the status is for
		// the access log and the metrics.
		return &apiError{status: statusClientClosedRequest, song: field, message: "client closed the request", cause: err}

	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == context.DeadlineExceeded:
		return &apiError{
			status:     http.StatusBadGateway,
			song:       field,
			message:    "MusicBrainz did not answer in time for " + what,
			retryAfter: defaultRetryAfter,
			cause:      err,
		}

	default:
		// Everything else a lookup can fail with is a problem talking to
		// MusicBrainz: an error status, a network failure, a broken response.
		retryAfter := defaultRetryAfter
		var upstream *provider.UpstreamError
		if errors.As(err, &upstream) && upstream.RetryAfter > 0 {
			retryAfter = min(upstream.RetryAfter, maxRetryAfter)
		}
		return &apiError{
			status:     http.StatusBadGateway,
			song:       field,
			message:    "MusicBrainz is unavailable, could not look up " + what,
			retryAfter: retryAfter,
			cause:      err,
		}
	}
}

func (s songRequest) trimmed() songRequest {
	return songRequest{Artist: strings.TrimSpace(s.Artist), Title: strings.TrimSpace(s.Title)}
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

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}
