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
	"time"

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

func track(recording ...provider.Tag) *provider.Track {
	names := make([]string, len(recording))
	for i, t := range recording {
		names[i] = t.Name
	}
	return &provider.Track{Tags: names, RecordingTags: recording, TagSource: provider.TagSourceRecording}
}

func newTestRouter(t *testing.T, searcher TrackSearcher) http.Handler {
	t.Helper()
	normalizer, err := tags.Load("")
	if err != nil {
		t.Fatalf("load tags: %v", err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewRouter(NewHandlers(searcher, normalizer, testTimeout), logger)
}

func postCompare(t *testing.T, router http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/compare", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

const testTimeout = 5 * time.Second

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
		TagSourceA    string   `json:"tag_source_a"`
		TagSourceB    string   `json:"tag_source_b"`
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
	if got.TagSourceA != "recording" || got.TagSourceB != "recording" {
		t.Errorf("tag sources = %q / %q, want recording / recording", got.TagSourceA, got.TagSourceB)
	}
}

func TestCompareWeighsArtistTagsAtHalf(t *testing.T) {
	mixed := &provider.Track{
		Tags:          []string{"rock", "jazz", "pop"},
		RecordingTags: []provider.Tag{{Name: "rock", Count: 1}, {Name: "jazz", Count: 1}},
		// Many more votes than the recording tags: only shares must matter.
		ArtistTags: []provider.Tag{{Name: "rock", Count: 50}, {Name: "pop", Count: 50}},
		TagSource:  provider.TagSourceMixed,
	}
	byArtist := &provider.Track{
		Tags:       []string{"rock", "pop"},
		ArtistTags: []provider.Tag{{Name: "rock", Count: 3}, {Name: "pop", Count: 1}},
		TagSource:  provider.TagSourceArtist,
	}
	searcher := &fakeSearcher{tracks: map[string]*provider.Track{"A/One": mixed, "B/Two": byArtist}}

	rec := postCompare(t, newTestRouter(t, searcher), compareBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	var got struct {
		GenreScore    float64  `json:"genre_score"`
		WeightedScore float64  `json:"weighted_score"`
		SharedTags    []string `json:"shared_tags"`
		OnlyA         []string `json:"only_a"`
		TagSourceA    string   `json:"tag_source_a"`
		TagSourceB    string   `json:"tag_source_b"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// A: recording shares rock .5, jazz .5; artist shares rock .5, pop .5 at half
	//    weight -> rock .75, jazz .5, pop .25 (total 1.5) -> .5, .333, .167.
	// B: rock .75, pop .25 (the 0.5 weight cancels out when it is the only source).
	// min: rock .5 + pop .167 = .667; max: rock .75 + jazz .333 + pop .25 = 1.333.
	if want := (0.5 + 0.25/1.5) / (0.75 + 0.5/1.5 + 0.25); math.Abs(got.WeightedScore-want) > 1e-9 {
		t.Errorf("weighted_score = %v, want %v", got.WeightedScore, want)
	}
	if want := 2.0 / 3.0; math.Abs(got.GenreScore-want) > 1e-9 {
		t.Errorf("genre_score = %v, want %v", got.GenreScore, want)
	}
	if want := []string{"rock", "pop"}; !reflect.DeepEqual(got.SharedTags, want) {
		t.Errorf("shared_tags = %v, want %v", got.SharedTags, want)
	}
	if want := []string{"jazz"}; !reflect.DeepEqual(got.OnlyA, want) {
		t.Errorf("only_a = %v, want %v", got.OnlyA, want)
	}
	if got.TagSourceA != "mixed" || got.TagSourceB != "artist" {
		t.Errorf("tag sources = %q / %q, want mixed / artist", got.TagSourceA, got.TagSourceB)
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

func TestCompareLogsErrorText(t *testing.T) {
	searcher := &fakeSearcher{
		tracks: map[string]*provider.Track{"A/One": track(provider.Tag{Name: "rock", Count: 1})},
		errs:   map[string]error{"B/Two": provider.ErrUpstream},
	}
	normalizer, err := tags.Load("")
	if err != nil {
		t.Fatalf("load tags: %v", err)
	}

	var logs strings.Builder
	router := NewRouter(NewHandlers(searcher, normalizer, testTimeout), slog.New(slog.NewJSONHandler(&logs, nil)))

	rec := postCompare(t, router, compareBody)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}

	var line map[string]any
	if err := json.Unmarshal([]byte(logs.String()), &line); err != nil {
		t.Fatalf("decode log line %q: %v", logs.String(), err)
	}
	if msg, _ := line["error"].(string); !strings.Contains(msg, "upstream error") {
		t.Errorf("log error = %q, want the upstream error text", msg)
	}
	if line["failed_song"] != "song_b" {
		t.Errorf("failed_song = %v, want song_b", line["failed_song"])
	}
	if line["cache_a"] != nil {
		t.Errorf("cache_a logged on a failed request: %v", line["cache_a"])
	}
}

func TestCompareLowData(t *testing.T) {
	tags := func(names ...string) *provider.Track {
		var list []provider.Tag
		for _, name := range names {
			list = append(list, provider.Tag{Name: name, Count: 1})
		}
		return track(list...)
	}

	tests := []struct {
		name string
		a, b *provider.Track
		want bool
	}{
		{name: "three tags each", a: tags("rock", "pop", "jazz"), b: tags("rock", "blues", "folk"), want: false},
		{name: "two tags on side a", a: tags("rock", "pop"), b: tags("rock", "blues", "folk"), want: true},
		{name: "two tags on side b", a: tags("rock", "pop", "jazz"), b: tags("rock", "blues"), want: true},
		{
			// Three raw tags, but only two survive: the synonyms merge and the stop list bites.
			name: "counted after normalization",
			a:    tags("hip hop", "hip-hop", "seen live", "rap"),
			b:    tags("rock", "blues", "folk"),
			want: true,
		},
		{
			name: "artist tags count towards the total",
			a: &provider.Track{
				Tags:          []string{"rock", "pop", "jazz"},
				RecordingTags: []provider.Tag{{Name: "rock", Count: 1}},
				ArtistTags:    []provider.Tag{{Name: "pop", Count: 1}, {Name: "jazz", Count: 1}},
				TagSource:     provider.TagSourceMixed,
			},
			b:    tags("rock", "blues", "folk"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			searcher := &fakeSearcher{tracks: map[string]*provider.Track{"A/One": tt.a, "B/Two": tt.b}}

			rec := postCompare(t, newTestRouter(t, searcher), compareBody)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
			}

			var got map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if want := map[bool]string{true: "true", false: "false"}[tt.want]; string(got["low_data"]) != want {
				t.Errorf("low_data = %s, want %s", got["low_data"], want)
			}
		})
	}
}
