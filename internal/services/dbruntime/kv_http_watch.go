package dbruntime

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// This optional private binding publishes no product capability, command or
// UI. It captures one original provider and translates only lease generations.
func (m *Manager) KVHTTPWatch(id int64) (contract.KVHTTPWatchProvider, error) {
	published, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	s := published.(*session)
	if _, err = contract.Require[contract.KVHTTPWatchProvider](s.raw); err != nil {
		return nil, err
	}
	if !s.alive() {
		return nil, contract.ErrRevoked
	}
	return s, nil
}

func (s *session) bindKVHTTPWatch(raw contract.KVHTTPWatchLease) (contract.KVHTTPWatchLease, error) {
	if raw == nil {
		return nil, contract.ErrUnsupported
	}
	scope, expected := raw.Scope().Clone(), s.KVScope()
	if scope.Namespace.ProfileID != s.id || scope.Namespace != expected.Namespace || scope.Generation != s.raw.Generation() ||
		!bytes.Equal(scope.Prefix, expected.Prefix) || scope.Header != expected.Header || scope.HTTP != expected.HTTP || scope.Version != expected.Version || !s.alive() {
		return nil, errors.Join(contract.ErrRevoked, raw.Close())
	}
	scope.Generation = s.generation
	return &kvHTTPWatchLease{session: s, raw: raw, scope: scope}, nil
}

func (s *session) ReserveKVHTTPWatch(document uint64) (contract.KVHTTPWatchLease, error) {
	if !s.alive() {
		return nil, contract.ErrRevoked
	}
	p, err := contract.Require[contract.KVHTTPWatchProvider](s.raw)
	if err != nil {
		return nil, err
	}
	raw, err := p.ReserveKVHTTPWatch(document)
	if err != nil {
		if raw != nil {
			err = errors.Join(err, raw.Close())
		}
		return nil, err
	}
	return s.bindKVHTTPWatch(raw)
}

func (s *session) RecoverKVHTTPWatch(previous contract.KVHTTPWatchLease) (contract.KVHTTPWatchRecovery, error) {
	if !s.alive() {
		return contract.KVHTTPWatchRecovery{}, contract.ErrRevoked
	}
	old, ok := previous.(*kvHTTPWatchLease)
	if !ok || old == nil || old.scope.Namespace.ProfileID != s.id {
		return contract.KVHTTPWatchRecovery{}, contract.ErrKVPlanUsed
	}
	current := s.KVScope()
	if old.scope.Namespace != current.Namespace || !bytes.Equal(old.scope.Prefix, current.Prefix) || old.scope.Header != current.Header || old.scope.HTTP != current.HTTP || old.scope.Version != current.Version {
		return contract.KVHTTPWatchRecovery{}, contract.ErrRevoked
	}
	p, err := contract.Require[contract.KVHTTPWatchProvider](s.raw)
	if err != nil {
		return contract.KVHTTPWatchRecovery{}, err
	}
	old.closed.Store(true)
	r, err := p.RecoverKVHTTPWatch(old.raw)
	if r.Watch != nil {
		v := r.Watch.Clone()
		r.Watch = &v
	}
	if err != nil {
		if r.Lease != nil {
			err = errors.Join(err, r.Lease.Close())
			r.Lease = nil
		}
		return r, err
	}
	r.Lease, err = s.bindKVHTTPWatch(r.Lease)
	return r, err
}

type kvHTTPWatchLease struct {
	session  *session
	raw      contract.KVHTTPWatchLease
	scope    contract.KVScope
	closed   atomic.Bool
	once     sync.Once
	closeErr error
}

type kvHTTPWatchPreparation struct {
	owner   *kvHTTPWatchLease
	raw     contract.KVHTTPWatchPreparation
	request contract.KVHTTPWatchRequest
}

func (p *kvHTTPWatchPreparation) Snapshot() contract.KVHTTPWatchRequest { return p.request }
func (l *kvHTTPWatchLease) Scope() contract.KVScope                     { return l.scope.Clone() }
func (l *kvHTTPWatchLease) CheckKVLease() error {
	if l.closed.Load() || !l.session.alive() {
		return contract.ErrRevoked
	}
	return l.raw.CheckKVLease()
}
func (l *kvHTTPWatchLease) Limits() contract.KVHTTPWatchLimits { return l.raw.Limits() }
func (l *kvHTTPWatchLease) Prepare(r contract.KVHTTPWatchRequest) (contract.KVHTTPWatchPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	p, err := l.raw.Prepare(r)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, contract.ErrUnsupported
	}
	if p.Snapshot() != r {
		return nil, contract.ErrRevoked
	}
	if err = l.CheckKVLease(); err != nil {
		return nil, err
	}
	return &kvHTTPWatchPreparation{owner: l, raw: p, request: r}, nil
}
func (l *kvHTTPWatchLease) Start(ctx context.Context, prepared contract.KVHTTPWatchPreparation) (contract.KVHTTPWatchRun, error) {
	p, ok := prepared.(*kvHTTPWatchPreparation)
	if !ok || p == nil || p.owner != l {
		return nil, contract.ErrKVPlanUsed
	}
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	raw, err := l.raw.Start(ctx, p.raw)
	err = errors.Join(err, l.CheckKVLease())
	if raw == nil {
		return nil, err
	}
	if raw.Request() != p.request {
		return nil, errors.Join(err, contract.ErrRevoked, raw.Close())
	}
	// Even a late original run stays attached to its original request/provider.
	return &kvHTTPWatchRun{raw: raw, request: p.request}, err
}
func (l *kvHTTPWatchLease) LastWatch() *contract.KVHTTPWatchSnapshot {
	r := l.raw.LastWatch()
	if r == nil {
		return nil
	}
	v := r.Clone()
	return &v
}
func (l *kvHTTPWatchLease) ClearObservations() error {
	if err := l.CheckKVLease(); err != nil {
		return err
	}
	return errors.Join(l.raw.ClearObservations(), l.CheckKVLease())
}
func (l *kvHTTPWatchLease) Close() error {
	l.closed.Store(true)
	l.once.Do(func() { l.closeErr = l.raw.Close() })
	return l.closeErr
}

type kvHTTPWatchRun struct {
	raw     contract.KVHTTPWatchRun
	request contract.KVHTTPWatchRequest
}

func (w *kvHTTPWatchRun) Request() contract.KVHTTPWatchRequest   { return w.request }
func (w *kvHTTPWatchRun) Poll() contract.KVHTTPWatchBatch        { return w.raw.Poll().Clone() }
func (w *kvHTTPWatchRun) Snapshot() contract.KVHTTPWatchSnapshot { return w.raw.Snapshot().Clone() }
func (w *kvHTTPWatchRun) Stop(ctx context.Context) error         { return w.raw.Stop(ctx) }
func (w *kvHTTPWatchRun) Close() error                           { return w.raw.Close() }
