package provider

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testUserAgent = "song-vibes-check-test/0.0.0 ( test@example.com )"

func newTestClient(t *testing.T, handler http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	opts = append([]Option{WithBaseURL(srv.URL), WithRateInterval(0)}, opts...)
	return NewClient(testUserAgent, opts...)
}

func jsonHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// fakeMB serves a recording search and artist lookups, and records the paths
// it was asked for.
type fakeMB struct {
	search       string
	searchStatus int
	artists      map[string]string
	artistStatus int
	paths        []string
}

func (f *fakeMB) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.paths = append(f.paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		if id, ok := strings.CutPrefix(r.URL.Path, "/artist/"); ok {
			body, found := f.artists[id]
			switch {
			case f.artistStatus != 0:
				w.WriteHeader(f.artistStatus)
			case !found:
				w.WriteHeader(http.StatusNotFound)
			}
			_, _ = w.Write([]byte(body))
			return
		}

		if f.searchStatus != 0 {
			w.WriteHeader(f.searchStatus)
		}
		_, _ = w.Write([]byte(f.search))
	}
}

// rec builds one search result. tags is a list of "name:count" pairs.
func rec(id string, score int, artist string, tags ...string) string {
	var entries []string
	for _, t := range tags {
		name, count, _ := strings.Cut(t, ":")
		entries = append(entries, fmt.Sprintf(`{"name":%q,"count":%s}`, name, count))
	}
	return fmt.Sprintf(
		`{"id":%q,"score":%d,"title":"Song","artist-credit":[{"name":%q,"artist":{"id":"artist-1","name":%q}}],"tags":[%s]}`,
		id, score, artist, artist, strings.Join(entries, ","))
}

func searchBody(recordings ...string) string {
	return `{"recordings":[` + strings.Join(recordings, ",") + `]}`
}

func TestSearchTrack(t *testing.T) {
	queenTags := `{"tags":[{"name":"Rock","count":9},{"name":"glam rock","count":4}],"genres":[{"name":"rock","count":9}]}`

	tests := []struct {
		name       string
		mb         fakeMB
		artist     string
		wantErr    error
		wantAnyErr bool
		wantTrack  *Track
		wantPaths  []string
	}{
		{
			name: "merges tags of matching recordings, summing votes",
			mb: fakeMB{search: searchBody(
				rec("rec-b", 100, "Queen", "rock:2", "glam rock:1"),
				rec("rec-a", 95, "Queen", "Rock:3", " art rock :1"),
				rec("rec-c", 100, "Queen"),
			)},
			wantTrack: &Track{
				ID: "rec-a", Title: "Song", Artist: "Queen",
				Tags:         []string{"rock", "art rock", "glam rock"},
				WeightedTags: []Tag{{"rock", 5}, {"art rock", 1}, {"glam rock", 1}},
				TagSource:    TagSourceRecording,
			},
			wantPaths: []string{"/recording/"},
		},
		{
			name: "ignores recordings scored below 90",
			mb: fakeMB{search: searchBody(
				rec("rec-low", 89, "Queen", "jazz:50"),
				rec("rec-ok", 90, "Queen", "rock:1"),
			)},
			wantTrack: &Track{
				ID: "rec-ok", Title: "Song", Artist: "Queen",
				Tags: []string{"rock"}, WeightedTags: []Tag{{"rock", 1}}, TagSource: TagSourceRecording,
			},
		},
		{
			name: "ignores recordings by another artist",
			mb: fakeMB{search: searchBody(
				rec("rec-cover", 100, "Queen Tribute Band", "karaoke:50"),
				rec("rec-ok", 100, "Queen", "rock:1"),
			)},
			wantTrack: &Track{
				ID: "rec-ok", Title: "Song", Artist: "Queen",
				Tags: []string{"rock"}, WeightedTags: []Tag{{"rock", 1}}, TagSource: TagSourceRecording,
			},
		},
		{
			name: "merges at most five recordings, the best tagged ones",
			mb: fakeMB{search: searchBody(
				rec("rec-1", 100, "Queen", "rock:1"),
				rec("rec-2", 100, "Queen", "rock:2"),
				rec("rec-3", 100, "Queen", "rock:3"),
				rec("rec-4", 100, "Queen", "rock:4"),
				rec("rec-5", 100, "Queen", "rock:5"),
				rec("rec-6", 100, "Queen", "rock:6"),
				rec("rec-7", 100, "Queen", "pop:1"),
			)},
			wantTrack: &Track{
				ID: "rec-6", Title: "Song", Artist: "Queen",
				// rec-6..rec-2: 6+5+4+3+2. rec-1 and rec-7 tie on votes; neither fits.
				Tags: []string{"rock"}, WeightedTags: []Tag{{"rock", 20}}, TagSource: TagSourceRecording,
			},
		},
		{
			name:   "artist name is compared after normalization",
			artist: "guns n roses",
			mb: fakeMB{search: searchBody(
				rec("rec-1", 100, "Guns N\u2019 Roses", "hard rock:4"),
			)},
			wantTrack: &Track{
				ID: "rec-1", Title: "Song", Artist: "Guns N\u2019 Roses",
				Tags: []string{"hard rock"}, WeightedTags: []Tag{{"hard rock", 4}}, TagSource: TagSourceRecording,
			},
		},
		{
			name: "falls back to artist tags when no matching recording has any",
			mb: fakeMB{
				search:  searchBody(rec("rec-1", 100, "Queen"), rec("rec-2", 100, "Queen")),
				artists: map[string]string{"artist-1": queenTags},
			},
			wantTrack: &Track{
				ID: "rec-1", Title: "Song", Artist: "Queen",
				Tags:         []string{"rock", "glam rock"},
				WeightedTags: []Tag{{"rock", 9}, {"glam rock", 4}},
				TagSource:    TagSourceArtist,
			},
			wantPaths: []string{"/recording/", "/artist/artist-1"},
		},
		{
			name: "does not use tags of non-matching recordings before falling back",
			mb: fakeMB{
				search:  searchBody(rec("rec-low", 50, "Queen", "jazz:9"), rec("rec-1", 100, "Queen")),
				artists: map[string]string{"artist-1": queenTags},
			},
			wantTrack: &Track{
				ID: "rec-1", Title: "Song", Artist: "Queen",
				Tags:         []string{"rock", "glam rock"},
				WeightedTags: []Tag{{"rock", 9}, {"glam rock", 4}},
				TagSource:    TagSourceArtist,
			},
		},
		{
			name: "no tags on recordings or artist",
			mb: fakeMB{
				search:  searchBody(rec("rec-1", 100, "Queen")),
				artists: map[string]string{"artist-1": `{"tags":[],"genres":[]}`},
			},
			wantTrack: &Track{ID: "rec-1", Title: "Song", Artist: "Queen"},
		},
		{
			name:      "artist unknown to the lookup endpoint means no tags, not an error",
			mb:        fakeMB{search: searchBody(rec("rec-1", 100, "Queen"))},
			wantTrack: &Track{ID: "rec-1", Title: "Song", Artist: "Queen"},
		},
		{
			name: "artist fallback failing upstream is an error",
			mb: fakeMB{
				search:       searchBody(rec("rec-1", 100, "Queen")),
				artistStatus: http.StatusServiceUnavailable,
			},
			wantErr: ErrUpstream,
		},
		{
			name:    "no recordings",
			mb:      fakeMB{search: `{"recordings":[]}`},
			wantErr: ErrNotFound,
		},
		{
			name: "recordings exist but none matches",
			mb: fakeMB{search: searchBody(
				rec("rec-low", 80, "Queen", "rock:1"),
				rec("rec-other", 100, "Someone Else", "rock:1"),
			)},
			wantErr:   ErrNotFound,
			wantPaths: []string{"/recording/"},
		},
		{
			name:    "service unavailable",
			mb:      fakeMB{search: `{"error":"rate limited"}`, searchStatus: http.StatusServiceUnavailable},
			wantErr: ErrUpstream,
		},
		{
			name:       "malformed JSON",
			mb:         fakeMB{search: `{"recordings":`},
			wantAnyErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mb := tt.mb
			client := newTestClient(t, mb.handler())

			artist := tt.artist
			if artist == "" {
				artist = "Queen"
			}
			track, err := client.SearchTrack(context.Background(), artist, "Song")

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
			case tt.wantAnyErr:
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				want := *tt.wantTrack
				want.Artist = strings.ReplaceAll(want.Artist, `\u2019`, "\u2019")
				if !reflect.DeepEqual(track, &want) {
					t.Errorf("track = %+v\nwant    %+v", track, &want)
				}
			}

			if tt.wantPaths != nil && !reflect.DeepEqual(mb.paths, tt.wantPaths) {
				t.Errorf("requested paths = %v, want %v", mb.paths, tt.wantPaths)
			}
		})
	}
}

func TestSearchTrackIsStableUnderResultReordering(t *testing.T) {
	recordings := []string{
		rec("rec-1", 100, "Queen", "rock:3", "glam rock:1"),
		rec("rec-2", 100, "Queen"),
		rec("rec-3", 100, "Queen", "rock:1", "art rock:2"),
		rec("rec-4", 100, "Queen", "pop:1"),
		rec("rec-5", 100, "Queen", "pop:1"),
		rec("rec-6", 100, "Queen", "opera:1"),
		rec("rec-7", 100, "Queen", "rock:1"),
		rec("rec-8", 100, "Queen"),
	}

	var first *Track
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20; i++ {
		rng.Shuffle(len(recordings), func(a, b int) { recordings[a], recordings[b] = recordings[b], recordings[a] })

		mb := fakeMB{search: searchBody(recordings...)}
		track, err := newTestClient(t, mb.handler()).SearchTrack(context.Background(), "Queen", "Song")
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if first == nil {
			first = track
			continue
		}
		if !reflect.DeepEqual(track, first) {
			t.Fatalf("shuffle %d changed the result:\n got  %+v\n want %+v", i, track, first)
		}
	}
}

func TestNormalizeArtist(t *testing.T) {
	same := [][]string{
		{"Guns N' Roses", "guns n roses", "Guns N\u2019 Roses", "GUNS  N  ROSES"},
		{"The Beatles", "beatles", "the  beatles"},
		{"Beyonc\u00e9", "beyonce", "BEYONCE"},
		{"Simon & Garfunkel", "simon and garfunkel"},
		{"AC/DC", "ac dc", "AC-DC"},
		{"Sigur R\u00f3s", "sigur ros"},
	}
	for _, group := range same {
		want := normalizeArtist(group[0])
		if want == "" {
			t.Errorf("normalizeArtist(%q) is empty", group[0])
		}
		for _, name := range group[1:] {
			if got := normalizeArtist(name); got != want {
				t.Errorf("normalizeArtist(%q) = %q, want %q (same as %q)", name, got, want, group[0])
			}
		}
	}

	different := [][2]string{{"Queen", "Queens of the Stone Age"}, {"The The", "The"}, {"Prince", "Princess"}}
	for _, pair := range different {
		if normalizeArtist(pair[0]) == normalizeArtist(pair[1]) {
			t.Errorf("normalizeArtist treats %q and %q as the same artist", pair[0], pair[1])
		}
	}
}

func TestSearchTrackMatchesAnyCreditedArtistOrTheWholeCredit(t *testing.T) {
	body := `{"recordings":[{"id":"rec-1","score":100,"title":"Under Pressure",
		"artist-credit":[
			{"name":"Queen","joinphrase":" & ","artist":{"id":"queen-id","name":"Queen"}},
			{"name":"David Bowie","artist":{"id":"bowie-id","name":"David Bowie"}}],
		"tags":[{"name":"rock","count":2}]}]}`

	for _, artist := range []string{"Queen", "david bowie", "Queen & David Bowie", "queen and david bowie"} {
		mb := fakeMB{search: body}
		track, err := newTestClient(t, mb.handler()).SearchTrack(context.Background(), artist, "Under Pressure")
		if err != nil {
			t.Errorf("artist %q: %v", artist, err)
			continue
		}
		if len(track.Tags) != 1 {
			t.Errorf("artist %q: tags = %v", artist, track.Tags)
		}
	}

	mb := fakeMB{search: body}
	if _, err := newTestClient(t, mb.handler()).SearchTrack(context.Background(), "Freddie Mercury", "Under Pressure"); !errors.Is(err, ErrNotFound) {
		t.Errorf("uncredited artist: error = %v, want ErrNotFound", err)
	}
}

const okSearch = `{"recordings":[{"id":"x","score":100,"artist-credit":[{"name":"a","artist":{"id":"a-id","name":"a"}}],"tags":[{"name":"rock","count":1}]}]}`

func TestSearchTrackRequest(t *testing.T) {
	var got *http.Request
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		jsonHandler(http.StatusOK, `{"recordings":[]}`)(w, r)
	})

	_, _ = client.SearchTrack(context.Background(), `AC/DC "live"`, `Back\In Black`)

	if got.URL.Path != "/recording/" {
		t.Errorf("path = %q, want /recording/", got.URL.Path)
	}
	if ua := got.Header.Get("User-Agent"); ua != testUserAgent {
		t.Errorf("User-Agent = %q, want %q", ua, testUserAgent)
	}
	if accept := got.Header.Get("Accept"); accept != "application/json" {
		t.Errorf("Accept = %q, want application/json", accept)
	}

	q := got.URL.Query()
	wantQuery := `artist:"AC/DC \"live\"" AND recording:"Back\\In Black"`
	if q.Get("query") != wantQuery {
		t.Errorf("query = %q, want %q", q.Get("query"), wantQuery)
	}
	for key, want := range map[string]string{"fmt": "json", "limit": "100"} {
		if q.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, q.Get(key), want)
		}
	}
}

func TestArtistFallbackIsRateLimitedToo(t *testing.T) {
	const interval = 150 * time.Millisecond

	mb := fakeMB{
		search:  searchBody(rec("rec-1", 100, "Queen")),
		artists: map[string]string{"artist-1": `{"tags":[{"name":"rock","count":1}]}`},
	}
	client := newTestClient(t, mb.handler(), WithRateInterval(interval))

	start := time.Now()
	track, err := client.SearchTrack(context.Background(), "Queen", "Song")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if track.TagSource != TagSourceArtist {
		t.Fatalf("TagSource = %q, want artist fallback", track.TagSource)
	}
	if elapsed := time.Since(start); elapsed < interval {
		t.Errorf("search and artist lookup took %v, want them at least %v apart", elapsed, interval)
	}
}

func slowHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
}

func TestSearchTrackClientTimeout(t *testing.T) {
	client := newTestClient(t, slowHandler(t), WithTimeout(50*time.Millisecond))

	start := time.Now()
	_, err := client.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody")

	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("request took %v, timeout was not applied", elapsed)
	}
}

func TestSearchTrackContextDeadline(t *testing.T) {
	client := newTestClient(t, slowHandler(t))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.SearchTrack(ctx, "Queen", "Bohemian Rhapsody")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestSearchTrackRateLimit(t *testing.T) {
	const interval = 150 * time.Millisecond

	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		jsonHandler(http.StatusOK, okSearch)(w, r)
	}, WithRateInterval(interval))

	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := client.SearchTrack(context.Background(), "a", "b"); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
	}

	if elapsed := time.Since(start); elapsed < 2*interval {
		t.Errorf("3 requests took %v, want at least %v", elapsed, 2*interval)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("server saw %d requests, want 3", got)
	}
}

func TestSearchTrackRateLimitHonoursContext(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		jsonHandler(http.StatusOK, okSearch)(w, r)
	}, WithRateInterval(time.Hour))

	if _, err := client.SearchTrack(context.Background(), "a", "b"); err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.SearchTrack(ctx, "a", "b")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("server saw %d requests, want 1 (second must not be sent)", got)
	}
}
