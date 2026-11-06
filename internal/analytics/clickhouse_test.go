package analytics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T, h http.HandlerFunc, opts ...Option) *ClickHouse {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	store, err := NewClickHouse(srv.URL+"/", opts...)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestNewClickHouseValidates(t *testing.T) {
	for _, bad := range []string{"", "127.0.0.1:8123", "ftp://host", "http://"} {
		if _, err := NewClickHouse(bad); err == nil {
			t.Errorf("NewClickHouse(%q) accepted an invalid URL", bad)
		}
	}
	if _, err := NewClickHouse("http://127.0.0.1:8123", WithTimeout(0)); err == nil {
		t.Error("NewClickHouse accepted a zero timeout")
	}
}

func TestStatsRequestAndParsing(t *testing.T) {
	var got *http.Request
	store := newTestStore(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		// 64-bit integers come quoted by default, but a bare number must work too.
		_, _ = w.Write([]byte(`{"to_track":"5","total_plays":"31","total_skips":"4"}` + "\n" +
			`{"to_track":"10","total_plays":7,"total_skips":0}` + "\n"))
	}, WithCredentials("vibe", "s3cret"), WithDatabase("analytics"))

	stats, err := store.Stats(context.Background(), 2, []int64{5, 10, 11})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]Stat{5: {Plays: 31, Skips: 4}, 10: {Plays: 7, Skips: 0}}
	if !reflect.DeepEqual(stats, want) {
		t.Errorf("stats = %v, want %v", stats, want)
	}

	if got.Method != http.MethodGet {
		t.Errorf("method = %s, want GET (read-only on the server)", got.Method)
	}
	q := got.URL.Query()
	if q.Get("param_from_track") != "2" || q.Get("param_to_tracks") != "['5','10','11']" {
		t.Errorf("params = %v, want from 2 and to ['5','10','11']", q)
	}
	if q.Get("database") != "analytics" {
		t.Errorf("database = %q, want analytics", q.Get("database"))
	}
	sql := q.Get("query")
	for _, part := range []string{"{from_track:String}", "{to_tracks:Array(String)}", "GROUP BY to_track", "sum(plays)", "sum(skips)"} {
		if !strings.Contains(sql, part) {
			t.Errorf("query is missing %q:\n%s", part, sql)
		}
	}
	// ids are bound, never spliced into the SQL text
	if strings.Contains(sql, "'5'") || strings.Contains(sql, " 2 ") {
		t.Errorf("query contains literal ids:\n%s", sql)
	}
	if got.Header.Get("X-ClickHouse-User") != "vibe" || got.Header.Get("X-ClickHouse-Key") != "s3cret" {
		t.Errorf("credentials headers = %v", got.Header)
	}
	if strings.Contains(got.URL.String(), "s3cret") {
		t.Error("the password is in the URL")
	}
}

func TestStatsEmptyTargetsAndLimit(t *testing.T) {
	store := newTestStore(t, func(http.ResponseWriter, *http.Request) {
		t.Error("ClickHouse was called")
	})
	stats, err := store.Stats(context.Background(), 2, nil)
	if err != nil || len(stats) != 0 {
		t.Errorf("empty targets = %v, %v; want an empty map", stats, err)
	}
	if _, err := store.Stats(context.Background(), 2, make([]int64, maxTargets+1)); err == nil {
		t.Error("too many targets were accepted")
	}
}

func TestStatsNoRowsIsEmptyNotError(t *testing.T) {
	store := newTestStore(t, func(http.ResponseWriter, *http.Request) {})
	stats, err := store.Stats(context.Background(), 2, []int64{5})
	if err != nil || stats == nil || len(stats) != 0 {
		t.Errorf("no rows = %v, %v; want an empty map", stats, err)
	}
}

func TestStatsErrors(t *testing.T) {
	for _, tt := range []struct {
		name       string
		handler    http.HandlerFunc
		wantReason string
		wantStatus int
	}{
		{"server error", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Code: 60. DB::Exception: Table vibe.transitions doesn't exist", http.StatusNotFound)
		}, "status_4xx", http.StatusNotFound},
		{"unavailable", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}, "status_5xx", http.StatusServiceUnavailable},
		{"bad row", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json\n"))
		}, "bad_response", 0},
		{"bad track id", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"to_track":"abc","total_plays":"1","total_skips":"0"}` + "\n"))
		}, "bad_response", 0},
	} {
		store := newTestStore(t, tt.handler)
		_, err := store.Stats(context.Background(), 2, []int64{5})
		var upstream *UpstreamError
		if !errors.Is(err, ErrUpstream) || !errors.As(err, &upstream) {
			t.Errorf("%s: err = %v, want an UpstreamError", tt.name, err)
			continue
		}
		if upstream.Status != tt.wantStatus {
			t.Errorf("%s: status = %d, want %d", tt.name, upstream.Status, tt.wantStatus)
		}
		if got := errorReason(err); got != tt.wantReason {
			t.Errorf("%s: reason = %q, want %q", tt.name, got, tt.wantReason)
		}
	}
}

func TestStatsTimeout(t *testing.T) {
	release := make(chan struct{})
	store := newTestStore(t, func(http.ResponseWriter, *http.Request) { <-release },
		WithTimeout(50*time.Millisecond))
	defer close(release)

	start := time.Now()
	_, err := store.Stats(context.Background(), 2, []int64{5})
	if !errors.Is(err, context.DeadlineExceeded) || errorReason(err) != "timeout" {
		t.Errorf("err = %v (reason %q), want a timeout", err, errorReason(err))
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %s, the timeout was not applied", time.Since(start))
	}
}

func TestStatsUnreachableAndCanceled(t *testing.T) {
	store, err := NewClickHouse("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Stats(context.Background(), 2, []int64{5})
	if !errors.Is(err, ErrUpstream) || errorReason(err) != "network" {
		t.Errorf("err = %v (reason %q), want a network error", err, errorReason(err))
	}
	if strings.Contains(err.Error(), "SELECT") || strings.Contains(err.Error(), "param_") {
		t.Errorf("the error repeats the request URL: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.Stats(ctx, 2, []int64{5})
	if errorReason(err) != "canceled" {
		t.Errorf("reason = %q, want canceled", errorReason(err))
	}
}
