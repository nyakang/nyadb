package connections

import "context"

// This vault key cannot collide with a valid alternative configuration name.
const RedisSentinelCredential = "\x00redis-sentinel"

type redisSecretKey struct{}
type redisTransientSecret struct{ password string }

func (redisTransientSecret) String() string   { return "Redis Sentinel credential (redacted)" }
func (redisTransientSecret) GoString() string { return "Redis Sentinel credential (redacted)" }

// WithRedisSentinelPassword carries a captured secret through injected neutral
// connection/test APIs without storing it in public profiles or changing their API.
func WithRedisSentinelPassword(ctx context.Context, password string) context.Context {
	return context.WithValue(ctx, redisSecretKey{}, redisTransientSecret{password})
}
func RedisSentinelPassword(ctx context.Context) string {
	v, _ := ctx.Value(redisSecretKey{}).(redisTransientSecret)
	return v.password
}
