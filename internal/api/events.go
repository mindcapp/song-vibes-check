package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/mindcapp/song-vibes-check/internal/events"
)

const (
	// maxEventBodyBytes caps POST /api/v1/events; a valid event is ~150 bytes.
	maxEventBodyBytes = 4 << 10
	minSessionIDLen   = 8
	maxSessionIDLen   = 64
	// maxListenMs is one day: longer is a client bug, and it must fit UInt32.
	maxListenMs = 24 * 60 * 60 * 1000
)

// WithEvents enables POST /api/v1/events. Without a publisher it answers 503.
func (h *Handlers) WithEvents(p events.Publisher) *Handlers {
	h.events = p
	return h
}

type eventRequest struct {
	SessionID   string `json:"session_id"`
	TrackID     int64  `json:"track_id"`
	PrevTrackID *int64 `json:"prev_track_id"`
	EventType   string `json:"event_type"`
	ListenMs    *int64 `json:"listen_ms"`
}

type eventResponse struct {
	EventID string `json:"event_id"`
	Status  string `json:"status"`
}

var errEventsDisabled = &apiError{
	status:  http.StatusServiceUnavailable,
	message: "event collection is not configured on this server (REDPANDA_BROKERS is not set)",
}

// PostEvent handles POST /api/v1/events: a listening event from the player.
// The server adds event_id and ts_ms and derives an anonymous user_id from
// session_id; the event is then published asynchronously.
//
// Errors: 400 for an invalid body, 429 (with Retry-After) when the publish
// buffer is full, 503 when events are not configured or the server is
// shutting down.
func (h *Handlers) PostEvent(w http.ResponseWriter, r *http.Request) {
	if h.events == nil {
		h.fail(w, r, errEventsDisabled)
		return
	}

	req, apiErr := decodeEvent(w, r)
	if apiErr != nil {
		h.fail(w, r, apiErr)
		return
	}

	ev := events.Event{
		EventID:   events.NewUUID(),
		TsMs:      time.Now().UnixMilli(),
		UserID:    anonymousUserID(req.SessionID),
		SessionID: req.SessionID,
		TrackID:   strconv.FormatInt(req.TrackID, 10),
		EventType: req.EventType,
		ListenMs:  uint32(*req.ListenMs),
	}
	if req.PrevTrackID != nil {
		ev.PrevTrackID = strconv.FormatInt(*req.PrevTrackID, 10)
	}

	addLogAttr(r.Context(), slog.String("event_id", ev.EventID))
	addLogAttr(r.Context(), slog.String("event_type", ev.EventType))
	addLogAttr(r.Context(), slog.String("track_id", ev.TrackID))

	switch err := h.events.Publish(ev); {
	case err == nil:
	case errors.Is(err, events.ErrBufferFull):
		h.fail(w, r, &apiError{
			status:     http.StatusTooManyRequests,
			message:    "event buffer is full, retry later",
			retryAfter: defaultRetryAfter,
			cause:      err,
		})
		return
	case errors.Is(err, events.ErrClosed):
		h.fail(w, r, &apiError{status: http.StatusServiceUnavailable, message: "server is shutting down", cause: err})
		return
	default:
		h.fail(w, r, &apiError{status: http.StatusInternalServerError, message: "could not publish the event", cause: err})
		return
	}

	writeJSON(w, http.StatusAccepted, eventResponse{EventID: ev.EventID, Status: "accepted"})
}

// decodeEvent reads and validates the request body.
func decodeEvent(w http.ResponseWriter, r *http.Request) (*eventRequest, *apiError) {
	bad := func(msg string, cause error) *apiError {
		return &apiError{status: http.StatusBadRequest, message: msg, cause: cause}
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEventBodyBytes))
	dec.DisallowUnknownFields()
	var req eventRequest
	if err := dec.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, &apiError{status: http.StatusRequestEntityTooLarge, message: "request body is too large", cause: err}
		}
		return nil, bad("invalid JSON body", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, bad("request body must be a single JSON object", err)
	}

	if n := len(req.SessionID); n < minSessionIDLen || n > maxSessionIDLen || !isToken(req.SessionID) {
		return nil, bad("session_id must be 8 to 64 characters of letters, digits, '-' or '_'", nil)
	}
	if req.TrackID <= 0 {
		return nil, bad("track_id must be a positive integer", nil)
	}
	if req.PrevTrackID != nil && *req.PrevTrackID <= 0 {
		return nil, bad("prev_track_id must be a positive integer when present", nil)
	}
	switch req.EventType {
	case events.TypeComplete, events.TypeSkip, events.TypeLike:
	default:
		return nil, bad(`event_type must be one of "complete", "skip", "like"`, nil)
	}
	if req.ListenMs == nil {
		return nil, bad("listen_ms is required", nil)
	}
	if *req.ListenMs < 0 || *req.ListenMs > maxListenMs {
		return nil, bad("listen_ms must be between 0 and 86400000", nil)
	}
	return &req, nil
}

// isToken reports whether s only has ASCII letters, digits, '-' and '_'.
func isToken(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// anonymousUserID stands in for a user id until JWT auth is wired in: every
// session is its own anonymous user.
func anonymousUserID(sessionID string) string {
	return "anon-" + sessionID
}
