package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mindcapp/song-vibes-check/internal/events"
	"github.com/mindcapp/song-vibes-check/internal/tags"
)

type fakePublisher struct {
	published []events.Event
	err       error
}

func (f *fakePublisher) Publish(ev events.Event) error {
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, ev)
	return nil
}

func (f *fakePublisher) Close(context.Context) error { return nil }

func eventsRouter(t *testing.T, pub events.Publisher) (http.Handler, *strings.Builder) {
	t.Helper()
	normalizer, err := tags.Load("")
	if err != nil {
		t.Fatalf("load tags: %v", err)
	}
	var logs strings.Builder
	h := NewHandlers(&fakeSearcher{}, normalizer, testTimeout)
	if pub != nil {
		h = h.WithEvents(pub)
	}
	return NewRouter(h, slog.New(slog.NewJSONHandler(&logs, nil))), &logs
}

func postEvent(router http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body)))
	return rec
}

const validEvent = `{"session_id":"sess-12345","track_id":10,"prev_track_id":2,"event_type":"skip","listen_ms":4200}`

func TestPostEventAccepted(t *testing.T) {
	pub := &fakePublisher{}
	router, logs := eventsRouter(t, pub)

	before := time.Now().UnixMilli()
	rec := postEvent(router, validEvent)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var body eventResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(pub.published) != 1 {
		t.Fatalf("published %d events, want 1", len(pub.published))
	}
	ev := pub.published[0]
	if body.EventID != ev.EventID || body.Status != "accepted" {
		t.Errorf("response = %+v, published event_id %q", body, ev.EventID)
	}
	if !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(ev.EventID) {
		t.Errorf("event_id = %q, want a UUID", ev.EventID)
	}
	if ev.TsMs < before || ev.TsMs > time.Now().UnixMilli() {
		t.Errorf("ts_ms = %d, want the server time", ev.TsMs)
	}
	want := events.Event{
		EventID: ev.EventID, TsMs: ev.TsMs,
		UserID: "anon-sess-12345", SessionID: "sess-12345",
		TrackID: "10", PrevTrackID: "2", EventType: "skip", ListenMs: 4200,
	}
	if ev != want {
		t.Errorf("event = %+v, want %+v", ev, want)
	}
	for _, s := range []string{`"path":"/api/v1/events"`, `"status":202`, `"event_type":"skip"`} {
		if !strings.Contains(logs.String(), s) {
			t.Errorf("access log is missing %s:\n%s", s, logs)
		}
	}
}

func TestPostEventFirstTrackHasEmptyPrev(t *testing.T) {
	pub := &fakePublisher{}
	router, _ := eventsRouter(t, pub)
	rec := postEvent(router, `{"session_id":"sess-12345","track_id":10,"event_type":"complete","listen_ms":0}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got := pub.published[0].PrevTrackID; got != "" {
		t.Errorf("prev_track_id = %q, want empty", got)
	}
}

func TestPostEventValidation(t *testing.T) {
	pub := &fakePublisher{}
	router, _ := eventsRouter(t, pub)

	for _, tt := range []struct{ name, body, wantInError string }{
		{"not JSON", `nope`, "invalid JSON"},
		{"two objects", validEvent + validEvent, "single JSON object"},
		{"unknown field", `{"session_id":"sess-12345","track_id":10,"event_type":"skip","listen_ms":1,"user_id":"x"}`, "invalid JSON"},
		{"missing session", `{"track_id":10,"event_type":"skip","listen_ms":1}`, "session_id"},
		{"short session", `{"session_id":"abc","track_id":10,"event_type":"skip","listen_ms":1}`, "session_id"},
		{"long session", `{"session_id":"` + strings.Repeat("a", 65) + `","track_id":10,"event_type":"skip","listen_ms":1}`, "session_id"},
		{"session with spaces", `{"session_id":"sess 12345","track_id":10,"event_type":"skip","listen_ms":1}`, "session_id"},
		{"missing track", `{"session_id":"sess-12345","event_type":"skip","listen_ms":1}`, "track_id"},
		{"track as string", `{"session_id":"sess-12345","track_id":"10","event_type":"skip","listen_ms":1}`, "invalid JSON"},
		{"negative track", `{"session_id":"sess-12345","track_id":-1,"event_type":"skip","listen_ms":1}`, "track_id"},
		{"fractional track", `{"session_id":"sess-12345","track_id":1.5,"event_type":"skip","listen_ms":1}`, "invalid JSON"},
		{"zero prev", `{"session_id":"sess-12345","track_id":10,"prev_track_id":0,"event_type":"skip","listen_ms":1}`, "prev_track_id"},
		{"bad type", `{"session_id":"sess-12345","track_id":10,"event_type":"pause","listen_ms":1}`, "event_type"},
		{"missing listen", `{"session_id":"sess-12345","track_id":10,"event_type":"skip"}`, "listen_ms is required"},
		{"negative listen", `{"session_id":"sess-12345","track_id":10,"event_type":"skip","listen_ms":-5}`, "listen_ms"},
		{"huge listen", `{"session_id":"sess-12345","track_id":10,"event_type":"skip","listen_ms":86400001}`, "listen_ms"},
	} {
		rec := postEvent(router, tt.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", tt.name, rec.Code, rec.Body)
			continue
		}
		if msg := errorBody(t, rec); !strings.Contains(msg, tt.wantInError) {
			t.Errorf("%s: error = %q, want it to contain %q", tt.name, msg, tt.wantInError)
		}
	}
	if len(pub.published) != 0 {
		t.Errorf("invalid events were published: %+v", pub.published)
	}
}

func TestPostEventBodyTooLarge(t *testing.T) {
	router, _ := eventsRouter(t, &fakePublisher{})
	body := `{"session_id":"sess-12345","track_id":10,"event_type":"skip","listen_ms":1,"x":"` +
		strings.Repeat("a", maxEventBodyBytes) + `"}`
	if rec := postEvent(router, body); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestPostEventPublisherErrors(t *testing.T) {
	for _, tt := range []struct {
		name           string
		err            error
		wantStatus     int
		wantRetryAfter string
	}{
		{"buffer full is 429", events.ErrBufferFull, http.StatusTooManyRequests, "1"},
		{"closed is 503", events.ErrClosed, http.StatusServiceUnavailable, ""},
		{"other error is 500", errors.New("boom"), http.StatusInternalServerError, ""},
	} {
		router, _ := eventsRouter(t, &fakePublisher{err: tt.err})
		rec := postEvent(router, validEvent)
		if rec.Code != tt.wantStatus {
			t.Errorf("%s: status = %d, want %d", tt.name, rec.Code, tt.wantStatus)
		}
		if got := rec.Header().Get("Retry-After"); got != tt.wantRetryAfter {
			t.Errorf("%s: Retry-After = %q, want %q", tt.name, got, tt.wantRetryAfter)
		}
		if msg := errorBody(t, rec); strings.Contains(msg, "boom") {
			t.Errorf("%s: error leaks the cause: %q", tt.name, msg)
		}
	}
}

func TestPostEventWithoutPublisherIs503(t *testing.T) {
	router, _ := eventsRouter(t, nil)
	rec := postEvent(router, validEvent)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if msg := errorBody(t, rec); !strings.Contains(msg, "REDPANDA_BROKERS") {
		t.Errorf("error = %q, want it to mention REDPANDA_BROKERS", msg)
	}
	if rec := get(router, "/health"); rec.Code != http.StatusOK {
		t.Errorf("/health = %d without a publisher, want 200", rec.Code)
	}
}

func TestPostEventOnlyAcceptsPost(t *testing.T) {
	router, _ := eventsRouter(t, &fakePublisher{})
	if rec := get(router, "/api/v1/events"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
	if got := metricPath("/api/v1/events"); got != "/api/v1/events" {
		t.Errorf("metricPath = %q, want the path itself", got)
	}
}
