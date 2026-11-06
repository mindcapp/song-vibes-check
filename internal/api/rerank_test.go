package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/mindcapp/song-vibes-check/internal/analytics"
	"github.com/mindcapp/song-vibes-check/internal/tags"
	"github.com/mindcapp/song-vibes-check/internal/vectors"
)

// fakeStats serves fixed statistics and records what it was asked for.
type fakeStats struct {
	stats map[int64]analytics.Stat
	err   error
	from  int64
	to    []int64
	calls int
}

func (f *fakeStats) Stats(ctx context.Context, from int64, to []int64) (map[int64]analytics.Stat, error) {
	f.calls++
	f.from, f.to = from, to
	if f.err != nil {
		return nil, f.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.stats, nil
}

// rerankStore returns the hand-calculated example of analytics.Rerank:
// cosines 0.90 / 0.85 / 0.80 for tracks 11 / 12 / 13 (artists X, Y, Z).
func rerankStore() *fakeStore {
	mk := func(id int64, artist string, cosine float64) vectors.Neighbor {
		return vectors.Neighbor{Track: vectors.Track{TrackID: id, Title: fmt.Sprintf("t%d", id), Artist: artist, Genre: "Rock"}, Score: cosine}
	}
	return &fakeStore{
		tracks: map[int64]vectors.Track{2: {TrackID: 2, Title: "Food", Artist: "AWOL", Genre: "Hip-Hop"}},
		ranking: []vectors.Neighbor{
			mk(11, "X", 0.90), mk(12, "Y", 0.85), mk(13, "Z", 0.80),
		},
	}
}

func handCalcStats() *fakeStats {
	return &fakeStats{stats: map[int64]analytics.Stat{
		11: {Plays: 10, Skips: 2},
		12: {Plays: 20, Skips: 18},
	}}
}

func rerankRouter(t *testing.T, store vectors.VectorStore, stats analytics.TransitionStats) (http.Handler, *strings.Builder) {
	t.Helper()
	normalizer, err := tags.Load("")
	if err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	h := NewHandlers(&fakeSearcher{}, normalizer, testTimeout).WithVectors(store)
	if stats != nil {
		h = h.WithAnalytics(stats, analytics.DefaultRerankConfig())
	}
	return NewRouter(h, slog.New(slog.NewJSONHandler(&logs, nil))), &logs
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body)
	}
	return body
}

func neighborIDs(t *testing.T, body map[string]any) []int64 {
	t.Helper()
	var ids []int64
	for _, n := range body["neighbors"].([]any) {
		ids = append(ids, int64(n.(map[string]any)["track_id"].(float64)))
	}
	return ids
}

func near(t *testing.T, name string, got any, want float64) {
	t.Helper()
	f, ok := got.(float64)
	if !ok || math.Abs(f-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestSimilarRerankReordersAndExplains(t *testing.T) {
	store, stats := rerankStore(), handCalcStats()
	router, logs := rerankRouter(t, store, stats)

	rec := get(router, "/api/v1/similar?track_id=2&limit=3&rerank=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if body["reranked"] != true {
		t.Errorf("reranked = %v, want true", body["reranked"])
	}
	if _, ok := body["rerank_skipped_reason"]; ok {
		t.Error("a successful rerank carries rerank_skipped_reason")
	}
	// Qdrant order 11, 12, 13 becomes 11, 13, 12 (hand calculation in analytics tests)
	if got, want := neighborIDs(t, body), []int64{11, 13, 12}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}

	n := body["neighbors"].([]any)
	first, second, third := n[0].(map[string]any), n[1].(map[string]any), n[2].(map[string]any)
	near(t, "11 score", first["score"], 0.835)
	near(t, "11 cosine", first["cosine"], 0.90)
	near(t, "11 keep_rate", first["keep_rate"], 10.25/15)
	near(t, "11 plays", first["plays"], 10)
	near(t, "13 score", second["score"], 0.695)
	near(t, "13 keep_rate", second["keep_rate"], 0.45)
	near(t, "13 plays", second["plays"], 0)
	near(t, "12 score", third["score"], 0.646)
	for _, key := range []string{"track_id", "title", "artist", "genre", "x", "y", "score", "cosine", "keep_rate", "plays"} {
		if _, ok := first[key]; !ok {
			t.Errorf("neighbour is missing %q: %v", key, first)
		}
	}

	if stats.from != 2 || !reflect.DeepEqual(stats.to, []int64{11, 12, 13}) {
		t.Errorf("stats asked from=%d to=%v, want 2 and [11 12 13]", stats.from, stats.to)
	}
	for _, want := range []string{`"rerank":true`, `"reranked":true`, `"candidates":3`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("access log is missing %s:\n%s", want, logs)
		}
	}
}

func TestSimilarRerankCandidatesAndLimit(t *testing.T) {
	for _, tt := range []struct {
		query     string
		wantLimit int
	}{
		{"limit=2", 20},  // max(4*2, 20)
		{"limit=5", 20},  // default limit
		{"limit=10", 40}, // 4*10
		{"limit=50", 200},
	} {
		store := rerankStore()
		router, _ := rerankRouter(t, store, handCalcStats())
		rec := get(router, "/api/v1/similar?track_id=2&rerank=true&"+tt.query)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", tt.query, rec.Code)
		}
		if !reflect.DeepEqual(store.limits, []int{tt.wantLimit}) {
			t.Errorf("%s: Qdrant asked for %v candidates, want [%d]", tt.query, store.limits, tt.wantLimit)
		}
	}

	// the answer is cut to limit after reranking
	router, _ := rerankRouter(t, rerankStore(), handCalcStats())
	body := decode(t, get(router, "/api/v1/similar?track_id=2&rerank=true&limit=2"))
	if got, want := neighborIDs(t, body), []int64{11, 13}; !reflect.DeepEqual(got, want) {
		t.Errorf("limit 2 = %v, want %v", got, want)
	}
}

func TestSimilarRerankRespectsExcludeSameArtist(t *testing.T) {
	store := rerankStore()
	store.ranking = append([]vectors.Neighbor{
		{Track: vectors.Track{TrackID: 10, Artist: "AWOL"}, Score: 0.99}, // the source's own artist
	}, store.ranking...)
	stats := handCalcStats()
	router, _ := rerankRouter(t, store, stats)

	body := decode(t, get(router, "/api/v1/similar?track_id=2&limit=3&rerank=true&exclude_same_artist=true"))
	if got, want := neighborIDs(t, body), []int64{11, 13, 12}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v (AWOL track 10 excluded)", got, want)
	}
	if !reflect.DeepEqual(stats.to, []int64{11, 12, 13}) {
		t.Errorf("stats asked about %v, want the filtered candidates [11 12 13]", stats.to)
	}
	// candidates = max(4*3, 20) = 20, and the artist filter pulls 6x that from Qdrant
	if !reflect.DeepEqual(store.limits, []int{120}) {
		t.Errorf("Qdrant limits = %v, want [120]", store.limits)
	}
}

func TestSimilarRerankEmptyStatsKeepsQdrantOrder(t *testing.T) {
	for name, stats := range map[string]*fakeStats{"empty": {stats: map[int64]analytics.Stat{}}, "nil": {}} {
		router, _ := rerankRouter(t, rerankStore(), stats)
		body := decode(t, get(router, "/api/v1/similar?track_id=2&limit=3&rerank=true"))
		if body["reranked"] != true {
			t.Errorf("%s: reranked = %v, want true (statistics were read, they are just empty)", name, body["reranked"])
		}
		if got, want := neighborIDs(t, body), []int64{11, 12, 13}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: order = %v, want Qdrant order %v", name, got, want)
		}
		first := body["neighbors"].([]any)[0].(map[string]any)
		near(t, name+" keep_rate", first["keep_rate"], 0.5)
		near(t, name+" plays", first["plays"], 0)
		near(t, name+" score", first["score"], 0.7*0.9+0.3*0.5)
	}
}

func TestSimilarRerankFallsBackWhenStatsFail(t *testing.T) {
	for _, tt := range []struct {
		name       string
		err        error
		wantReason string
		wantCode   string
	}{
		{"unavailable", &analytics.UpstreamError{Err: errors.New("dial tcp 10.0.0.9:8123: connection refused")}, "unavailable", "unavailable"},
		{"server error", &analytics.UpstreamError{Status: 500, Detail: "Code: 60. Table vibe.transitions doesn't exist"}, "unavailable", "unavailable"},
		{"timeout", fmt.Errorf("%w: %w", analytics.ErrUpstream, context.DeadlineExceeded), "did not arrive in time", "timeout"},
	} {
		store, stats := rerankStore(), handCalcStats()
		stats.err = tt.err
		router, logs := rerankRouter(t, store, stats)

		rec := get(router, "/api/v1/similar?track_id=2&limit=2&rerank=true")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (rerank must not fail the request)", tt.name, rec.Code)
			continue
		}
		body := decode(t, rec)
		if body["reranked"] != false {
			t.Errorf("%s: reranked = %v, want false", tt.name, body["reranked"])
		}
		reason, _ := body["rerank_skipped_reason"].(string)
		if !strings.Contains(reason, tt.wantReason) {
			t.Errorf("%s: reason = %q, want it to contain %q", tt.name, reason, tt.wantReason)
		}
		for _, leak := range []string{"10.0.0.9", "vibe.transitions", "connection refused"} {
			if strings.Contains(rec.Body.String(), leak) {
				t.Errorf("%s: response leaks %q: %s", tt.name, leak, rec.Body)
			}
		}
		// Qdrant's order and format, cut to limit; "score" is the plain cosine
		if got, want := neighborIDs(t, body), []int64{11, 12}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: order = %v, want Qdrant's %v", tt.name, got, want)
		}
		first := body["neighbors"].([]any)[0].(map[string]any)
		near(t, tt.name+" score", first["score"], 0.90)
		for _, key := range []string{"cosine", "keep_rate", "plays"} {
			if _, ok := first[key]; ok {
				t.Errorf("%s: a skipped answer has %q: %v", tt.name, key, first)
			}
		}
		for _, want := range []string{`"reranked":false`, `"rerank_skipped":"` + tt.wantCode + `"`} {
			if !strings.Contains(logs.String(), want) {
				t.Errorf("%s: access log is missing %s:\n%s", tt.name, want, logs)
			}
		}
	}
}

func TestSimilarRerankNotConfigured(t *testing.T) {
	store := rerankStore()
	router, _ := rerankRouter(t, store, nil) // no WithAnalytics

	rec := get(router, "/api/v1/similar?track_id=2&limit=2&rerank=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if body["reranked"] != false {
		t.Errorf("reranked = %v, want false", body["reranked"])
	}
	if reason, _ := body["rerank_skipped_reason"].(string); !strings.Contains(reason, "CLICKHOUSE_URL") {
		t.Errorf("reason = %q, want it to mention CLICKHOUSE_URL", reason)
	}
	if got, want := neighborIDs(t, body), []int64{11, 12}; !reflect.DeepEqual(got, want) {
		t.Errorf("neighbours = %v, want %v", got, want)
	}
	// nothing to rerank with, so no extra candidates are pulled
	if !reflect.DeepEqual(store.limits, []int{2}) {
		t.Errorf("Qdrant limits = %v, want [2]", store.limits)
	}
}

func TestSimilarWithoutRerankKeepsOldContract(t *testing.T) {
	for _, query := range []string{"", "&rerank=false", "&rerank=0"} {
		stats := handCalcStats()
		store := rerankStore()
		router, _ := rerankRouter(t, store, stats)

		rec := get(router, "/api/v1/similar?track_id=2&limit=3"+query)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: status = %d", query, rec.Code)
		}
		body := decode(t, rec)
		for _, key := range []string{"reranked", "rerank_skipped_reason"} {
			if _, ok := body[key]; ok {
				t.Errorf("%q: plain answer has %q", query, key)
			}
		}
		if len(body) != 2 {
			t.Errorf("%q: top-level keys = %v, want only track and neighbors", query, body)
		}
		if got, want := neighborIDs(t, body), []int64{11, 12, 13}; !reflect.DeepEqual(got, want) {
			t.Errorf("%q: order = %v, want Qdrant's %v", query, got, want)
		}
		first := body["neighbors"].([]any)[0].(map[string]any)
		if len(first) != 7 {
			t.Errorf("%q: neighbour keys = %v, want the 7 original fields", query, first)
		}
		if stats.calls != 0 {
			t.Errorf("%q: ClickHouse was queried without rerank", query)
		}
		if !reflect.DeepEqual(store.limits, []int{3}) {
			t.Errorf("%q: Qdrant limits = %v, want [3]", query, store.limits)
		}
	}
}

func TestSimilarRerankValidationAndUpstreamErrors(t *testing.T) {
	router, _ := rerankRouter(t, rerankStore(), handCalcStats())
	rec := get(router, "/api/v1/similar?track_id=2&rerank=maybe")
	if rec.Code != http.StatusBadRequest || !strings.Contains(errorBody(t, rec), "rerank must be true or false") {
		t.Errorf("rerank=maybe: %d %s", rec.Code, rec.Body)
	}
	if rec := get(router, "/api/v1/similar?track_id=999&rerank=true"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown track: status = %d, want 404", rec.Code)
	}

	store := rerankStore()
	store.err = &vectors.UpstreamError{Err: errors.New("down")}
	stats := handCalcStats()
	router, _ = rerankRouter(t, store, stats)
	rec = get(router, "/api/v1/similar?track_id=2&rerank=true")
	if rec.Code != http.StatusBadGateway || rec.Header().Get("Retry-After") != "1" {
		t.Errorf("Qdrant down: status = %d, Retry-After %q; want 502 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	if stats.calls != 0 {
		t.Error("ClickHouse was queried although Qdrant failed")
	}
}

func TestSimilarRerankClientGone(t *testing.T) {
	router, _ := rerankRouter(t, rerankStore(), handCalcStats())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/similar?track_id=2&rerank=true", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != statusClientClosedRequest {
		t.Errorf("status = %d, want %d (client closed the request)", rec.Code, statusClientClosedRequest)
	}
}

func TestSimilarRequestsMetric(t *testing.T) {
	count := func(mode string) float64 { return testutil.ToFloat64(similarRequests.WithLabelValues(mode)) }
	plain, reranked, skipped := count(modePlain), count(modeReranked), count(modeRerankSkipped)

	router, _ := rerankRouter(t, rerankStore(), handCalcStats())
	get(router, "/api/v1/similar?track_id=2")
	get(router, "/api/v1/similar?track_id=2&rerank=true")
	get(router, "/api/v1/similar?track_id=2&rerank=true")
	failing := handCalcStats()
	failing.err = errors.New("boom")
	router, _ = rerankRouter(t, rerankStore(), failing)
	get(router, "/api/v1/similar?track_id=2&rerank=true")
	get(router, "/api/v1/similar?track_id=999&rerank=true") // 404 is not counted

	for mode, want := range map[string]float64{modePlain: plain + 1, modeReranked: reranked + 2, modeRerankSkipped: skipped + 1} {
		if got := count(mode); got != want {
			t.Errorf("similar_requests_total{mode=%q} = %v, want %v", mode, got, want)
		}
	}
}
