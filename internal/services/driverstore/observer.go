package driverstore

import "context"

type runtimeObserverKey struct{}

// WithRuntimeObserver records the original managed process before handshake.
// It is request-scoped and does not change the pinned package or launch args.
func WithRuntimeObserver(ctx context.Context, started func(int) error) context.Context {
	return context.WithValue(ctx, runtimeObserverKey{}, started)
}
