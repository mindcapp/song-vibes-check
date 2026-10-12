package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mindcapp/song-vibes-check/internal/provider"
	"github.com/mindcapp/song-vibes-check/internal/tags"
)

// routerWithLogs builds a router whose log output can be inspected.
func routerWithLogs(t *testing.T, searcher TrackSearcher, timeout time.Duration) (http.Handler, *strings.Builder) {
	t.Helper()
	normalizer, err := tags.Load("")
	if err != nil {
		t.Fatalf("load tags: %v", err)
	}
	var logs strings.Builder
	return NewRouter(NewHandlers(searcher, normalizer, timeout), slog.New(slog.NewJSONHandler(&logs, nil))), &logs
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error response is not JSON: %q", rec.Body)
	}
	if body.Error == "" {
		t.Fatalf("error response has no \"error\" field: %q", rec.Body)
	}
	return body.Error
}

func TestCompareErrorStatuses(t *testing.T) {
	good := track(provider.Tag{Name: "rock", Count: 1})

	tests := []struct {
		name           string
		a, b           *provider.Track
		errB           error
		wantStatus     int
		wantRetryAfter string
		wantInBody     string
		wantNotInBody  string
		wantSong       string
	}{
		{
			name:       "song not found is 404",
			a:          good,
			errB:       provider.ErrNotFound,
			wantStatus: http.StatusNotFound,
			wantInBody: `track not found: "Two" by "B"`,
			wantSong:   "song_b",
		},
		{
			name:       "song found without tags is 422",
			a:          good,
			b:          &provider.Track{ID: "rec-1"},
			wantStatus: http.StatusUnprocessableEntity,
			wantInBody: `"Two" by "B" was found but has no usable genre tags`,
			wantSong:   "song_b",
		},
		{
			name:       "song whose tags are all stop-listed is 422",
			a:          good,
			b:          track(provider.Tag{Name: "seen live", Count: 9}, provider.Tag{Name: "1994", Count: 2}),
			wantStatus: http.StatusUnprocessableEntity,
			wantSong:   "song_b",
		},
		{
			name:           "MusicBrainz 503 is 502 with a default Retry-After",
			a:              good,
			errB:           &provider.UpstreamError{Status: 503},
			wantStatus:     http.StatusBadGateway,
			wantRetryAfter: "1",
			wantInBody:     "MusicBrainz is unavailable",
			wantNotInBody:  "status 503",
			wantSong:       "song_b",
		},
		{
			name:           "Retry-After from MusicBrainz is passed on",
			a:              good,
			errB:           &provider.UpstreamError{Status: 503, RetryAfter: 2500 * time.Millisecond},
			wantStatus:     http.StatusBadGateway,
			wantRetryAfter: "3",
			wantSong:       "song_b",
		},
		{
			name:           "an absurd Retry-After is capped",
			a:              good,
			errB:           &provider.UpstreamError{Status: 503, RetryAfter: 6 * time.Hour},
			wantStatus:     http.StatusBadGateway,
			wantRetryAfter: "60",
			wantSong:       "song_b",
		},
		{
			name:           "network failure is 502",
			a:              good,
			errB:           &provider.UpstreamError{Err: errors.New("dial tcp: connection refused")},
			wantStatus:     http.StatusBadGateway,
			wantRetryAfter: "1",
			wantNotInBody:  "connection refused",
			wantSong:       "song_b",
		},
		{
			name:           "unreadable MusicBrainz response is 502",
			a:              good,
			errB:           errors.New("musicbrainz: decode response: unexpected EOF"),
			wantStatus:     http.StatusBadGateway,
			wantRetryAfter: "1",
			wantSong:       "song_b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			searcher := &fakeSearcher{tracks: map[string]*provider.Track{"A/One": tt.a}}
			if tt.b != nil {
				searcher.tracks["B/Two"] = tt.b
			}
			if tt.errB != nil {
				searcher.errs = map[string]error{"B/Two": tt.errB}
			}
			router, logs := routerWithLogs(t, searcher, testTimeout)

			rec := postCompare(t, router, compareBody)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body)
			}
			if got := rec.Header().Get("Retry-After"); got != tt.wantRetryAfter {
				t.Errorf("Retry-After = %q, want %q", got, tt.wantRetryAfter)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			body := errorBody(t, rec)
			if tt.wantInBody != "" && !strings.Contains(body, tt.wantInBody) {
				t.Errorf("error = %q, want it to contain %q", body, tt.wantInBody)
			}
			if tt.wantNotInBody != "" && strings.Contains(body, tt.wantNotInBody) {
				t.Errorf("error = %q leaks upstream detail %q", body, tt.wantNotInBody)
			}

			var line map[string]any
			if err := json.Unmarshal([]byte(logs.String()), &line); err != nil {
				t.Fatalf("decode log line %q: %v", logs.String(), err)
			}
			if line["failed_song"] != tt.wantSong {
				t.Errorf("logged failed_song = %v, want %v", line["failed_song"], tt.wantSong)
			}
			if tt.wantNotInBody != "" && !strings.Contains(fmt.Sprint(line["error"]), tt.wantNotInBody) {
				t.Errorf("log error = %v, want the upstream detail %q kept for operators", line["error"], tt.wantNotInBody)
			}
		})
	}
}

func TestCompareBadRequestBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "not JSON", body: `hello`},
		{name: "wrong type", body: `{"song_a":"Queen","song_b":{"artist":"B","title":"Two"}}`},
		{name: "whitespace-only artist", body: `{"song_a":{"artist":"   ","title":"One"},"song_b":{"artist":"B","title":"Two"}}`},
		{name: "whitespace-only title", body: `{"song_a":{"artist":"A","title":"One"},"song_b":{"artist":"B","title":"\t"}}`},
		{name: "empty body", body: ``},
		{name: "oversized body", body: `{"song_a":{"artist":"` + strings.Repeat("a", maxBodyBytes) + `","title":"One"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			searcher := &fakeSearcher{}
			router, _ := routerWithLogs(t, searcher, testTimeout)

			rec := postCompare(t, router, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			errorBody(t, rec)
			if len(searcher.calls) != 0 {
				t.Errorf("searcher was called %v for an invalid request", searcher.calls)
			}
		})
	}
}

func TestCompareTrimsFields(t *testing.T) {
	searcher := &fakeSearcher{tracks: map[string]*provider.Track{
		"A/One": track(provider.Tag{Name: "rock", Count: 1}),
		"B/Two": track(provider.Tag{Name: "rock", Count: 1}),
	}}
	router, _ := routerWithLogs(t, searcher, testTimeout)

	rec := postCompare(t, router, `{"song_a":{"artist":" A ","title":"One\n"},"song_b":{"artist":"B","title":"  Two"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

// blockingSearcher never answers; it returns when the request's context ends.
type blockingSearcher struct{}

func (blockingSearcher) SearchTrack(ctx context.Context, _, _ string) (*provider.Track, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCompareTimesOutWith502(t *testing.T) {
	router, logs := routerWithLogs(t, blockingSearcher{}, 50*time.Millisecond)

	start := time.Now()
	rec := postCompare(t, router, compareBody)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", rec.Code, rec.Body)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("request took %v, want about the 50ms timeout", elapsed)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	if body := errorBody(t, rec); !strings.Contains(body, "did not answer in time") {
		t.Errorf("error = %q", body)
	}
	if !strings.Contains(logs.String(), "deadline exceeded") {
		t.Errorf("timeout cause not logged: %s", logs)
	}
}

func TestCompareClientGoneIsLoggedAs499(t *testing.T) {
	router, logs := routerWithLogs(t, blockingSearcher{}, testTimeout)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/compare", strings.NewReader(compareBody)).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		router.ServeHTTP(rec, req)
		close(done)
	}()
	cancel()
	<-done

	if rec.Code != statusClientClosedRequest {
		t.Errorf("status = %d, want %d", rec.Code, statusClientClosedRequest)
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Error("Retry-After set for a request the client abandoned")
	}
	if !strings.Contains(logs.String(), `"status":499`) {
		t.Errorf("access log does not show 499: %s", logs)
	}
}

type panickingSearcher struct{}

func (panickingSearcher) SearchTrack(context.Context, string, string) (*provider.Track, error) {
	panic("boom")
}

func TestPanicBecomesLogged500(t *testing.T) {
	router, logs := routerWithLogs(t, panickingSearcher{}, testTimeout)

	rec := postCompare(t, router, compareBody)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := errorBody(t, rec); body != "internal error" {
		t.Errorf("error = %q, want a generic message", body)
	}
	out := logs.String()
	for _, want := range []string{`"level":"ERROR"`, `"msg":"handler panicked"`, `"panic":"boom"`, `"stack":"goroutine `, `"status":500`} {
		if !strings.Contains(out, want) {
			t.Errorf("logs are missing %s:\n%s", want, out)
		}
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	router, _ := routerWithLogs(t, &fakeSearcher{}, testTimeout)

	for _, tt := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/compare", http.StatusMethodNotAllowed},
		{http.MethodGet, "/nope", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
		if rec.Code != tt.want {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.path, rec.Code, tt.want)
		}
	}
}

func TestMetricPathIsBounded(t *testing.T) {
	for path, want := range map[string]string{
		"/compare":         "/compare",
		"/health":          "/health",
		"/metrics":         "/metrics",
		"/":                "other",
		"/wp-login.php":    "other",
		"/compare/":        "other",
		"/.env":            "other",
		"/compare?x=1":     "other",
		"/a/very/long/url": "other",
	} {
		if got := metricPath(path); got != want {
			t.Errorf("metricPath(%q) = %q, want %q", path, got, want)
		}
	}
}
