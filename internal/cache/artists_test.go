package cache

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"song-similarity/internal/provider"
)

type countingTagger struct {
	tags  []provider.Tag
	err   error
	calls atomic.Int32
}

func (c *countingTagger) ArtistTags(context.Context, string) ([]provider.Tag, error) {
	c.calls.Add(1)
	return c.tags, c.err
}

func queenTags() []provider.Tag {
	return []provider.Tag{{Name: "rock", Count: 9}, {Name: "glam rock", Count: 4}, {Name: "art rock", Count: 2}}
}

var artistTestConfig = Config{TTL: 24 * time.Hour, LowDataTTL: time.Hour, ArtistTTL: 72 * time.Hour}

func TestArtistTagsAreCachedByMBID(t *testing.T) {
	next := &countingTagger{tags: queenTags()}
	store := newMemStore()
	a := NewArtistTags(next, store, artistTestConfig, quietLogger())
	hitsBefore := testutil.ToFloat64(hitsTotal.WithLabelValues(kindArtist))
	trackHitsBefore := testutil.ToFloat64(hitsTotal.WithLabelValues(kindTrack))

	first, err := a.ArtistTags(context.Background(), "0383DADF-2a4e-4d10-a46a-e9e041da8eb3")
	if err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	first[0].Name = "scribbled" // callers own their copy

	second, err := a.ArtistTags(context.Background(), " 0383dadf-2a4e-4d10-a46a-e9e041da8eb3 ")
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}

	if !reflect.DeepEqual(second, queenTags()) {
		t.Errorf("cached tags = %v, want %v", second, queenTags())
	}
	if calls := next.calls.Load(); calls != 1 {
		t.Errorf("upstream called %d times, want 1", calls)
	}

	key := ArtistKey("0383dadf-2a4e-4d10-a46a-e9e041da8eb3")
	if key != "artist:v1:0383dadf-2a4e-4d10-a46a-e9e041da8eb3" {
		t.Errorf("key = %q", key)
	}
	if ttl := store.ttls[key]; ttl != artistTestConfig.ArtistTTL {
		t.Errorf("stored with TTL %v, want the artist TTL %v", ttl, artistTestConfig.ArtistTTL)
	}
	if got := testutil.ToFloat64(hitsTotal.WithLabelValues(kindArtist)) - hitsBefore; got != 1 {
		t.Errorf("artist cache hits grew by %v, want 1", got)
	}
	if got := testutil.ToFloat64(hitsTotal.WithLabelValues(kindTrack)) - trackHitsBefore; got != 0 {
		t.Errorf("track cache hits grew by %v, want 0", got)
	}
}

func TestThinlyTaggedArtistIsCachedBriefly(t *testing.T) {
	for _, tags := range [][]provider.Tag{nil, {{Name: "rock", Count: 1}}} {
		store := newMemStore()
		a := NewArtistTags(&countingTagger{tags: tags}, store, artistTestConfig, quietLogger())

		if _, err := a.ArtistTags(context.Background(), "artist-1"); err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if ttl := store.ttls[ArtistKey("artist-1")]; ttl != artistTestConfig.LowDataTTL {
			t.Errorf("%d tags: stored with TTL %v, want %v", len(tags), ttl, artistTestConfig.LowDataTTL)
		}
	}
}

func TestArtistTagsErrorsAreNotCached(t *testing.T) {
	next := &countingTagger{err: provider.ErrUpstream}
	store := newMemStore()
	a := NewArtistTags(next, store, artistTestConfig, quietLogger())

	for i := 0; i < 2; i++ {
		if _, err := a.ArtistTags(context.Background(), "artist-1"); !errors.Is(err, provider.ErrUpstream) {
			t.Fatalf("lookup %d: error = %v, want ErrUpstream", i, err)
		}
	}
	if calls := next.calls.Load(); calls != 2 || store.sets != 0 {
		t.Errorf("upstream calls = %d, sets = %d; want 2 and 0", calls, store.sets)
	}
}

func TestArtistTagsDegradeWhenStoreFails(t *testing.T) {
	next := &countingTagger{tags: queenTags()}
	a := NewArtistTags(next, &brokenStore{getErr: errStoreDown, setErr: errStoreDown}, artistTestConfig, quietLogger())

	for i := 0; i < 3; i++ {
		got, err := a.ArtistTags(context.Background(), "artist-1")
		if err != nil || !reflect.DeepEqual(got, queenTags()) {
			t.Fatalf("lookup %d: tags=%v err=%v, want the upstream tags", i, got, err)
		}
	}
	if calls := next.calls.Load(); calls != 3 {
		t.Errorf("upstream called %d times, want 3", calls)
	}
}

func TestTrackAndArtistKeysDoNotCollide(t *testing.T) {
	if Key("artist-1", "") == ArtistKey("artist-1") {
		t.Error("track and artist keys collide")
	}
}

// TestCounterSeriesExistBeforeFirstUse checks that building a cache creates
// all of its counter series at zero. Dashboards divide these counters by one
// another, and a ratio is empty for as long as any term has no series.
func TestCounterSeriesExistBeforeFirstUse(t *testing.T) {
	NewSearcher(&countingSearcher{}, newMemStore(), testConfig, quietLogger())
	NewArtistTags(&countingTagger{}, newMemStore(), artistTestConfig, quietLogger())

	for name, counter := range map[string]*prometheus.CounterVec{
		"cache_hits_total": hitsTotal, "cache_misses_total": missesTotal,
		"cache_coalesced_total": coalescedTotal, "cache_errors_total": errorsTotal,
	} {
		// Both caches, so two series per counter.
		if got := testutil.CollectAndCount(counter); got != 2 {
			t.Errorf("%s has %d series, want 2 (track and artist)", name, got)
		}
	}
}
