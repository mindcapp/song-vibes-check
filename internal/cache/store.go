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
// "redis://redis:6379/0". Timeouts are deliberately short: the cache is an
// optimisation, and a slow Redis must not slow requests down.
func NewRedisStore(redisURL string, logger *slog.Logger) (*RedisStore, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("cache: parse REDIS_URL: %w", err)
	}
	opts.DialTimeout = 500 * time.Millisecond
	opts.ReadTimeout = 300 * time.Millisecond
	opts.WriteTimeout = 300 * time.Millisecond
	opts.MaxRetries = 0

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

// slogAdapter routes go-redis's internal log lines through slog so they stay
// structured like the rest of the service's output.
type slogAdapter struct {
	logger *slog.Logger
}

func (a slogAdapter) Printf(_ context.Context, format string, v ...interface{}) {
	a.logger.Warn("redis client", "detail", fmt.Sprintf(format, v...))
}
