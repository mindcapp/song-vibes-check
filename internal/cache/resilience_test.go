package cache

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"song-similarity/internal/provider"
)

var errStoreDown = errors.New("dial tcp 10.0.0.1:6379: connect: connection refused")

// brokenStore fails every call, like an unreachable Redis.
type brokenStore struct {
	getErr, setErr error
	gets, sets     atomic.Int32
}

func (b *brokenStore) Get(context.Context, string) ([]byte, bool, error) {
	b.gets.Add(1)
	return nil, false, b.getErr
}

func (b *brokenStore) Set(context.Context, string, []byte, time.Duration) error {
	b.sets.Add(1)
	return b.setErr
}

// hangingStore blocks until the context it was given ends.
type hangingStore struct{}

func (hangingStore) Get(ctx context.Context, _ string) ([]byte, bool, error) {
	<-ctx.Done()
	return nil, false, ctx.Err()
}

func (hangingStore) Set(ctx context.Context, _ string, _ []byte, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func TestResultComesFromUpstreamWhenStoreErrors(t *testing.T) {
	tests := []struct {
		name           string
		getErr, setErr error
	}{
		{name: "get and set fail", getErr: errStoreDown, setErr: errStoreDown},
		{name: "only get fails", getErr: errStoreDown},
		{name: "only set fails", setErr: errStoreDown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := &countingSearcher{track: taggedTrack()}
			store := &brokenStore{getErr: tt.getErr, setErr: tt.setErr}

			s := NewSearcher(next, store, testConfig, quietLogger())
			clock := time.Unix(1_700_000_000, 0)
			s.now = func() time.Time { return clock }

			const lookups = 3
			for i := 0; i < lookups; i++ {
				// Step past the cooldown so the store is retried on every
				// lookup and each one really goes through the failing calls.
				clock = clock.Add(testConfig.Cooldown + time.Second)

				got, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody")
				if err != nil {
					t.Fatalf("lookup %d: got error %v, want the upstream result", i, err)
				}
				if !reflect.DeepEqual(got, taggedTrack()) {
					t.Fatalf("lookup %d: track = %+v, want the upstream track %+v", i, got, taggedTrack())
				}
			}

			if next.calls != lookups {
				t.Errorf("upstream called %d times, want %d", next.calls, lookups)
			}
			if gets := store.gets.Load(); gets != lookups {
				t.Errorf("store.Get attempted %d times, want %d", gets, lookups)
			}
		})
	}
}

func TestUpstreamErrorPassesThroughWhenStoreErrors(t *testing.T) {
	for _, upstreamErr := range []error{provider.ErrNotFound, provider.ErrUpstream} {
		next := &countingSearcher{err: upstreamErr}
		store := &brokenStore{getErr: errStoreDown, setErr: errStoreDown}
		s := NewSearcher(next, store, testConfig, quietLogger())

		if _, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody"); !errors.Is(err, upstreamErr) {
			t.Errorf("error = %v, want upstream error %v unchanged", err, upstreamErr)
		}
	}
}

func TestStoreCallsAreBoundedByOpTimeout(t *testing.T) {
	next := &countingSearcher{track: taggedTrack()}
	cfg := Config{TTL: time.Hour, NegativeTTL: time.Minute, OpTimeout: 50 * time.Millisecond}
	s := NewSearcher(next, hangingStore{}, cfg, quietLogger())

	start := time.Now()
	got, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody")
	elapsed := time.Since(start)

	if err != nil || got == nil {
		t.Fatalf("lookup failed with a hanging store: track=%v err=%v", got, err)
	}
	// One timed-out Get; the Set is skipped because the cache is bypassed.
	if elapsed > 500*time.Millisecond {
		t.Errorf("lookup took %v, want about one OpTimeout (50ms)", elapsed)
	}
	if !s.bypassed() {
		t.Error("a timed-out store call did not put the cache into bypass")
	}
}

// TestBypassStateIsRaceFree hammers the bypass state from many goroutines
// while the store flips between failing and healthy. It asserts little by
// itself; its value is under `go test -race`.
func TestBypassStateIsRaceFree(t *testing.T) {
	store := &flakyStore{mem: newMemStore()}
	next := &fixedSearcher{track: taggedTrack()}
	cfg := Config{TTL: time.Hour, NegativeTTL: time.Minute, Cooldown: time.Microsecond}
	s := NewSearcher(next, store, cfg, quietLogger())

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				title := []string{"One", "Two", "Three", "Four"}[(g+i)%4]
				track, err := s.SearchTrack(context.Background(), "Queen", title)
				if err != nil || track == nil {
					t.Errorf("lookup failed: track=%v err=%v", track, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

type fixedSearcher struct{ track *provider.Track }

func (f *fixedSearcher) SearchTrack(context.Context, string, string) (*provider.Track, error) {
	return f.track, nil
}

// flakyStore fails every third call.
type flakyStore struct {
	mem *memStore
	n   atomic.Int64
}

func (f *flakyStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if f.n.Add(1)%3 == 0 {
		return nil, false, errStoreDown
	}
	return f.mem.Get(ctx, key)
}

func (f *flakyStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if f.n.Add(1)%3 == 0 {
		return errStoreDown
	}
	return f.mem.Set(ctx, key, value, ttl)
}
