package cache

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/mindcapp/song-vibes-check/internal/provider"
)

// trackKeyPrefix carries a version that is bumped whenever the meaning of a
// cached track changes. v2: tags merged from several recordings. v3: only
// recording tags and the artist MBID are stored; artist tags live in their
// own cache.
const trackKeyPrefix = "track:v3:"

// Searcher wraps a provider.TrackSearcher with a read-through cache. Store
// failures never fail a lookup: they are logged, counted and the lookup falls
// through to the wrapped searcher. Concurrent misses for the same track share
// a single upstream lookup, and every caller gets its own copy of the track.
// A Searcher is safe for concurrent use.
type Searcher struct {
	core
	next    provider.TrackSearcher
	flights flightGroup[*provider.Track]
}

// NewSearcher wraps next with a cache backed by store.
func NewSearcher(next provider.TrackSearcher, store Store, cfg Config, logger *slog.Logger) *Searcher {
	s := &Searcher{next: next}
	s.core.init(kindTrack, store, cfg, logger)
	s.flights.clone = (*provider.Track).Clone
	s.flights.joined = s.countJoins()
	s.flights.onPanic = s.logPanic
	return s
}

type trackEntry struct {
	Found bool        `json:"found"`
	Track *entryTrack `json:"track,omitempty"`
}

type entryTrack struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Artist   string     `json:"artist"`
	ArtistID string     `json:"artist_id,omitempty"`
	Tags     []entryTag `json:"tags"`
}

type entryTag struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// SearchTrack returns the cached result for (artist, title) when there is
// one, and otherwise asks the wrapped searcher and stores what it returns.
// Upstream failures are never cached.
func (s *Searcher) SearchTrack(ctx context.Context, artist, title string) (*provider.Track, error) {
	key := Key(artist, title)

	if raw, found := s.get(ctx, key); found {
		var e trackEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			s.logger.Warn("cache entry is corrupt, ignoring it", "key", key, "error", err.Error())
		} else {
			hitsTotal.WithLabelValues(kindTrack).Inc()
			if !e.Found || e.Track == nil {
				return nil, provider.ErrNotFound
			}
			return fromEntry(e.Track), nil
		}
	}

	return s.flights.do(ctx, key, func(flightCtx context.Context) (*provider.Track, error) {
		// flightCtx has no deadline on purpose. The upstream client queues
		// behind its rate limiter, so a burst of different tracks can wait
		// many seconds for a slot; a deadline here would fail the tail of
		// that queue. The HTTP call itself is bounded by the client's own
		// timeout, and the lookup is cancelled once nobody waits for it.
		track, err := s.next.SearchTrack(flightCtx, artist, title)
		switch {
		case err == nil:
			ttl := s.cfg.TTL
			if len(track.RecordingTags) < LowDataTags {
				ttl = s.cfg.LowDataTTL
			}
			s.save(flightCtx, key, trackEntry{Found: true, Track: toEntry(track)}, ttl)
		case errors.Is(err, provider.ErrNotFound):
			s.save(flightCtx, key, trackEntry{Found: false}, s.cfg.NegativeTTL)
		}
		return track, err
	})
}

func (s *Searcher) save(ctx context.Context, key string, e trackEntry, ttl time.Duration) {
	raw, err := json.Marshal(e)
	if err != nil {
		s.logger.Warn("cache entry could not be encoded", "key", key, "error", err.Error())
		return
	}
	s.set(ctx, key, raw, ttl)
}

// Key builds the cache key for a track lookup: artist and title are trimmed,
// lower-cased and have inner whitespace collapsed, so "Queen " and "queen"
// share an entry.
func Key(artist, title string) string {
	return trackKeyPrefix + keyPart(artist) + "|" + keyPart(title)
}

func keyPart(s string) string {
	return url.QueryEscape(strings.Join(strings.Fields(strings.ToLower(s)), " "))
}

func toEntry(t *provider.Track) *entryTrack {
	return &entryTrack{ID: t.ID, Title: t.Title, Artist: t.Artist, ArtistID: t.ArtistID, Tags: toEntryTags(t.RecordingTags)}
}

func fromEntry(e *entryTrack) *provider.Track {
	track := &provider.Track{ID: e.ID, Title: e.Title, Artist: e.Artist, ArtistID: e.ArtistID, Cached: true}
	track.RecordingTags = fromEntryTags(e.Tags)
	for _, tag := range track.RecordingTags {
		track.Tags = append(track.Tags, tag.Name)
	}
	return track
}

func toEntryTags(tags []provider.Tag) []entryTag {
	out := make([]entryTag, len(tags))
	for i, tag := range tags {
		out[i] = entryTag{Name: tag.Name, Count: tag.Count}
	}
	return out
}

func fromEntryTags(tags []entryTag) []provider.Tag {
	if len(tags) == 0 {
		return nil
	}
	out := make([]provider.Tag, len(tags))
	for i, tag := range tags {
		out[i] = provider.Tag{Name: tag.Name, Count: tag.Count}
	}
	return out
}
