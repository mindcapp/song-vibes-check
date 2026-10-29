package events

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"testing"
	"time"
)

func TestNewUUID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for range 100 {
		id := NewUUID()
		if !re.MatchString(id) {
			t.Fatalf("NewUUID() = %q, not a v4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("NewUUID() repeated %q", id)
		}
		seen[id] = true
	}
}

// With an unreachable broker nothing is ever delivered, so every published
// event stays in flight and the bounded buffer fills up.
func TestRedpandaBufferLimitAndClose(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	p, err := NewRedpanda([]string{"127.0.0.1:1"}, "", 2, logger)
	if err != nil {
		t.Fatal(err)
	}
	ev := Event{EventID: NewUUID(), SessionID: "s1", TrackID: "2", EventType: TypeSkip}

	for i := range 2 {
		if err := p.Publish(ev); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	if err := p.Publish(ev); !errors.Is(err, ErrBufferFull) {
		t.Fatalf("third publish = %v, want ErrBufferFull", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := p.Close(ctx); err == nil {
		t.Error("Close with undeliverable events = nil, want a flush error")
	}
	if err := p.Publish(ev); !errors.Is(err, ErrClosed) {
		t.Errorf("publish after Close = %v, want ErrClosed", err)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}

func TestNewRedpandaNeedsBrokers(t *testing.T) {
	if _, err := NewRedpanda(nil, "", 0, slog.New(slog.NewJSONHandler(io.Discard, nil))); err == nil {
		t.Error("NewRedpanda without brokers succeeded")
	}
}
