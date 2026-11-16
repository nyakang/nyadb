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
	spec, err := l.prepareStart(ctx, profile, settings, false, started)
	if err != nil {
		return nil, err
	}
	client, err := agentbridge.Start(ctx, spec, options)
	if err != nil {
		return nil, errors.Join(err, l.releaseRuntime())
	}
	return &Runtime{Client: client, lease: l}, nil
}

func (l *Lease) prepareStart(ctx context.Context, profile string, settings *JDBCSettings, single bool, started func(int) error) (agentbridge.LaunchSpec, error) {
	if ctx == nil {
		return agentbridge.LaunchSpec{}, ErrPackage
	}
	l.mu.Lock()
	if l.active || l.consumed {
		l.mu.Unlock()
		return agentbridge.LaunchSpec{}, ErrBusy
	}
	spec, err := l.launchSpec(profile)
	if err != nil {
		l.mu.Unlock()
		return agentbridge.LaunchSpec{}, err
	}
	if single && l.manifest.Runtime != "native" {
		l.mu.Unlock()
		return agentbridge.LaunchSpec{}, ErrPackage
	}
	if settings != nil {
		args, err := settings.arguments()
		if err != nil || l.manifest.Runtime != "java" {
			l.mu.Unlock()
			return agentbridge.LaunchSpec{}, ErrPackage
		}
		spec.Args = append(args, spec.Args...)
	}
	l.active = true
	l.consumed = true
	l.mu.Unlock()
	if started == nil {
		started, _ = ctx.Value(runtimeObserverKey{}).(func(int) error)
	}
	spec.Started = started
	return spec, nil
}

func (l *Lease) releaseRuntime() error {
	l.mu.Lock()
	l.active = false
	l.mu.Unlock()
	return l.Close()
}

func (r *Runtime) Close() error {
	r.once.Do(func() {
		r.err = r.Client.Close()
		r.err = errors.Join(r.err, r.lease.releaseRuntime())
	})
	return r.err
}

// SingleSessionRuntime binds a native v1 process to its immutable package
// marker. It never supplies the Java/v2 Client or permits rebinding a process.
type SingleSessionRuntime struct {
	Session *agentbridge.SingleSession
	lease   *Lease
	once    sync.Once
	err     error
}

func (l *Lease) StartSingleSession(ctx context.Context, profile string, options agentbridge.Options) (*SingleSessionRuntime, error) {
	return l.StartSingleSessionObserved(ctx, profile, options, nil)
}

func (l *Lease) StartSingleSessionObserved(ctx context.Context, profile string, options agentbridge.Options, started func(int) error) (*SingleSessionRuntime, error) {
	spec, err := l.prepareStart(ctx, profile, nil, true, started)
	if err != nil {
		return nil, err
	}
	session, err := agentbridge.StartSingleSession(ctx, spec, options)
	if err != nil {
		return nil, errors.Join(err, l.releaseRuntime())
	}
	return &SingleSessionRuntime{Session: session, lease: l}, nil
}

func (r *SingleSessionRuntime) Close() error {
	r.once.Do(func() {
		r.err = errors.Join(r.Session.Close(), r.lease.releaseRuntime())
	})
	return r.err
}
