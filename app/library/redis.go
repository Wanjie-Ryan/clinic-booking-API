package library

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisKey prefixes a cache key with REDIS_KEY_PREFIX, so keys stay namespaced
// if this Redis instance is ever shared with something else.
func redisKey(key string) string {
	prefix := os.Getenv("REDIS_KEY_PREFIX")
	if prefix == "" {
		return key
	}
	return fmt.Sprintf("%s:%s", prefix, key)
}

// GetRedisKey fetches a value. On a cache miss this returns redis.Nil, which
// callers should check for with errors.Is(err, redis.Nil) rather than treating
// every non-nil error as a real failure.
func GetRedisKey(ctx context.Context, conn *redis.Client, key string) (string, error) {
	return conn.Get(ctx, redisKey(key)).Result()
}

// SetRedisKeyWithExpiry sets a value with a TTL.
func SetRedisKeyWithExpiry(ctx context.Context, conn *redis.Client, key string, value string, ttl time.Duration) error {
	return conn.Set(ctx, redisKey(key), value, ttl).Err()
}

// DeleteRedisKey removes a key. Used for cache invalidation.
func DeleteRedisKey(ctx context.Context, conn *redis.Client, key string) error {
	return conn.Del(ctx, redisKey(key)).Err()
}
