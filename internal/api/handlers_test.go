package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"song-similarity/internal/provider"
	"song-similarity/internal/tags"
)

type fakeSearcher struct {
	tracks map[string]*provider.Track
	errs   map[string]error
	calls  []string
}

func (f *fakeSearcher) SearchTrack(_ context.Context, artist, title string) (*provider.Track, error) {
	key := artist + "/" + title
	f.calls = append(f.calls, key)
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	if track, ok := f.tracks[key]; ok {
		return track, nil
	}
	return nil, provider.ErrNotFound
}

func track(weighted ...provider.Tag) *provider.Track {
	names := make([]string, len(weighted))
	for i, t := range weighted {
		names[i] = t.Name
	}
	return &provider.Track{Tags: names, WeightedTags: weighted}
}

func newTestRouter(t *testing.T, searcher TrackSearcher) http.Handler {
	t.Helper()
	normalizer, err := tags.Load("")
	if err != nil {
		t.Fatalf("load tags: %v", err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewRouter(NewHandlers(searcher, normalizer), logger)
}

func postCompare(t *testing.T, router http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/compare", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

const compareBody = `{"song_a":{"artist":"A","title":"One"},"song_b":{"artist":"B","title":"Two"}}`

func TestCompareResponse(t *testing.T) {
	searcher := &fakeSearcher{tracks: map[string]*provider.Track{
		"A/One": track(
			provider.Tag{Name: "hip-hop", Count: 3},
			provider.Tag{Name: "hiphop", Count: 1},
			provider.Tag{Name: "seen live", Count: 9},
			provider.Tag{Name: "1994", Count: 2},
			provider.Tag{Name: "jazz rap", Count: 4},
		),
		"B/Two": track(
			provider.Tag{Name: "hip hop", Count: 6},
			provider.Tag{Name: "american", Count: 5},
			provider.Tag{Name: "trap", Count: 2},
		),
	}}

	rec := postCompare(t, newTestRouter(t, searcher), compareBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	var got struct {
		GenreScore    *float64 `json:"genre_score"`
		WeightedScore *float64 `json:"weighted_score"`
		TagsA         []string `json:"tags_a"`
		TagsB         []string `json:"tags_b"`
		SharedTags    []string `json:"shared_tags"`
		OnlyA         []string `json:"only_a"`
		OnlyB         []string `json:"only_b"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got.GenreScore == nil || got.WeightedScore == nil {
		t.Fatalf("missing score fields in %s", rec.Body)
	}

	// Normalized: A = {hip hop: 4, jazz rap: 4}, B = {hip hop: 6, trap: 2}.
	if want := 1.0 / 3.0; math.Abs(*got.GenreScore-want) > 1e-9 {
		t.Errorf("genre_score = %v, want %v", *got.GenreScore, want)
	}
	// Shares: A = .5/.5, B = .75/.25 -> min .5, max .75 + .5 + .25 = 1.5.
	if want := 0.5 / 1.5; math.Abs(*got.WeightedScore-want) > 1e-9 {
		t.Errorf("weighted_score = %v, want %v", *got.WeightedScore, want)
	}

	wantRawA := []string{"hip-hop", "hiphop", "seen live", "1994", "jazz rap"}
	if !reflect.DeepEqual(got.TagsA, wantRawA) {
		t.Errorf("tags_a = %v, want raw tags %v", got.TagsA, wantRawA)
	}
	wantRawB := []string{"hip hop", "american", "trap"}
	if !reflect.DeepEqual(got.TagsB, wantRawB) {
		t.Errorf("tags_b = %v, want raw tags %v", got.TagsB, wantRawB)
	}

	if want := []string{"hip hop"}; !reflect.DeepEqual(got.SharedTags, want) {
		t.Errorf("shared_tags = %v, want %v", got.SharedTags, want)
	}
	if want := []string{"jazz rap"}; !reflect.DeepEqual(got.OnlyA, want) {
		t.Errorf("only_a = %v, want %v", got.OnlyA, want)
	}
	if want := []string{"trap"}; !reflect.DeepEqual(got.OnlyB, want) {
		t.Errorf("only_b = %v, want %v", got.OnlyB, want)
	}
}

func TestCompareEmptyListsAreArrays(t *testing.T) {
	searcher := &fakeSearcher{tracks: map[string]*provider.Track{
		"A/One": track(provider.Tag{Name: "rock", Count: 1}),
		"B/Two": track(provider.Tag{Name: "rock", Count: 1}),
	}}

	rec := postCompare(t, newTestRouter(t, searcher), compareBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, field := range []string{"only_a", "only_b"} {
		if string(raw[field]) != "[]" {
			t.Errorf("%s = %s, want []", field, raw[field])
		}
	}
}

func TestCompareBadRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "invalid JSON", body: `{"song_a":`},
		{name: "missing song_a title", body: `{"song_a":{"artist":"A"},"song_b":{"artist":"B","title":"Two"}}`},
		{name: "missing song_b artist", body: `{"song_a":{"artist":"A","title":"One"},"song_b":{"title":"Two"}}`},
		{name: "empty object", body: `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			searcher := &fakeSearcher{}
			rec := postCompare(t, newTestRouter(t, searcher), tt.body)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if len(searcher.calls) != 0 {
				t.Errorf("searcher was called %v for an invalid request", searcher.calls)
			}
		})
	}
}

func TestHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	newTestRouter(t, &fakeSearcher{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Errorf("health = %d %s", rec.Code, rec.Body)
	}
}
