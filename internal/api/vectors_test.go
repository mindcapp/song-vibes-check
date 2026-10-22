package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/mindcapp/song-vibes-check/internal/tags"
	"github.com/mindcapp/song-vibes-check/internal/vectors"
)

// fakeStore serves neighbours from a fixed ranking and records the limits it
// was asked for.
type fakeStore struct {
	tracks    map[int64]vectors.Track
	ranking   []vectors.Neighbor // neighbours of every track, best first
	err       error
	limits    []int
	searchArg string
}

func (f *fakeStore) Similar(_ context.Context, id int64, limit int) (*vectors.Track, []vectors.Neighbor, error) {
	f.limits = append(f.limits, limit)
	if f.err != nil {
		return nil, nil, f.err
	}
	source, ok := f.tracks[id]
	if !ok {
		return nil, nil, vectors.ErrNotFound
	}
	return &source, f.ranking[:min(limit, len(f.ranking))], nil
}

func (f *fakeStore) Search(_ context.Context, q string, limit int) ([]vectors.Track, error) {
	f.searchArg = q
	if f.err != nil {
		return nil, f.err
	}
	var out []vectors.Track
	for _, n := range f.ranking[:min(limit, len(f.ranking))] {
		out = append(out, n.Track)
	}
	return out, nil
}

func vectorsRouter(t *testing.T, store vectors.VectorStore) (http.Handler, *strings.Builder) {
	t.Helper()
	normalizer, err := tags.Load("")
	if err != nil {
		t.Fatalf("load tags: %v", err)
	}
	var logs strings.Builder
	h := NewHandlers(&fakeSearcher{}, normalizer, testTimeout).WithVectors(store)
	return NewRouter(h, slog.New(slog.NewJSONHandler(&logs, nil))), &logs
}

func get(router http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func neighbor(id int64, artist, genre string, score float64) vectors.Neighbor {
	return vectors.Neighbor{
		Track: vectors.Track{TrackID: id, Title: "t" + artist, Artist: artist, Genre: genre, X: 1, Y: 2},
		Score: score,
	}
}

func testStore() *fakeStore {
	return &fakeStore{
		tracks: map[int64]vectors.Track{2: {TrackID: 2, Title: "Food", Artist: "AWOL", Genre: "Hip-Hop", X: 7.6, Y: -3.3}},
		ranking: []vectors.Neighbor{
			neighbor(5, "AWOL", "Hip-Hop", 0.95),
			neighbor(10, "Kurt Vile", "Pop", 0.9),
			neighbor(11, "AWOL", "Hip-Hop", 0.85),
			neighbor(12, "Mattin", "Experimental", 0.8),
		},
	}
}

func TestSimilarResponse(t *testing.T) {
	store := testStore()
	router, logs := vectorsRouter(t, store)

	rec := get(router, "/api/v1/similar?track_id=2&limit=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	wantTrack := map[string]any{"track_id": 2.0, "title": "Food", "artist": "AWOL", "genre": "Hip-Hop", "x": 7.6, "y": -3.3}
	if !reflect.DeepEqual(body["track"], wantTrack) {
		t.Errorf("track = %v, want %v", body["track"], wantTrack)
	}
	neighbors := body["neighbors"].([]any)
	if len(neighbors) != 2 {
		t.Fatalf("neighbors = %v, want 2", neighbors)
	}
	first := neighbors[0].(map[string]any)
	for _, key := range []string{"track_id", "title", "artist", "genre", "score", "x", "y"} {
		if _, ok := first[key]; !ok {
			t.Errorf("neighbour is missing %q: %v", key, first)
		}
	}
	if first["track_id"] != 5.0 || first["score"] != 0.95 {
		t.Errorf("first neighbour = %v, want track 5 with score 0.95", first)
	}
	if !reflect.DeepEqual(store.limits, []int{2}) {
		t.Errorf("store limits = %v, want [2]", store.limits)
	}
	for _, want := range []string{`"path":"/api/v1/similar"`, `"track_id":2`, `"neighbors":2`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("access log is missing %s:\n%s", want, logs)
		}
	}
}

func TestSimilarDefaultLimit(t *testing.T) {
	store := testStore()
	router, _ := vectorsRouter(t, store)
	if rec := get(router, "/api/v1/similar?track_id=2"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !reflect.DeepEqual(store.limits, []int{defaultSimilarLimit}) {
		t.Errorf("store limits = %v, want [%d]", store.limits, defaultSimilarLimit)
	}
}

func TestSimilarExcludeSameArtist(t *testing.T) {
	store := testStore()
	router, _ := vectorsRouter(t, store)

	rec := get(router, "/api/v1/similar?track_id=2&limit=2&exclude_same_artist=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var body similarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, n := range body.Neighbors {
		ids = append(ids, n.TrackID)
	}
	if want := []int64{10, 12}; !reflect.DeepEqual(ids, want) {
		t.Errorf("neighbour ids = %v, want %v (AWOL filtered out)", ids, want)
	}
	// asked with a margin: max(2*6, 30) candidates
	if !reflect.DeepEqual(store.limits, []int{minCandidates}) {
		t.Errorf("store limits = %v, want [%d]", store.limits, minCandidates)
	}
}

func TestSimilarExcludeSameArtistWidensSearch(t *testing.T) {
	store := testStore()
	// minCandidates neighbours all by AWOL, then one other artist further down
	store.ranking = nil
	for i := range maxCandidates {
		artist := "AWOL"
		if i == minCandidates+5 {
			artist = "Mattin"
		}
		store.ranking = append(store.ranking, neighbor(int64(100+i), artist, "Experimental", 0.5))
	}
	router, _ := vectorsRouter(t, store)

	rec := get(router, "/api/v1/similar?track_id=2&limit=3&exclude_same_artist=1")
	var body similarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Neighbors) != 1 || body.Neighbors[0].Artist != "Mattin" {
		t.Errorf("neighbours = %+v, want only the Mattin track", body.Neighbors)
	}
	if !reflect.DeepEqual(store.limits, []int{minCandidates, maxCandidates}) {
		t.Errorf("store limits = %v, want [%d %d]", store.limits, minCandidates, maxCandidates)
	}
}

func TestSimilarEmptyNeighboursIsArray(t *testing.T) {
	store := testStore()
	store.ranking = nil
	router, _ := vectorsRouter(t, store)
	rec := get(router, "/api/v1/similar?track_id=2")
	if !strings.Contains(rec.Body.String(), `"neighbors":[]`) {
		t.Errorf("body = %s, want an empty neighbours array", rec.Body)
	}
}

func TestSimilarBadParameters(t *testing.T) {
	router, _ := vectorsRouter(t, testStore())
	for _, tt := range []struct{ query, wantInBody string }{
		{"", "track_id is required"},
		{"track_id=", "track_id is required"},
		{"track_id=abc", "positive integer"},
		{"track_id=0", "positive integer"},
		{"track_id=-3", "positive integer"},
		{"track_id=1.5", "positive integer"},
		{"track_id=99999999999999999999", "positive integer"},
		{"track_id=2&limit=0", "between 1 and 50"},
		{"track_id=2&limit=51", "between 1 and 50"},
		{"track_id=2&limit=ten", "between 1 and 50"},
		{"track_id=2&exclude_same_artist=maybe", "exclude_same_artist must be true or false"},
	} {
		rec := get(router, "/api/v1/similar?"+tt.query)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400", tt.query, rec.Code)
			continue
		}
		if msg := errorBody(t, rec); !strings.Contains(msg, tt.wantInBody) {
			t.Errorf("%q: error = %q, want it to contain %q", tt.query, msg, tt.wantInBody)
		}
	}
}

func TestSimilarNotFound(t *testing.T) {
	router, _ := vectorsRouter(t, testStore())
	rec := get(router, "/api/v1/similar?track_id=999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if msg := errorBody(t, rec); !strings.Contains(msg, "track_id 999") {
		t.Errorf("error = %q, want it to name the track", msg)
	}
}

func TestVectorStoreUnavailableIs502(t *testing.T) {
	store := testStore()
	store.err = &vectors.UpstreamError{Err: errors.New("dial tcp 10.0.0.7:6333: connection refused")}
	router, logs := vectorsRouter(t, store)

	for _, target := range []string{"/api/v1/similar?track_id=2", "/api/v1/tracks/search?q=food"} {
		rec := get(router, target)
		if rec.Code != http.StatusBadGateway {
			t.Errorf("%s: status = %d, want 502", target, rec.Code)
			continue
		}
		if rec.Header().Get("Retry-After") != "1" {
			t.Errorf("%s: Retry-After = %q, want 1", target, rec.Header().Get("Retry-After"))
		}
		if msg := errorBody(t, rec); strings.Contains(msg, "10.0.0.7") {
			t.Errorf("%s: error leaks the cause: %q", target, msg)
		}
	}
	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("the cause is not logged:\n%s", logs)
	}
}

func TestVectorEndpointsWithoutStoreAre503(t *testing.T) {
	normalizer, err := tags.Load("")
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(NewHandlers(&fakeSearcher{}, normalizer, testTimeout),
		slog.New(slog.NewJSONHandler(io.Discard, nil)))

	for _, target := range []string{"/api/v1/similar?track_id=2", "/api/v1/tracks/search?q=food"} {
		rec := get(router, target)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", target, rec.Code)
			continue
		}
		if msg := errorBody(t, rec); !strings.Contains(msg, "QDRANT_URL") {
			t.Errorf("%s: error = %q, want it to mention QDRANT_URL", target, msg)
		}
	}
	if rec := get(router, "/health"); rec.Code != http.StatusOK {
		t.Errorf("/health = %d without a vector store, want 200", rec.Code)
	}
}

func TestSearchTracks(t *testing.T) {
	store := testStore()
	router, _ := vectorsRouter(t, store)

	rec := get(router, "/api/v1/tracks/search?q=%20Kurt%20&limit=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var body searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tracks) != 2 {
		t.Errorf("tracks = %+v, want 2", body.Tracks)
	}
	if store.searchArg != "Kurt" {
		t.Errorf("store got q = %q, want it trimmed", store.searchArg)
	}

	store.ranking = nil
	rec = get(router, "/api/v1/tracks/search?q=zz")
	if !strings.Contains(rec.Body.String(), `"tracks":[]`) {
		t.Errorf("body = %s, want an empty tracks array", rec.Body)
	}
}

func TestSearchTracksBadParameters(t *testing.T) {
	router, _ := vectorsRouter(t, testStore())
	for _, query := range []string{
		"", "q=a", "q=%20a%20", "q=" + strings.Repeat("x", maxQueryRunes+1),
		"q=food&limit=0", "q=food&limit=51", "q=food&limit=x",
	} {
		if rec := get(router, "/api/v1/tracks/search?"+query); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400", query, rec.Code)
		}
	}
	// two characters are enough, counted as runes, not bytes
	if rec := get(router, "/api/v1/tracks/search?q=%D0%AF%D0%B9"); rec.Code != http.StatusOK {
		t.Errorf("q=Яй: status = %d, want 200", rec.Code)
	}
}

func TestMetricPathCoversVectorEndpoints(t *testing.T) {
	for _, path := range []string{"/api/v1/similar", "/api/v1/tracks/search"} {
		if got := metricPath(path); got != path {
			t.Errorf("metricPath(%q) = %q, want the path itself", path, got)
		}
	}
	if got := metricPath("/api/v1/similar/2"); got != "other" {
		t.Errorf("metricPath of an unknown sub-path = %q, want other", got)
	}
}
