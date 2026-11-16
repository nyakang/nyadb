package driverstore

import "context"

type runtimeObserverKey struct{}

// WithRuntimeObserver records the original managed process before handshake.
// It is request-scoped and does not change the pinned package or launch args.
func WithRuntimeObserver(ctx context.Context, started func(int) error) context.Context {
	return context.WithValue(ctx, runtimeObserverKey{}, started)
}

// RuntimeObserver captures the original observer for providers whose later
// document processes use an independent lifetime. It cannot change launch args.
func RuntimeObserver(ctx context.Context) func(int) error {
	if ctx == nil {
		return nil
	}
	started, _ := ctx.Value(runtimeObserverKey{}).(func(int) error)
	return started
}
