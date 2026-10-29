// Package events publishes listening events (complete / skip / like) to a
// Kafka-compatible stream (Redpanda) for offline analytics in ClickHouse.
package events

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
)

// DefaultTopic is the topic ClickHouse's Kafka table reads (lab-events/).
const DefaultTopic = "vibe.events.stream"

// Event types accepted by the stream.
const (
	TypeComplete = "complete"
	TypeSkip     = "skip"
	TypeLike     = "like"
)

// Event is one message on the stream. The JSON field names are the columns
// of vibe.events_queue in lab-events/clickhouse-init/01_schema.sql.
type Event struct {
	EventID     string `json:"event_id"`
	TsMs        int64  `json:"ts_ms"`
	UserID      string `json:"user_id"`
	SessionID   string `json:"session_id"`
	TrackID     string `json:"track_id"`
	PrevTrackID string `json:"prev_track_id"` // "" for the first track of a session
	EventType   string `json:"event_type"`
	ListenMs    uint32 `json:"listen_ms"`
}

// Publisher sends events asynchronously.
type Publisher interface {
	// Publish queues ev for delivery and returns without waiting for the
	// broker. ErrBufferFull means the queue is full and ev was dropped;
	// ErrClosed means the publisher is shutting down.
	Publish(ev Event) error
	// Close stops accepting events and waits, until ctx is done, for the
	// queued ones to be delivered.
	Close(ctx context.Context) error
}

var (
	// ErrBufferFull is returned by Publish when too many events are waiting
	// to be delivered, e.g. because the broker is down.
	ErrBufferFull = errors.New("events: publish buffer is full")
	// ErrClosed is returned by Publish after Close.
	ErrClosed = errors.New("events: publisher is closed")
)

// NewUUID returns a random (version 4) UUID string.
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails on supported platforms
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
