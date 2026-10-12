package cache

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"song-similarity/internal/provider"
)

const artistKeyPrefix = "artist:v1:"

// ArtistTags wraps a provider.ArtistTagger with a read-through cache keyed by
// artist MBID. It degrades and coalesces concurrent misses exactly like
// Searcher. Artist tags are shared by all of an artist's songs and change
// slowly, so they are kept for Config.ArtistTTL.
type ArtistTags struct {
	core
	next    provider.ArtistTagger
	flights flightGroup[[]provider.Tag]
}

// NewArtistTags wraps next with a cache backed by store.
func NewArtistTags(next provider.ArtistTagger, store Store, cfg Config, logger *slog.Logger) *ArtistTags {
	a := &ArtistTags{next: next}
	a.core.init(kindArtist, store, cfg, logger)
	a.flights.clone = func(tags []provider.Tag) []provider.Tag { return append([]provider.Tag(nil), tags...) }
	a.flights.joined = a.countJoins()
	a.flights.onPanic = a.logPanic
	return a
}

type artistEntry struct {
	Tags []entryTag `json:"tags"`
}

// ArtistKey builds the cache key for an artist's tags.
func ArtistKey(artistID string) string {
	return artistKeyPrefix + strings.ToLower(strings.TrimSpace(artistID))
}

// ArtistTags returns the cached tags for the artist when there are any, and
// otherwise asks the wrapped tagger and stores what it returns. Upstream
// failures are never cached.
func (a *ArtistTags) ArtistTags(ctx context.Context, artistID string) ([]provider.Tag, error) {
	key := ArtistKey(artistID)

	if raw, found := a.get(ctx, key); found {
		var e artistEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			a.logger.Warn("cache entry is corrupt, ignoring it", "key", key, "error", err.Error())
		} else {
			hitsTotal.WithLabelValues(kindArtist).Inc()
			return fromEntryTags(e.Tags), nil
		}
	}

	return a.flights.do(ctx, key, func(flightCtx context.Context) ([]provider.Tag, error) {
		tags, err := a.next.ArtistTags(flightCtx, artistID)
		if err != nil {
			return nil, err
		}

		ttl := a.cfg.ArtistTTL
		if len(tags) < LowDataTags {
			ttl = a.cfg.LowDataTTL
		}
		raw, err := json.Marshal(artistEntry{Tags: toEntryTags(tags)})
		if err != nil {
			a.logger.Warn("cache entry could not be encoded", "key", key, "error", err.Error())
			return tags, nil
		}
		a.set(flightCtx, key, raw, ttl)
		return tags, nil
	})
}
