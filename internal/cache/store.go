// Package cache keeps MusicBrainz lookups in Redis so repeated comparisons do
// not spend the upstream rate limit.
package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store is the minimal key-value interface the cache needs.
type Store interface {
	// Get returns found == false, with a nil error, when the key is absent.
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// RedisStore is a Store backed by Redis.
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore builds a store from a Redis URL such as
// "redis://redis:6379/0". The client is configured to fail fast: the cache is
// an optimisation, so a slow or missing Redis must cost milliseconds, not
// seconds. Callers are still expected to bound every call with a context
// deadline, which the client honours.
func NewRedisStore(redisURL string, logger *slog.Logger) (*RedisStore, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("cache: parse REDIS_URL: %w", err)
	}
	opts.DialTimeout = 200 * time.Millisecond
	opts.ReadTimeout = 200 * time.Millisecond
	opts.WriteTimeout = 200 * time.Millisecond
	opts.PoolTimeout = 200 * time.Millisecond
	opts.ContextTimeoutEnabled = true
	opts.MaxRetries = -1 // 0 would mean "default" (3 retries)
	opts.DialerRetries = 1

	redis.SetLogger(slogAdapter{logger})

	return &RedisStore{client: redis.NewClient(opts)}, nil
}

func (s *RedisStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	value, err := s.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func (s *RedisStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return s.client.Set(ctx, key, value, ttl).Err()
}

// Ping reports whether Redis is reachable.
func (s *RedisStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

// Close releases the connection pool.
func (s *RedisStore) Close() error {
	return s.client.Close()
}

// slogAdapter routes go-redis's internal log lines to slog at DEBUG. They
// repeat what the cache already reports once per outage at WARN, so at the
// default INFO level they are silent.
type slogAdapter struct {
	logger *slog.Logger
}

func (a slogAdapter) Printf(ctx context.Context, format string, v ...interface{}) {
	if a.logger.Enabled(ctx, slog.LevelDebug) {
		a.logger.Debug("redis client", "detail", fmt.Sprintf(format, v...))
	}
}
