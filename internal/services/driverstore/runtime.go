package driverstore

import (
	"context"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

// Runtime binds the original process and its package marker. Close waits for
// the executor/process before releasing artifacts. Recovery takes a new lease.
type Runtime struct {
	Client *agentbridge.Client
	lease  *Lease
	once   sync.Once
	err    error
}

// Start transfers the lease's lifetime to the original runtime. A failed start
// waits for its captured process and releases the package; it never retries.
func (l *Lease) Start(ctx context.Context, profile string, options agentbridge.Options) (*Runtime, error) {
	return l.StartObserved(ctx, profile, options, nil)
}

// StartObserved records the captured process before handshake/connect can wait.
// The observer never publishes a database session or supplies launch arguments.
func (l *Lease) StartObserved(ctx context.Context, profile string, options agentbridge.Options, started func(int) error) (*Runtime, error) {
	l.mu.Lock()
	if l.active || l.consumed {
		l.mu.Unlock()
		return nil, ErrBusy
	}
	spec, err := l.launchSpec(profile)
	if err != nil {
		l.mu.Unlock()
		return nil, err
	}
	l.active = true
	l.consumed = true
	l.mu.Unlock()
	spec.Started = started
	client, err := agentbridge.Start(ctx, spec, options)
	if err != nil {
		l.mu.Lock()
		l.active = false
		l.mu.Unlock()
		return nil, errors.Join(err, l.Close())
	}
	return &Runtime{Client: client, lease: l}, nil
}

func (r *Runtime) Close() error {
	r.once.Do(func() {
		r.err = r.Client.Close()
		r.lease.mu.Lock()
		r.lease.active = false
		r.lease.mu.Unlock()
		r.err = errors.Join(r.err, r.lease.Close())
	})
	return r.err
}
