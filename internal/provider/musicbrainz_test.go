package provider

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
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

// fakeMB stands in for MusicBrainz. It serves the tagged-only search, the
// plain search and artist lookups, and records every request it receives.
type fakeMB struct {
	mu      sync.Mutex
	tagged  string            // body for searches restricted with tag:*
	plain   string            // body for unrestricted searches
	artists map[string]string // artist MBID -> body
	// fail lists statuses to answer with before serving normally, one per
	// request; retryAfter is sent along with them when set.
	fail       []int
	retryAfter string
	requests   []string
}

func (f *fakeMB) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		kind := "plain"
		id, isArtist := strings.CutPrefix(r.URL.Path, "/artist/")
		switch {
		case isArtist:
			kind = "artist:" + id
		case strings.Contains(r.URL.Query().Get("query"), "tag:*"):
			kind = "tagged"
		}
		f.requests = append(f.requests, kind)

		w.Header().Set("Content-Type", "application/json")
		if len(f.fail) > 0 {
			status := f.fail[0]
			f.fail = f.fail[1:]
			if f.retryAfter != "" {
				w.Header().Set("Retry-After", f.retryAfter)
			}
			w.WriteHeader(status)
			return
		}

		body := `{"recordings":[]}`
		switch {
		case isArtist:
			var found bool
			if body, found = f.artists[id]; !found {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		case kind == "tagged" && f.tagged != "":
			body = f.tagged
		case kind == "plain" && f.plain != "":
			body = f.plain
		}
		_, _ = w.Write([]byte(body))
	}
}

func (f *fakeMB) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
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
	tests := []struct {
		name         string
		mb           *fakeMB
		artist       string
		wantErr      error
		wantAnyErr   bool
		wantTrack    *Track
		wantRequests []string
	}{
		{
			name: "merges tags of matching recordings, summing votes",
			mb: &fakeMB{tagged: searchBody(
				rec("rec-b", 100, "Queen", "rock:2", "glam rock:1"),
				rec("rec-a", 95, "Queen", "Rock:3", " art rock :1"),
			)},
			wantTrack: &Track{
				ID: "rec-a", Title: "Song", Artist: "Queen", ArtistID: "artist-1",
				Tags:          []string{"rock", "art rock", "glam rock"},
				RecordingTags: []Tag{{"rock", 5}, {"art rock", 1}, {"glam rock", 1}},
			},
			wantRequests: []string{"tagged"},
		},
		{
			name: "ignores recordings scored below 90",
			mb: &fakeMB{tagged: searchBody(
				rec("rec-low", 89, "Queen", "jazz:50"),
				rec("rec-ok", 90, "Queen", "rock:1"),
			)},
			wantTrack: &Track{
				ID: "rec-ok", Title: "Song", Artist: "Queen", ArtistID: "artist-1",
				Tags: []string{"rock"}, RecordingTags: []Tag{{"rock", 1}},
			},
		},
		{
			name: "ignores recordings by another artist",
			mb: &fakeMB{tagged: searchBody(
				rec("rec-cover", 100, "Queen Tribute Band", "karaoke:50"),
				rec("rec-ok", 100, "Queen", "rock:1"),
			)},
			wantTrack: &Track{
				ID: "rec-ok", Title: "Song", Artist: "Queen", ArtistID: "artist-1",
				Tags: []string{"rock"}, RecordingTags: []Tag{{"rock", 1}},
			},
		},
		{
			name: "merges at most five recordings, the best tagged ones",
			mb: &fakeMB{tagged: searchBody(
				rec("rec-1", 100, "Queen", "rock:1"),
				rec("rec-2", 100, "Queen", "rock:2"),
				rec("rec-3", 100, "Queen", "rock:3"),
				rec("rec-4", 100, "Queen", "rock:4"),
				rec("rec-5", 100, "Queen", "rock:5"),
				rec("rec-6", 100, "Queen", "rock:6"),
				rec("rec-7", 100, "Queen", "pop:1"),
			)},
			wantTrack: &Track{
				ID: "rec-6", Title: "Song", Artist: "Queen", ArtistID: "artist-1",
				// rec-6..rec-2: 6+5+4+3+2. rec-1 and rec-7 do not fit.
				Tags: []string{"rock"}, RecordingTags: []Tag{{"rock", 20}},
			},
		},
		{
			name:   "artist name is compared after normalization",
			artist: "guns n roses",
			mb:     &fakeMB{tagged: searchBody(rec("rec-1", 100, "Guns N’ Roses", "hard rock:4"))},
			wantTrack: &Track{
				ID: "rec-1", Title: "Song", Artist: "Guns N’ Roses", ArtistID: "artist-1",
				Tags: []string{"hard rock"}, RecordingTags: []Tag{{"hard rock", 4}},
			},
		},
		{
			name: "score sent as a string is accepted",
			mb: &fakeMB{tagged: `{"recordings":[{"id":"rec-1","score":"100","title":"Song",
				"artist-credit":[{"name":"Queen","artist":{"id":"artist-1","name":"Queen"}}],
				"tags":[{"name":"rock","count":1}]}]}`},
			wantTrack: &Track{
				ID: "rec-1", Title: "Song", Artist: "Queen", ArtistID: "artist-1",
				Tags: []string{"rock"}, RecordingTags: []Tag{{"rock", 1}},
			},
		},
		{
			name: "no tagged recording: plain search finds the song and its artist",
			mb: &fakeMB{plain: searchBody(
				rec("rec-9", 100, "Queen"),
				rec("rec-2", 100, "Queen"),
				rec("rec-other", 100, "Someone Else"),
			)},
			wantTrack:    &Track{ID: "rec-2", Title: "Song", Artist: "Queen", ArtistID: "artist-1"},
			wantRequests: []string{"tagged", "plain"},
		},
		{
			name: "tagged recordings that do not match are not used",
			mb: &fakeMB{
				tagged: searchBody(rec("rec-low", 50, "Queen", "jazz:9"), rec("rec-other", 100, "Someone Else", "pop:9")),
				plain:  searchBody(rec("rec-1", 100, "Queen")),
			},
			wantTrack:    &Track{ID: "rec-1", Title: "Song", Artist: "Queen", ArtistID: "artist-1"},
			wantRequests: []string{"tagged", "plain"},
		},
		{
			name:         "no recordings at all",
			mb:           &fakeMB{},
			wantErr:      ErrNotFound,
			wantRequests: []string{"tagged", "plain"},
		},
		{
			name: "recordings exist but none matches",
			mb: &fakeMB{plain: searchBody(
				rec("rec-low", 80, "Queen"),
				rec("rec-other", 100, "Someone Else"),
			)},
			wantErr: ErrNotFound,
		},
		{
			name:       "malformed JSON",
			mb:         &fakeMB{tagged: `{"recordings":`},
			wantAnyErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, tt.mb.handler())

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
				if !reflect.DeepEqual(track, tt.wantTrack) {
					t.Errorf("track = %+v\nwant    %+v", track, tt.wantTrack)
				}
			}

			if tt.wantRequests != nil && !reflect.DeepEqual(tt.mb.seen(), tt.wantRequests) {
				t.Errorf("requests = %v, want %v", tt.mb.seen(), tt.wantRequests)
			}
		})
	}
}

func TestArtistTags(t *testing.T) {
	mb := &fakeMB{artists: map[string]string{
		"queen-id": `{"tags":[{"name":"Rock","count":9},{"name":"glam rock","count":4},{"name":"british","count":4}],
			"genres":[{"name":"rock","count":9},{"name":"art rock","count":2}]}`,
		"empty-id": `{"tags":[],"genres":[]}`,
	}}
	client := newTestClient(t, mb.handler())

	got, err := client.ArtistTags(context.Background(), "queen-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Genres repeat tags and must not double the votes; order is by votes, then name.
	want := []Tag{{"rock", 9}, {"british", 4}, {"glam rock", 4}, {"art rock", 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tags = %v, want %v", got, want)
	}

	for _, id := range []string{"empty-id", "unknown-id"} {
		got, err := client.ArtistTags(context.Background(), id)
		if err != nil || len(got) != 0 {
			t.Errorf("ArtistTags(%q) = %v, %v; want no tags and no error", id, got, err)
		}
	}

	if want := []string{"artist:queen-id", "artist:empty-id", "artist:unknown-id"}; !reflect.DeepEqual(mb.seen(), want) {
		t.Errorf("requests = %v, want lookups by MBID %v", mb.seen(), want)
	}
}

func TestArtistTagsRequest(t *testing.T) {
	var got *http.Request
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		_, _ = w.Write([]byte(`{"tags":[]}`))
	})

	if _, err := client.ArtistTags(context.Background(), "0383dadf-2a4e-4d10-a46a-e9e041da8eb3"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.URL.Path != "/artist/0383dadf-2a4e-4d10-a46a-e9e041da8eb3" {
		t.Errorf("path = %q, want a lookup by MBID", got.URL.Path)
	}
	if inc := got.URL.Query().Get("inc"); inc != "tags genres" {
		t.Errorf("inc = %q, want %q", inc, "tags genres")
	}
	if got.URL.Query().Has("query") {
		t.Error("artist tags were requested through search, want a lookup")
	}
}

func TestRetry(t *testing.T) {
	ok := searchBody(rec("rec-1", 100, "Queen", "rock:1"))

	tests := []struct {
		name         string
		fail         []int
		wantErr      bool
		wantStatus   int
		wantRequests int
	}{
		{name: "one 503 is retried and succeeds", fail: []int{503}, wantRequests: 2},
		{name: "two 503s give up after one retry", fail: []int{503, 503, 503}, wantErr: true, wantStatus: 503, wantRequests: 2},
		{name: "500 is not retried", fail: []int{500}, wantErr: true, wantStatus: 500, wantRequests: 1},
		{name: "429 is not retried", fail: []int{429}, wantErr: true, wantStatus: 429, wantRequests: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mb := &fakeMB{tagged: ok, fail: tt.fail}
			client := newTestClient(t, mb.handler())

			_, err := client.SearchTrack(context.Background(), "Queen", "Song")

			if got := len(mb.seen()); got != tt.wantRequests {
				t.Errorf("%d requests sent, want %d", got, tt.wantRequests)
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var upstream *UpstreamError
			if !errors.As(err, &upstream) || !errors.Is(err, ErrUpstream) {
				t.Fatalf("error = %v, want an *UpstreamError matching ErrUpstream", err)
			}
			if upstream.Status != tt.wantStatus {
				t.Errorf("status = %d, want %d", upstream.Status, tt.wantStatus)
			}
		})
	}
}

func TestRetryOnNetworkError(t *testing.T) {
	var (
		mu       sync.Mutex
		requests int
	)
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		first := requests == 1
		mu.Unlock()

		if first {
			// Drop the connection without answering.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				if tcp, ok := conn.(*net.TCPConn); ok {
					_ = tcp.SetLinger(0)
				}
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte(searchBody(rec("rec-1", 100, "Queen", "rock:1"))))
	})

	track, err := client.SearchTrack(context.Background(), "Queen", "Song")
	if err != nil {
		t.Fatalf("a dropped connection was not retried: %v", err)
	}
	if track.ID != "rec-1" || requests != 2 {
		t.Errorf("track = %+v after %d requests, want rec-1 after 2", track, requests)
	}
}

func TestRetryAfterIsHonouredButCapped(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		cap        time.Duration
		wantWait   time.Duration
		wantReport time.Duration
	}{
		{name: "short Retry-After is waited out", header: "1", cap: 3 * time.Second, wantWait: time.Second, wantReport: time.Second},
		{name: "long Retry-After is capped", header: "120", cap: 200 * time.Millisecond, wantWait: 200 * time.Millisecond, wantReport: 120 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mb := &fakeMB{fail: []int{503, 503}, retryAfter: tt.header}
			client := newTestClient(t, mb.handler())
			client.maxRetryAfter = tt.cap

			start := time.Now()
			_, err := client.SearchTrack(context.Background(), "Queen", "Song")
			elapsed := time.Since(start)

			if elapsed < tt.wantWait {
				t.Errorf("retry came after %v, want at least %v", elapsed, tt.wantWait)
			}
			if elapsed > tt.wantWait+2*time.Second {
				t.Errorf("retry came after %v, want about %v", elapsed, tt.wantWait)
			}

			var upstream *UpstreamError
			if !errors.As(err, &upstream) {
				t.Fatalf("error = %v, want an *UpstreamError", err)
			}
			if upstream.RetryAfter != tt.wantReport {
				t.Errorf("reported RetryAfter = %v, want the server's own %v", upstream.RetryAfter, tt.wantReport)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		value string
		want  time.Duration
	}{
		{"", 0},
		{"5", 5 * time.Second},
		{" 2 ", 2 * time.Second},
		{"0", 0},
		{"-3", 0},
		{"soon", 0},
		{"Mon, 12 Oct 2026 12:00:10 GMT", 10 * time.Second},
		{"Mon, 12 Oct 2026 11:59:00 GMT", 0},
	}
	for _, tt := range tests {
		if got := parseRetryAfter(tt.value, now); got != tt.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

// TestRateIntervalRunsFromEndOfResponse checks that the pause is measured
// from the moment the previous response finished, not from when its request
// started: with a slow server the two differ by the response time.
func TestRateIntervalRunsFromEndOfResponse(t *testing.T) {
	const (
		interval     = 150 * time.Millisecond
		responseTime = 200 * time.Millisecond
	)

	var (
		mu     sync.Mutex
		starts []time.Time
		ends   []time.Time
	)
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()

		time.Sleep(responseTime)
		_, _ = w.Write([]byte(searchBody(rec("rec-1", 100, "Queen", "rock:1"))))

		mu.Lock()
		ends = append(ends, time.Now())
		mu.Unlock()
	}, WithRateInterval(interval))

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.SearchTrack(context.Background(), "Queen", "Song"); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if len(starts) != 3 {
		t.Fatalf("server saw %d requests, want 3", len(starts))
	}
	for i := 1; i < len(starts); i++ {
		// The handler's "end" is taken just before the client sees the body,
		// so allow a little slack.
		if gap := starts[i].Sub(ends[i-1]); gap < interval-20*time.Millisecond {
			t.Errorf("request %d started %v after the previous response ended, want at least %v", i, gap, interval)
		}
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

		mb := &fakeMB{tagged: searchBody(recordings...)}
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
		mb := &fakeMB{tagged: body}
		track, err := newTestClient(t, mb.handler()).SearchTrack(context.Background(), artist, "Under Pressure")
		if err != nil {
			t.Errorf("artist %q: %v", artist, err)
			continue
		}
		if len(track.Tags) != 1 {
			t.Errorf("artist %q: tags = %v", artist, track.Tags)
		}
	}

	mb := &fakeMB{tagged: body}
	if _, err := newTestClient(t, mb.handler()).SearchTrack(context.Background(), "Freddie Mercury", "Under Pressure"); !errors.Is(err, ErrNotFound) {
		t.Errorf("uncredited artist: error = %v, want ErrNotFound", err)
	}
}

const okSearch = `{"recordings":[{"id":"x","score":100,"artist-credit":[{"name":"a","artist":{"id":"a-id","name":"a"}}],"tags":[{"name":"rock","count":1}]}]}`

func TestSearchTrackRequest(t *testing.T) {
	var got []*http.Request
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r)
		_, _ = w.Write([]byte(`{"recordings":[]}`))
	})

	_, _ = client.SearchTrack(context.Background(), `AC/DC "live"`, `Back\In Black`)

	if len(got) != 2 {
		t.Fatalf("%d requests sent, want the tagged search and then the plain one", len(got))
	}
	base := `artist:"AC/DC \"live\"" AND recording:"Back\\In Black"`
	wants := []struct{ query, limit string }{
		{base + " AND tag:*", "100"},
		{base, "25"},
	}
	for i, r := range got {
		if r.URL.Path != "/recording/" {
			t.Errorf("request %d: path = %q, want /recording/", i, r.URL.Path)
		}
		if ua := r.Header.Get("User-Agent"); ua != testUserAgent {
			t.Errorf("request %d: User-Agent = %q, want %q", i, ua, testUserAgent)
		}
		if accept := r.Header.Get("Accept"); accept != "application/json" {
			t.Errorf("request %d: Accept = %q, want application/json", i, accept)
		}
		q := r.URL.Query()
		if q.Get("query") != wants[i].query {
			t.Errorf("request %d: query = %q, want %q", i, q.Get("query"), wants[i].query)
		}
		if q.Get("limit") != wants[i].limit || q.Get("fmt") != "json" {
			t.Errorf("request %d: limit = %q fmt = %q, want %s and json", i, q.Get("limit"), q.Get("fmt"), wants[i].limit)
		}
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

	if !errors.Is(err, ErrUpstream) {
		t.Errorf("error = %v, want one matching ErrUpstream", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	// One timed-out attempt plus one timed-out retry.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("request took %v, timeout was not applied", elapsed)
	}
}

func TestSearchTrackContextDeadline(t *testing.T) {
	client := newTestClient(t, slowHandler(t))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.SearchTrack(ctx, "Queen", "Bohemian Rhapsody")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v: an expired request must not be retried", elapsed)
	}
}

func TestRateLimitQueueHonoursContext(t *testing.T) {
	var requests int
	var mu sync.Mutex
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		_, _ = w.Write([]byte(okSearch))
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
	if requests != 1 {
		t.Errorf("server saw %d requests, want 1 (second must not be sent)", requests)
	}

	// The abandoned turn must be handed back, or the limiter is stuck forever.
	select {
	case <-client.limiter.slot:
	default:
		t.Error("limiter slot was not returned after a cancelled wait")
	}
}
