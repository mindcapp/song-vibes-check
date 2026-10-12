package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestSearchTrack(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantErr    error
		wantAnyErr bool
		wantTrack  *Track
	}{
		{
			name:   "success merges tags and genres",
			status: http.StatusOK,
			body: `{"recordings":[{
				"id":"rec-1","title":"Bohemian Rhapsody",
				"artist-credit":[{"name":"Queen"}],
				"tags":[{"name":"Rock","count":5},{"name":" glam rock ","count":2}],
				"genres":[{"name":"rock","count":7},{"name":"progressive rock","count":1}]
			}]}`,
			wantTrack: &Track{
				ID:     "rec-1",
				Title:  "Bohemian Rhapsody",
				Artist: "Queen",
				Tags:   []string{"rock", "glam rock", "progressive rock"},
				WeightedTags: []Tag{
					{Name: "rock", Count: 7},
					{Name: "glam rock", Count: 2},
					{Name: "progressive rock", Count: 1},
				},
			},
		},
		{
			name:   "prefers first recording that has tags",
			status: http.StatusOK,
			body: `{"recordings":[
				{"id":"rec-live","title":"Song (live)","artist-credit":[{"name":"Band"}]},
				{"id":"rec-studio","title":"Song","artist-credit":[{"name":"Band"}],"tags":[{"name":"jazz","count":1}]}
			]}`,
			wantTrack: &Track{
				ID: "rec-studio", Title: "Song", Artist: "Band",
				Tags: []string{"jazz"}, WeightedTags: []Tag{{Name: "jazz", Count: 1}},
			},
		},
		{
			name:      "recording without tags",
			status:    http.StatusOK,
			body:      `{"recordings":[{"id":"rec-2","title":"Obscure","artist-credit":[{"name":"Nobody"}],"tags":[]}]}`,
			wantTrack: &Track{ID: "rec-2", Title: "Obscure", Artist: "Nobody", Tags: nil},
		},
		{
			name:   "falls back to requested artist when credit is missing",
			status: http.StatusOK,
			body:   `{"recordings":[{"id":"rec-3","title":"Solo","tags":[{"name":"pop","count":1}]}]}`,
			wantTrack: &Track{
				ID: "rec-3", Title: "Solo", Artist: "Queen",
				Tags: []string{"pop"}, WeightedTags: []Tag{{Name: "pop", Count: 1}},
			},
		},
		{
			name:    "no recordings",
			status:  http.StatusOK,
			body:    `{"recordings":[]}`,
			wantErr: ErrNotFound,
		},
		{
			name:    "service unavailable",
			status:  http.StatusServiceUnavailable,
			body:    `{"error":"rate limited"}`,
			wantErr: ErrUpstream,
		},
		{
			name:       "malformed JSON",
			status:     http.StatusOK,
			body:       `{"recordings":`,
			wantAnyErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, jsonHandler(tt.status, tt.body))

			track, err := client.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody")

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			case tt.wantAnyErr:
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(track, tt.wantTrack) {
				t.Errorf("track = %+v, want %+v", track, tt.wantTrack)
			}
		})
	}
}

func TestSearchTrackRequest(t *testing.T) {
	var got *http.Request
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		jsonHandler(http.StatusOK, `{"recordings":[{"id":"x","tags":[{"name":"rock","count":1}]}]}`)(w, r)
	})

	if _, err := client.SearchTrack(context.Background(), `AC/DC "live"`, `Back\In Black`); err != nil {
		t.Fatalf("unexpected error: %v", err)
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
	for key, want := range map[string]string{"fmt": "json", "inc": "tags genres", "limit": "25"} {
		if q.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, q.Get(key), want)
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
		jsonHandler(http.StatusOK, `{"recordings":[{"id":"x","tags":[{"name":"rock","count":1}]}]}`)(w, r)
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
		jsonHandler(http.StatusOK, `{"recordings":[{"id":"x","tags":[{"name":"rock","count":1}]}]}`)(w, r)
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
