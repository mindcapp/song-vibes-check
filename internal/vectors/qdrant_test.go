package vectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// fakeQdrant answers the three REST calls the store makes from a fixed set of
// points. Unknown routes fail the test.
type fakeQdrant struct {
	t        *testing.T
	points   []Track
	scrolls  atomic.Int32
	status   int // when non-zero, every request answers with it
	lastBody map[string]map[string]any
}

func (f *fakeQdrant) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("request body is not JSON: %v", err)
	}
	f.lastBody[r.URL.Path] = body
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	if got := r.Header.Get("api-key"); got != "secret" {
		f.t.Errorf("api-key header = %q, want %q", got, "secret")
	}

	asPoint := func(tr Track, score float64) map[string]any {
		return map[string]any{"id": tr.TrackID, "payload": tr, "score": score}
	}
	switch r.URL.Path {
	case "/collections/tracks/points":
		result := []any{}
		id := int64(body["ids"].([]any)[0].(float64))
		for _, p := range f.points {
			if p.TrackID == id {
				result = append(result, asPoint(p, 0))
			}
		}
		writeResult(w, result)
	case "/collections/tracks/points/query":
		writeResult(w, map[string]any{"points": []any{
			asPoint(f.points[1], 0.9), asPoint(f.points[2], 0.8),
		}})
	case "/collections/tracks/points/scroll":
		f.scrolls.Add(1)
		// two pages: the first two points, then the rest
		if body["offset"] == nil {
			writeResult(w, map[string]any{
				"points":           []any{asPoint(f.points[0], 0), asPoint(f.points[1], 0)},
				"next_page_offset": f.points[2].TrackID,
			})
			return
		}
		var rest []any
		for _, p := range f.points[2:] {
			rest = append(rest, asPoint(p, 0))
		}
		writeResult(w, map[string]any{"points": rest, "next_page_offset": nil})
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "status": "ok"})
}

var testPoints = []Track{
	{TrackID: 2, Title: "Food", Artist: "AWOL", Genre: "Hip-Hop", X: 1, Y: 2},
	{TrackID: 5, Title: "This World", Artist: "AWOL", Genre: "Hip-Hop", X: 1.5, Y: 2.5},
	{TrackID: 10, Title: "Freeway", Artist: "Kurt Vile", Genre: "Pop", X: -1, Y: 0},
	{TrackID: 20, Title: "Seafood Blues", Artist: "Foodies", Genre: "Folk", X: 3, Y: 3},
}

func newTestStore(t *testing.T, opts ...Option) (*Qdrant, *fakeQdrant) {
	t.Helper()
	fake := &fakeQdrant{t: t, points: testPoints, lastBody: map[string]map[string]any{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	store, err := NewQdrant(srv.URL+"/", "tracks", append([]Option{WithAPIKey("secret")}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return store, fake
}

func TestNewQdrantValidatesURL(t *testing.T) {
	for _, bad := range []string{"", "127.0.0.1:6333", "ftp://host", "http://"} {
		if _, err := NewQdrant(bad, "tracks"); err == nil {
			t.Errorf("NewQdrant(%q) accepted an invalid URL", bad)
		}
	}
	if _, err := NewQdrant("http://127.0.0.1:6333", ""); err == nil {
		t.Error("NewQdrant accepted an empty collection")
	}
}

func TestSimilar(t *testing.T) {
	store, fake := newTestStore(t)

	source, neighbors, err := store.Similar(context.Background(), 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*source, testPoints[0]) {
		t.Errorf("source = %+v, want %+v", *source, testPoints[0])
	}
	want := []Neighbor{{Track: testPoints[1], Score: 0.9}, {Track: testPoints[2], Score: 0.8}}
	if !reflect.DeepEqual(neighbors, want) {
		t.Errorf("neighbors = %+v, want %+v", neighbors, want)
	}

	query := fake.lastBody["/collections/tracks/points/query"]
	if query["query"] != float64(2) || query["limit"] != float64(2) {
		t.Errorf("query body = %v, want query 2 and limit 2", query)
	}
	excluded := fmt.Sprint(query["filter"])
	if excluded != "map[must_not:[map[has_id:[2]]]]" {
		t.Errorf("query filter = %s, want the source id excluded", excluded)
	}
}

func TestSimilarNotFound(t *testing.T) {
	store, _ := newTestStore(t)
	if _, _, err := store.Similar(context.Background(), 999, 5); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestUpstreamErrors(t *testing.T) {
	store, fake := newTestStore(t)
	fake.status = http.StatusInternalServerError

	_, _, err := store.Similar(context.Background(), 2, 5)
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Status != http.StatusInternalServerError {
		t.Errorf("err = %v, want UpstreamError with status 500", err)
	}
	if got := errorReason(err); got != "status_5xx" {
		t.Errorf("errorReason = %q, want status_5xx", got)
	}

	unreachable, err := NewQdrant("http://127.0.0.1:1", "tracks")
	if err != nil {
		t.Fatal(err)
	}
	_, err = unreachable.Search(context.Background(), "food", 5)
	if !errors.Is(err, ErrUpstream) || errorReason(err) != "network" {
		t.Errorf("err = %v (reason %q), want a network UpstreamError", err, errorReason(err))
	}
}

func TestSearchIsCaseInsensitiveAndRanked(t *testing.T) {
	store, fake := newTestStore(t)

	got, err := store.Search(context.Background(), "FOOD", 10)
	if err != nil {
		t.Fatal(err)
	}
	// title prefix, then artist prefix, then substring of the title
	var ids []int64
	for _, tr := range got {
		ids = append(ids, tr.TrackID)
	}
	if want := []int64{2, 20}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}

	got, _ = store.Search(context.Background(), "awol", 1)
	if len(got) != 1 || got[0].TrackID != 2 {
		t.Errorf("limit 1 = %+v, want only track 2", got)
	}
	if n := fake.scrolls.Load(); n != 2 {
		t.Errorf("scroll requests = %d, want 2 (one catalog load over two pages)", n)
	}
}

func TestSearchRefreshesCatalogAndServesStaleOnError(t *testing.T) {
	store, fake := newTestStore(t, WithCatalogTTL(time.Minute))
	now := time.Now()
	store.now = func() time.Time { return now }

	if _, err := store.Search(context.Background(), "food", 5); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	fake.status = http.StatusServiceUnavailable
	got, err := store.Search(context.Background(), "food", 5)
	if err != nil || len(got) != 2 {
		t.Errorf("stale search = %v, %v; want the old catalog", got, err)
	}
	if n := fake.scrolls.Load(); n != 2 {
		t.Errorf("scroll requests = %d, want 2 (the failed refresh is not counted by the fake)", n)
	}
}
