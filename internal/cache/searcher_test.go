package cache

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"song-similarity/internal/provider"
)

type memStore struct {
	mu     sync.Mutex
	data   map[string][]byte
	ttls   map[string]time.Duration
	getErr error
	setErr error
	gets   int
	sets   int
}

func newMemStore() *memStore {
	return &memStore{data: map[string][]byte{}, ttls: map[string]time.Duration{}}
}

func (m *memStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	if m.getErr != nil {
		return nil, false, m.getErr
	}
	v, ok := m.data[key]
	return v, ok, nil
}

func (m *memStore) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sets++
	if m.setErr != nil {
		return m.setErr
	}
	m.data[key] = value
	m.ttls[key] = ttl
	return nil
}

type countingSearcher struct {
	track *provider.Track
	err   error
	calls int
}

func (c *countingSearcher) SearchTrack(context.Context, string, string) (*provider.Track, error) {
	c.calls++
	return c.track, c.err
}

func taggedTrack() *provider.Track {
	return &provider.Track{
		ID: "rec-1", Title: "Bohemian Rhapsody", Artist: "Queen", ArtistID: "queen-id",
		Tags:          []string{"rock", "glam rock", "art rock"},
		RecordingTags: []provider.Tag{{Name: "rock", Count: 7}, {Name: "glam rock", Count: 2}, {Name: "art rock", Count: 1}},
	}
}

var testConfig = Config{TTL: 24 * time.Hour, LowDataTTL: time.Hour, NegativeTTL: time.Minute, Cooldown: 30 * time.Second}

func newTestSearcher(next provider.TrackSearcher, store Store) *Searcher {
	return NewSearcher(next, store, testConfig, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

type counters struct{ hits, misses, errs float64 }

func readCounters() counters {
	return counters{
		hits:   testutil.ToFloat64(hitsTotal.WithLabelValues(kindTrack)),
		misses: testutil.ToFloat64(missesTotal.WithLabelValues(kindTrack)),
		errs:   testutil.ToFloat64(errorsTotal.WithLabelValues(kindTrack)),
	}
}

func (c counters) since(before counters) counters {
	return counters{hits: c.hits - before.hits, misses: c.misses - before.misses, errs: c.errs - before.errs}
}

func TestSearcherCachesTrack(t *testing.T) {
	next := &countingSearcher{track: taggedTrack()}
	store := newMemStore()
	s := newTestSearcher(next, store)
	before := readCounters()

	first, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody")
	if err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	if first.Cached {
		t.Error("first lookup reported Cached = true")
	}

	second, err := s.SearchTrack(context.Background(), "  QUEEN ", "bohemian   rhapsody")
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if !second.Cached {
		t.Error("second lookup reported Cached = false")
	}

	if next.calls != 1 {
		t.Errorf("upstream called %d times, want 1", next.calls)
	}

	want := taggedTrack()
	want.Cached = true
	if !reflect.DeepEqual(second, want) {
		t.Errorf("cached track = %+v, want %+v", second, want)
	}

	if ttl := store.ttls[Key("Queen", "Bohemian Rhapsody")]; ttl != testConfig.TTL {
		t.Errorf("stored with TTL %v, want %v", ttl, testConfig.TTL)
	}
	if got := readCounters().since(before); got != (counters{hits: 1, misses: 1}) {
		t.Errorf("counters = %+v, want 1 hit and 1 miss", got)
	}
}

func TestSearcherCachesNotFoundBriefly(t *testing.T) {
	next := &countingSearcher{err: provider.ErrNotFound}
	store := newMemStore()
	s := newTestSearcher(next, store)

	for i := 0; i < 2; i++ {
		if _, err := s.SearchTrack(context.Background(), "Nobody", "Nothing"); !errors.Is(err, provider.ErrNotFound) {
			t.Fatalf("lookup %d: error = %v, want ErrNotFound", i, err)
		}
	}

	if next.calls != 1 {
		t.Errorf("upstream called %d times, want 1", next.calls)
	}
	if ttl := store.ttls[Key("Nobody", "Nothing")]; ttl != testConfig.NegativeTTL {
		t.Errorf("stored with TTL %v, want negative TTL %v", ttl, testConfig.NegativeTTL)
	}
}

func TestSearcherTTLDependsOnTagCount(t *testing.T) {
	withTags := func(n int) *provider.Track {
		track := &provider.Track{ID: "rec", Title: "Song", Artist: "Artist"}
		for i := 0; i < n; i++ {
			name := string(rune('a' + i))
			track.Tags = append(track.Tags, name)
			track.RecordingTags = append(track.RecordingTags, provider.Tag{Name: name, Count: 1})
		}
		return track
	}

	tests := []struct {
		tags int
		want time.Duration
	}{
		{tags: 0, want: testConfig.LowDataTTL},
		{tags: 1, want: testConfig.LowDataTTL},
		{tags: 2, want: testConfig.LowDataTTL},
		{tags: 3, want: testConfig.TTL},
		{tags: 8, want: testConfig.TTL},
	}

	for _, tt := range tests {
		store := newMemStore()
		s := newTestSearcher(&countingSearcher{track: withTags(tt.tags)}, store)

		if _, err := s.SearchTrack(context.Background(), "Artist", "Song"); err != nil {
			t.Fatalf("%d tags: lookup: %v", tt.tags, err)
		}
		if ttl := store.ttls[Key("Artist", "Song")]; ttl != tt.want {
			t.Errorf("%d tags: stored with TTL %v, want %v", tt.tags, ttl, tt.want)
		}
	}
}

func TestSearcherDoesNotCacheUpstreamErrors(t *testing.T) {
	next := &countingSearcher{err: provider.ErrUpstream}
	store := newMemStore()
	s := newTestSearcher(next, store)

	for i := 0; i < 2; i++ {
		if _, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody"); !errors.Is(err, provider.ErrUpstream) {
			t.Fatalf("lookup %d: error = %v, want ErrUpstream", i, err)
		}
	}

	if next.calls != 2 {
		t.Errorf("upstream called %d times, want 2", next.calls)
	}
	if store.sets != 0 {
		t.Errorf("store.Set called %d times, want 0", store.sets)
	}
}

func TestSearcherDegradesWhenStoreFails(t *testing.T) {
	next := &countingSearcher{track: taggedTrack()}
	store := newMemStore()
	store.getErr = errors.New("dial tcp: connection refused")
	store.setErr = store.getErr

	s := newTestSearcher(next, store)
	clock := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return clock }
	before := readCounters()

	for i := 0; i < 3; i++ {
		track, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody")
		if err != nil {
			t.Fatalf("lookup %d failed although only the cache is down: %v", i, err)
		}
		if track.Cached {
			t.Errorf("lookup %d reported Cached = true", i)
		}
	}

	if next.calls != 3 {
		t.Errorf("upstream called %d times, want 3", next.calls)
	}
	if store.gets != 1 || store.sets != 0 {
		t.Errorf("store touched %d gets / %d sets during cooldown, want 1 / 0", store.gets, store.sets)
	}
	if got := readCounters().since(before); got != (counters{misses: 3, errs: 1}) {
		t.Errorf("counters = %+v, want 3 misses and 1 error", got)
	}

	// Redis comes back; once the cooldown is over the cache is used again.
	store.getErr, store.setErr = nil, nil
	clock = clock.Add(testConfig.Cooldown + time.Second)

	if _, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody"); err != nil {
		t.Fatalf("lookup after recovery: %v", err)
	}
	track, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody")
	if err != nil {
		t.Fatalf("second lookup after recovery: %v", err)
	}
	if !track.Cached {
		t.Error("cache was not used after the store recovered")
	}
	if next.calls != 4 {
		t.Errorf("upstream called %d times, want 4", next.calls)
	}
}

func TestSearcherIgnoresCorruptEntry(t *testing.T) {
	next := &countingSearcher{track: taggedTrack()}
	store := newMemStore()
	store.data[Key("Queen", "Bohemian Rhapsody")] = []byte("not json")
	s := newTestSearcher(next, store)

	track, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if track.Cached || next.calls != 1 {
		t.Errorf("corrupt entry was served: cached=%v upstream calls=%d", track.Cached, next.calls)
	}
}

func TestSearcherCancelledRequestDoesNotTripBypass(t *testing.T) {
	next := &countingSearcher{track: taggedTrack()}
	store := newMemStore()
	store.getErr = context.Canceled
	s := newTestSearcher(next, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = s.SearchTrack(ctx, "Queen", "Bohemian Rhapsody")

	if s.bypassed() {
		t.Error("a cancelled request put the cache into bypass")
	}
}

func TestKey(t *testing.T) {
	tests := []struct {
		artist, title string
		want          string
	}{
		{"Queen", "Bohemian Rhapsody", "track:v3:queen|bohemian+rhapsody"},
		{"  QUEEN  ", "bohemian   RHAPSODY ", "track:v3:queen|bohemian+rhapsody"},
		{"AC/DC", "T.N.T.", "track:v3:ac%2Fdc|t.n.t."},
		{"a|b", "c", "track:v3:a%7Cb|c"},
		{"a", "b|c", "track:v3:a|b%7Cc"},
	}

	for _, tt := range tests {
		if got := Key(tt.artist, tt.title); got != tt.want {
			t.Errorf("Key(%q, %q) = %q, want %q", tt.artist, tt.title, got, tt.want)
		}
	}
}
