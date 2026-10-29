package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// DefaultBufferSize is how many events may wait for delivery before Publish
// starts rejecting new ones with ErrBufferFull.
const DefaultBufferSize = 10_000

const failureLogEvery = 10 * time.Second

// Redpanda is a Publisher that writes to a Kafka-compatible broker with
// franz-go. Records are keyed by session_id, so a session's events stay in
// order within one partition.
type Redpanda struct {
	client *kgo.Client
	topic  string
	logger *slog.Logger

	// inflight counts events handed to the client whose delivery has not
	// finished yet; Publish refuses new ones above limit. franz-go's own
	// limit is set higher: TryProduce reports ErrMaxBuffered only through the
	// promise, asynchronously, too late to answer the HTTP request with 429.
	inflight atomic.Int64
	limit    int64

	// lastFailureLog throttles delivery-failure logs during an outage to
	// one line per failureLogEvery; the metric counts every failure.
	lastFailureLog atomic.Int64

	mu     sync.RWMutex // guards closed against Publish racing Close
	closed bool
}

// NewRedpanda creates the producer. It does not contact the brokers: a
// broker that is down only makes events pile up in the bounded buffer.
func NewRedpanda(brokers []string, topic string, bufferSize int, logger *slog.Logger) (*Redpanda, error) {
	if len(brokers) == 0 {
		return nil, errors.New("events: no brokers")
	}
	if topic == "" {
		topic = DefaultTopic
	}
	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.DefaultProduceTopic(topic),
		kgo.MaxBufferedRecords(2*bufferSize),
		kgo.ProducerLinger(50*time.Millisecond),
		// Give up on a record after a while instead of retrying forever, so a
		// long broker outage shows up as delivery errors.
		kgo.RecordDeliveryTimeout(30*time.Second),
		kgo.AllowAutoTopicCreation(),
	)
	if err != nil {
		return nil, fmt.Errorf("events: kafka client: %w", err)
	}
	return &Redpanda{client: client, topic: topic, logger: logger, limit: int64(bufferSize)}, nil
}

// Publish implements Publisher.
func (p *Redpanda) Publish(ev Event) error {
	value, err := json.Marshal(ev)
	if err != nil {
		publishErrorsTotal.WithLabelValues(reasonEncode).Inc()
		return fmt.Errorf("events: encode: %w", err)
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		publishErrorsTotal.WithLabelValues(reasonClosed).Inc()
		return ErrClosed
	}

	if p.inflight.Add(1) > p.limit {
		p.inflight.Add(-1)
		publishErrorsTotal.WithLabelValues(reasonBufferFull).Inc()
		return ErrBufferFull
	}
	p.client.TryProduce(context.Background(), &kgo.Record{Key: []byte(ev.SessionID), Value: value},
		func(r *kgo.Record, err error) {
			p.inflight.Add(-1)
			if err != nil {
				publishErrorsTotal.WithLabelValues(reasonDelivery).Inc()
				p.logFailure(r.Topic, err)
				return
			}
			publishedTotal.Inc()
		})
	return nil
}

func (p *Redpanda) logFailure(topic string, err error) {
	now := time.Now().UnixNano()
	last := p.lastFailureLog.Load()
	if now-last < int64(failureLogEvery) || !p.lastFailureLog.CompareAndSwap(last, now) {
		return
	}
	p.logger.Warn("event delivery failed (further failures within 10s are only counted)",
		"topic", topic, "error", err.Error())
}

// Close implements Publisher.
func (p *Redpanda) Close(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()

	err := p.client.Flush(ctx)
	p.client.Close()
	if err != nil {
		return fmt.Errorf("events: flush: %w", err)
	}
	return nil
}
