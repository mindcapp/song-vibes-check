package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mindcapp/song-vibes-check/internal/vectors"
)

const (
	defaultSimilarLimit = 5
	maxSimilarLimit     = 50
	defaultSearchLimit  = 10
	maxSearchLimit      = 50
	minQueryRunes       = 2
	maxQueryRunes       = 100

	// With exclude_same_artist the store is asked for more candidates than
	// needed and the query's artist is filtered out, as in
	// pipeline/evaluate.py (30 candidates for 5 neighbours). If that leaves
	// too few, the search is widened once to maxCandidates.
	minCandidates       = 30
	candidatesPerResult = 6
	maxCandidates       = 1000
)

// WithVectors enables GET /api/v1/similar and GET /api/v1/tracks/search.
// Without a store they answer 503.
func (h *Handlers) WithVectors(store vectors.VectorStore) *Handlers {
	h.vectors = store
	return h
}

type similarResponse struct {
	Track     vectors.Track      `json:"track"`
	Neighbors []vectors.Neighbor `json:"neighbors"`
}

type searchResponse struct {
	Tracks []vectors.Track `json:"tracks"`
}

var errVectorsDisabled = &apiError{
	status:  http.StatusServiceUnavailable,
	message: "similar-track search is not configured on this server (QDRANT_URL is not set)",
}

// Similar handles GET /api/v1/similar?track_id=&limit=&exclude_same_artist=.
//
// Errors: 400 for invalid parameters, 404 when the track is not in the
// collection, 502 (with Retry-After) when Qdrant is unavailable, 503 when
// vector search is not configured.
func (h *Handlers) Similar(w http.ResponseWriter, r *http.Request) {
	if h.vectors == nil {
		h.fail(w, r, errVectorsDisabled)
		return
	}
	query := r.URL.Query()
	trackID, apiErr := parseTrackID(query.Get("track_id"))
	if apiErr != nil {
		h.fail(w, r, apiErr)
		return
	}
	limit, apiErr := parseLimit(query.Get("limit"), defaultSimilarLimit, maxSimilarLimit)
	if apiErr != nil {
		h.fail(w, r, apiErr)
		return
	}
	exclude, apiErr := parseFlag("exclude_same_artist", query.Get("exclude_same_artist"))
	if apiErr != nil {
		h.fail(w, r, apiErr)
		return
	}

	addLogAttr(r.Context(), slog.Int64("track_id", trackID))
	addLogAttr(r.Context(), slog.Int("limit", limit))
	addLogAttr(r.Context(), slog.Bool("exclude_same_artist", exclude))

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	source, neighbors, err := h.similar(ctx, trackID, limit, exclude)
	if err != nil {
		h.fail(w, r, vectorsError(ctx, err, fmt.Sprintf("track_id %d", trackID)))
		return
	}
	addLogAttr(r.Context(), slog.Int("neighbors", len(neighbors)))

	writeJSON(w, http.StatusOK, similarResponse{Track: *source, Neighbors: neighbors})
}

// similar asks the store for the neighbours, filtering out the query's own
// artist when exclude is set.
func (h *Handlers) similar(ctx context.Context, trackID int64, limit int, exclude bool) (*vectors.Track, []vectors.Neighbor, error) {
	if !exclude {
		source, neighbors, err := h.vectors.Similar(ctx, trackID, limit)
		if neighbors == nil {
			neighbors = []vectors.Neighbor{}
		}
		return source, neighbors, err
	}

	candidates := min(max(limit*candidatesPerResult, minCandidates), maxCandidates)
	for {
		source, found, err := h.vectors.Similar(ctx, trackID, candidates)
		if err != nil {
			return nil, nil, err
		}
		kept := make([]vectors.Neighbor, 0, limit)
		for _, n := range found {
			if n.Artist != source.Artist {
				kept = append(kept, n)
				if len(kept) == limit {
					break
				}
			}
		}
		// Done when there are enough, or when the store has nothing more to give.
		if len(kept) == limit || len(found) < candidates || candidates == maxCandidates {
			return source, kept, nil
		}
		candidates = maxCandidates
	}
}

// SearchTracks handles GET /api/v1/tracks/search?q=&limit=: tracks whose
// title or artist contains q, for the frontend's autocomplete.
//
// Errors: 400 for a too short or too long q or an invalid limit, 502 (with
// Retry-After) when Qdrant is unavailable, 503 when vector search is not
// configured.
func (h *Handlers) SearchTracks(w http.ResponseWriter, r *http.Request) {
	if h.vectors == nil {
		h.fail(w, r, errVectorsDisabled)
		return
	}
	query := r.URL.Query()
	q := strings.TrimSpace(query.Get("q"))
	if n := utf8.RuneCountInString(q); n < minQueryRunes || n > maxQueryRunes {
		h.fail(w, r, &apiError{
			status:  http.StatusBadRequest,
			message: fmt.Sprintf("q must be between %d and %d characters", minQueryRunes, maxQueryRunes),
		})
		return
	}
	limit, apiErr := parseLimit(query.Get("limit"), defaultSearchLimit, maxSearchLimit)
	if apiErr != nil {
		h.fail(w, r, apiErr)
		return
	}

	addLogAttr(r.Context(), slog.String("q", q))
	addLogAttr(r.Context(), slog.Int("limit", limit))

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	tracks, err := h.vectors.Search(ctx, q, limit)
	if err != nil {
		h.fail(w, r, vectorsError(ctx, err, fmt.Sprintf("q %q", q)))
		return
	}
	if tracks == nil {
		tracks = []vectors.Track{}
	}
	addLogAttr(r.Context(), slog.Int("results", len(tracks)))

	writeJSON(w, http.StatusOK, searchResponse{Tracks: tracks})
}

// vectorsError maps a failed store call to a response; what names the
// request in the message.
func vectorsError(ctx context.Context, err error, what string) *apiError {
	switch {
	case errors.Is(err, vectors.ErrNotFound):
		return &apiError{status: http.StatusNotFound, message: "track not found: " + what}

	case errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled:
		return &apiError{status: statusClientClosedRequest, message: "client closed the request", cause: err}

	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == context.DeadlineExceeded:
		return &apiError{
			status:     http.StatusBadGateway,
			message:    "vector database did not answer in time for " + what,
			retryAfter: defaultRetryAfter,
			cause:      err,
		}

	default:
		return &apiError{
			status:     http.StatusBadGateway,
			message:    "vector database is unavailable, could not look up " + what,
			retryAfter: defaultRetryAfter,
			cause:      err,
		}
	}
}

func parseTrackID(raw string) (int64, *apiError) {
	if raw == "" {
		return 0, &apiError{status: http.StatusBadRequest, message: "track_id is required"}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, &apiError{status: http.StatusBadRequest, message: "track_id must be a positive integer"}
	}
	return id, nil
}

func parseLimit(raw string, def, maxLimit int) (int, *apiError) {
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxLimit {
		return 0, &apiError{
			status:  http.StatusBadRequest,
			message: fmt.Sprintf("limit must be an integer between 1 and %d", maxLimit),
		}
	}
	return n, nil
}

func parseFlag(name, raw string) (bool, *apiError) {
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, &apiError{status: http.StatusBadRequest, message: name + " must be true or false"}
	}
	return v, nil
}
