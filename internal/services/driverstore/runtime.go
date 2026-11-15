package driverstore

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

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
	return l.start(ctx, profile, nil, options, started)
}

// JDBCSettings is adapter-owned native pool configuration. It cannot supply
// executable paths, JVM arguments, SQL, or credentials.
type JDBCSettings struct {
	MaxConnections, MetadataReserve int
	WaitTimeout                     time.Duration
}

func (s JDBCSettings) arguments() ([]string, error) {
	if s.MaxConnections < 1 || s.MaxConnections > 32 || s.MetadataReserve < 0 || s.MetadataReserve >= s.MaxConnections || s.WaitTimeout < 250*time.Millisecond || s.WaitTimeout > 300*time.Second {
		return nil, ErrPackage
	}
	return []string{
		"-Ddbx.agent.jdbc.pool.enabled=true",
		"-Ddbx.agent.jdbc.pool.maximumPoolSize=" + strconv.Itoa(s.MaxConnections),
		"-Ddbx.agent.jdbc.pool.minimumIdle=0",
		"-Ddbx.agent.jdbc.pool.metadataReserve=" + strconv.Itoa(s.MetadataReserve),
		"-Ddbx.agent.jdbc.pool.globalMaximumPhysicalConnections=" + strconv.Itoa(s.MaxConnections),
		"-Ddbx.agent.jdbc.pool.connectionTimeoutMillis=" + strconv.FormatInt(s.WaitTimeout.Milliseconds(), 10),
		"-Ddbx.agent.jdbc.pool.validationTimeoutMillis=" + strconv.FormatInt(min(s.WaitTimeout.Milliseconds(), 5000), 10),
	}, nil
}

// StartJDBCObserved binds validated pool settings to the captured Java process.
// Settings are independent of the immutable installed package manifest.
func (l *Lease) StartJDBCObserved(ctx context.Context, profile string, settings JDBCSettings, options agentbridge.Options, started func(int) error) (*Runtime, error) {
	return l.start(ctx, profile, &settings, options, started)
}

func (l *Lease) start(ctx context.Context, profile string, settings *JDBCSettings, options agentbridge.Options, started func(int) error) (*Runtime, error) {
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
	if settings != nil {
		args, err := settings.arguments()
		if err != nil || l.manifest.Runtime != "java" {
			l.mu.Unlock()
			return nil, ErrPackage
		}
		spec.Args = append(args, spec.Args...)
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
