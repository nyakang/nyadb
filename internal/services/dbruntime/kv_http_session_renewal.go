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
func (m *Manager) KVHTTPSessionRenewal(id int64) (contract.KVHTTPSessionRenewalProvider, error) {
	published, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	s := published.(*session)
	if _, err = contract.Require[contract.KVHTTPSessionRenewalProvider](s.raw); err != nil {
		return nil, err
	}
	if !s.alive() {
		return nil, contract.ErrRevoked
	}
	return s, nil
}

func (s *session) bindKVHTTPSessionRenewal(raw contract.KVHTTPSessionRenewalLease) (contract.KVHTTPSessionRenewalLease, error) {
	if raw == nil {
		return nil, contract.ErrUnsupported
	}
	scope, expected := raw.Scope().Clone(), s.KVScope()
	if scope.Namespace.ProfileID != s.id || scope.Namespace != expected.Namespace || scope.Generation != s.raw.Generation() ||
		!bytes.Equal(scope.Prefix, expected.Prefix) || scope.Header != expected.Header || scope.HTTP != expected.HTTP || scope.Version != expected.Version || !s.alive() {
		return nil, errors.Join(contract.ErrRevoked, raw.Close())
	}
	scope.Generation = s.generation
	return &kvHTTPSessionRenewalLease{session: s, raw: raw, scope: scope}, nil
}

func (s *session) ReserveKVHTTPSessionRenewal(document uint64) (contract.KVHTTPSessionRenewalLease, error) {
	if !s.alive() {
		return nil, contract.ErrRevoked
	}
	p, err := contract.Require[contract.KVHTTPSessionRenewalProvider](s.raw)
	if err != nil {
		return nil, err
	}
	raw, err := p.ReserveKVHTTPSessionRenewal(document)
	if err != nil {
		if raw != nil {
			err = errors.Join(err, raw.Close())
		}
		return nil, err
	}
	return s.bindKVHTTPSessionRenewal(raw)
}

func (s *session) RecoverKVHTTPSessionRenewal(previous contract.KVHTTPSessionRenewalLease) (contract.KVHTTPSessionRenewalRecovery, error) {
	if !s.alive() {
		return contract.KVHTTPSessionRenewalRecovery{}, contract.ErrRevoked
	}
	old, ok := previous.(*kvHTTPSessionRenewalLease)
	if !ok || old == nil || old.scope.Namespace.ProfileID != s.id {
		return contract.KVHTTPSessionRenewalRecovery{}, contract.ErrKVPlanUsed
	}
	current := s.KVScope()
	if old.scope.Namespace != current.Namespace || !bytes.Equal(old.scope.Prefix, current.Prefix) || old.scope.Header != current.Header || old.scope.HTTP != current.HTTP || old.scope.Version != current.Version {
		return contract.KVHTTPSessionRenewalRecovery{}, contract.ErrRevoked
	}
	p, err := contract.Require[contract.KVHTTPSessionRenewalProvider](s.raw)
	if err != nil {
		return contract.KVHTTPSessionRenewalRecovery{}, err
	}
	old.closed.Store(true)
	r, err := p.RecoverKVHTTPSessionRenewal(old.raw)
	if r.Renewal != nil {
		v := r.Renewal.Clone()
		r.Renewal = &v
	}
	if err != nil {
		if r.Lease != nil {
			err = errors.Join(err, r.Lease.Close())
			r.Lease = nil
		}
		return r, err
	}
	r.Lease, err = s.bindKVHTTPSessionRenewal(r.Lease)
	return r, err
}

type kvHTTPSessionRenewalLease struct {
	session  *session
	raw      contract.KVHTTPSessionRenewalLease
	scope    contract.KVScope
	closed   atomic.Bool
	once     sync.Once
	closeErr error
}

type kvHTTPSessionRenewalPreparation struct {
	owner   *kvHTTPSessionRenewalLease
	raw     contract.KVHTTPSessionRenewalPreparation
	request contract.KVHTTPSessionRenewalRequest
}

func (p *kvHTTPSessionRenewalPreparation) Snapshot() contract.KVHTTPSessionRenewalRequest {
	return p.request
}
func (l *kvHTTPSessionRenewalLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *kvHTTPSessionRenewalLease) CheckKVLease() error {
	if l.closed.Load() || !l.session.alive() {
		return contract.ErrRevoked
	}
	return l.raw.CheckKVLease()
}
func (l *kvHTTPSessionRenewalLease) Limits() contract.KVHTTPSessionRenewalLimits {
	return l.raw.Limits()
}
func (l *kvHTTPSessionRenewalLease) Prepare(r contract.KVHTTPSessionRenewalRequest) (contract.KVHTTPSessionRenewalPreparation, error) {
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
	return &kvHTTPSessionRenewalPreparation{owner: l, raw: p, request: r}, nil
}
func (l *kvHTTPSessionRenewalLease) Start(ctx context.Context, prepared contract.KVHTTPSessionRenewalPreparation) (contract.KVHTTPSessionRenewalRun, error) {
	p, ok := prepared.(*kvHTTPSessionRenewalPreparation)
	if !ok || p == nil || p.owner != l {
		return nil, contract.ErrKVPlanUsed
	}
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	if p.raw.Snapshot() != p.request {
		return nil, contract.ErrRevoked
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
	return &kvHTTPSessionRenewalRun{raw: raw, request: p.request}, err
}
func (l *kvHTTPSessionRenewalLease) LastRenewal() *contract.KVHTTPSessionRenewalSnapshot {
	r := l.raw.LastRenewal()
	if r == nil {
		return nil
	}
	v := r.Clone()
	return &v
}

func (l *kvHTTPSessionRenewalLease) RetainedRenewal() contract.KVHTTPSessionRenewalRun {
	p, ok := l.raw.(contract.KVHTTPSessionRenewalArchiveLease)
	if !ok {
		return nil
	}
	raw := p.RetainedRenewal()
	if raw == nil {
		return nil
	}
	return &kvHTTPSessionRenewalRun{raw: raw, request: raw.Request()}
}
func (l *kvHTTPSessionRenewalLease) ClearReceipts() error {
	if err := l.CheckKVLease(); err != nil {
		return err
	}
	return errors.Join(l.raw.ClearReceipts(), l.CheckKVLease())
}
func (l *kvHTTPSessionRenewalLease) Close() error {
	l.closed.Store(true)
	l.once.Do(func() { l.closeErr = l.raw.Close() })
	return l.closeErr
}

type kvHTTPSessionRenewalRun struct {
	raw     contract.KVHTTPSessionRenewalRun
	request contract.KVHTTPSessionRenewalRequest
}

func (w *kvHTTPSessionRenewalRun) Request() contract.KVHTTPSessionRenewalRequest { return w.request }
func (w *kvHTTPSessionRenewalRun) Poll() contract.KVHTTPSessionRenewalBatch {
	return w.raw.Poll().Clone()
}
func (w *kvHTTPSessionRenewalRun) Snapshot() contract.KVHTTPSessionRenewalSnapshot {
	return w.raw.Snapshot().Clone()
}
func (w *kvHTTPSessionRenewalRun) Stop(ctx context.Context) error { return w.raw.Stop(ctx) }
func (w *kvHTTPSessionRenewalRun) Close() error                   { return w.raw.Close() }

func (w *kvHTTPSessionRenewalRun) PollStatus() contract.KVHTTPSessionRenewalStatus {
	if indexed, ok := w.raw.(contract.KVHTTPSessionRenewalIndexedRun); ok {
		return indexed.PollStatus()
	}
	b := w.raw.Poll()
	// Fallback providers have no indexed archive; Snapshot supplies the total.
	return contract.KVHTTPSessionRenewalStatus{Observations: len(w.raw.Snapshot().Observations), Ended: b.Ended,
		TerminalOverflow: b.TerminalOverflow, RetainedBytes: b.RetainedBytes, Terminal: b.Terminal}
}

func (w *kvHTTPSessionRenewalRun) Observation(ordinal int) (contract.KVHTTPSessionRenewalObservation, bool) {
	if indexed, ok := w.raw.(contract.KVHTTPSessionRenewalIndexedRun); ok {
		return indexed.Observation(ordinal)
	}
	s := w.raw.Snapshot()
	if ordinal < 0 || ordinal >= len(s.Observations) {
		return contract.KVHTTPSessionRenewalObservation{}, false
	}
	return s.Observations[ordinal], true
}
