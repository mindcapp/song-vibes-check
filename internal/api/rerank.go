package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/mindcapp/song-vibes-check/internal/analytics"
	"github.com/mindcapp/song-vibes-check/internal/vectors"
)

const (
	// rerankCandidatesPerResult and minRerankCandidates: Qdrant is asked for
	// max(4*limit, 20) candidates, which are reranked down to limit.
	rerankCandidatesPerResult = 4
	minRerankCandidates       = 20
)

// Values of the "mode" label of similar_requests_total.
const (
	modePlain         = "plain"
	modeReranked      = "reranked"
	modeRerankSkipped = "rerank_skipped"
)

var similarRequests = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "similar_requests_total",
	Help: "Answered /api/v1/similar requests by mode: plain (no rerank asked), " +
		"reranked, or rerank_skipped (asked for, but fell back to Qdrant's order).",
}, []string{"mode"})

// WithAnalytics enables rerank=true on GET /api/v1/similar using listener
// statistics. Without it, rerank=true falls back to Qdrant's order and says so.
func (h *Handlers) WithAnalytics(stats analytics.TransitionStats, cfg analytics.RerankConfig) *Handlers {
	h.stats = stats
	h.rerank = cfg
	return h
}

// rerankedNeighbor is a neighbour of a reranked answer. score is the combined
// score the list is sorted by; cosine is what Qdrant reported (the "score" of
// a plain answer).
type rerankedNeighbor struct {
	vectors.Track
	Score    float64 `json:"score"`
	Cosine   float64 `json:"cosine"`
	KeepRate float64 `json:"keep_rate"`
	Plays    uint64  `json:"plays"`
}

type similarRerankedResponse struct {
	Track     vectors.Track      `json:"track"`
	Neighbors []rerankedNeighbor `json:"neighbors"`
	Reranked  bool               `json:"reranked"`
}

// similarSkippedResponse is a plain answer (Qdrant's order and format) to a
// request that asked for rerank=true, with the reason it was not applied.
type similarSkippedResponse struct {
	similarResponse
	Reranked            bool   `json:"reranked"`
	RerankSkippedReason string `json:"rerank_skipped_reason"`
}

// Why a rerank was skipped: a short code for logs and a message for clients.
type skipReason struct{ code, message string }

var (
	skipNotConfigured = skipReason{"not_configured",
		"reranking is not configured on this server (CLICKHOUSE_URL is not set); showing audio similarity only"}
	skipTimeout = skipReason{"timeout",
		"listener statistics did not arrive in time; showing audio similarity only"}
	skipUnavailable = skipReason{"unavailable",
		"listener statistics are unavailable; showing audio similarity only"}
)

// similarReranked answers GET /api/v1/similar?rerank=true. It never fails
// because of the statistics: if they cannot be had, Qdrant's answer goes out
// with reranked=false and the reason.
func (h *Handlers) similarReranked(ctx context.Context, w http.ResponseWriter, r *http.Request, trackID int64, limit int, exclude bool) {
	if h.stats == nil {
		// Nothing to rerank with: answer exactly as without rerank.
		source, neighbors, err := h.similar(ctx, trackID, limit, exclude)
		if err != nil {
			h.fail(w, r, vectorsError(ctx, err, "track_id "+itoa(trackID)))
			return
		}
		h.writeSkipped(w, r, source, neighbors, skipNotConfigured)
		return
	}

	candidates := max(limit*rerankCandidatesPerResult, minRerankCandidates)
	source, found, err := h.similar(ctx, trackID, candidates, exclude)
	if err != nil {
		h.fail(w, r, vectorsError(ctx, err, "track_id "+itoa(trackID)))
		return
	}
	addLogAttr(r.Context(), slog.Int("candidates", len(found)))

	ids := make([]int64, len(found))
	for i, n := range found {
		ids[i] = n.TrackID
	}
	stats, err := h.stats.Stats(ctx, trackID, ids)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled {
			h.fail(w, r, &apiError{status: statusClientClosedRequest, message: "client closed the request", cause: err})
			return
		}
		reason := skipUnavailable
		if errors.Is(err, context.DeadlineExceeded) {
			reason = skipTimeout
		}
		addLogAttr(r.Context(), slog.String("rerank_error", err.Error()))
		h.writeSkipped(w, r, source, found[:min(limit, len(found))], reason)
		return
	}

	ranked := analytics.Rerank(found, stats, h.rerank)
	ranked = ranked[:min(limit, len(ranked))]
	neighbors := make([]rerankedNeighbor, len(ranked))
	for i, x := range ranked {
		neighbors[i] = rerankedNeighbor{
			Track: x.Track, Score: x.Score, Cosine: x.Cosine, KeepRate: x.KeepRate, Plays: x.Plays,
		}
	}

	similarRequests.WithLabelValues(modeReranked).Inc()
	addLogAttr(r.Context(), slog.Bool("reranked", true))
	addLogAttr(r.Context(), slog.Int("stats_rows", len(stats)))
	addLogAttr(r.Context(), slog.Int("neighbors", len(neighbors)))
	writeJSON(w, http.StatusOK, similarRerankedResponse{Track: *source, Neighbors: neighbors, Reranked: true})
}

// writeSkipped answers with Qdrant's order and the reason rerank was skipped.
func (h *Handlers) writeSkipped(w http.ResponseWriter, r *http.Request, source *vectors.Track, neighbors []vectors.Neighbor, reason skipReason) {
	similarRequests.WithLabelValues(modeRerankSkipped).Inc()
	addLogAttr(r.Context(), slog.Bool("reranked", false))
	addLogAttr(r.Context(), slog.String("rerank_skipped", reason.code))
	addLogAttr(r.Context(), slog.Int("neighbors", len(neighbors)))
	writeJSON(w, http.StatusOK, similarSkippedResponse{
		similarResponse:     similarResponse{Track: *source, Neighbors: neighbors},
		Reranked:            false,
		RerankSkippedReason: reason.message,
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
