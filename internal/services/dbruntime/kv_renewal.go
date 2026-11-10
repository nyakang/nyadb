package dbruntime

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// KVRenewal binds the optional original-session feature. The workbench must
// separately capture a user's explicit authorization before starting a run.
func (m *Manager) KVRenewal(id int64) (contract.KVRenewalProvider, error) {
	published, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	s := published.(*session)
	if _, err = contract.Require[contract.KVRenewalProvider](s.raw); err != nil {
		return nil, err
	}
	if !s.alive() {
		return nil, contract.ErrRevoked
	}
	return s, nil
}
func (s *session) bindKVRenewal(raw contract.KVRenewalLease) (contract.KVRenewalLease, error) {
	if raw == nil {
		return nil, contract.ErrUnsupported
	}
	scope := raw.Scope().Clone()
	expected := s.KVScope()
	if scope.Namespace.ProfileID != s.id || scope.Namespace != expected.Namespace || scope.Generation != s.raw.Generation() || !bytes.Equal(scope.Prefix, expected.Prefix) || scope.Header != expected.Header || scope.Version != expected.Version || !s.alive() {
		return nil, errors.Join(contract.ErrRevoked, raw.Close())
	}
	scope.Generation = s.generation
	return &kvRenewalLease{session: s, raw: raw, scope: scope}, nil
}
func (s *session) ReserveKVRenewal(document uint64) (contract.KVRenewalLease, error) {
	if !s.alive() {
		return nil, contract.ErrRevoked
	}
	p, err := contract.Require[contract.KVRenewalProvider](s.raw)
	if err != nil {
		return nil, err
	}
	raw, err := p.ReserveKVRenewal(document)
	if err != nil {
		return nil, err
	}
	return s.bindKVRenewal(raw)
}
func (s *session) RecoverKVRenewal(previous contract.KVRenewalLease) (contract.KVRenewalRecovery, error) {
	if !s.alive() {
		return contract.KVRenewalRecovery{}, contract.ErrRevoked
	}
	old, ok := previous.(*kvRenewalLease)
	if !ok || old == nil || old.scope.Namespace.ProfileID != s.id {
		return contract.KVRenewalRecovery{}, contract.ErrKVPlanUsed
	}
	p, err := contract.Require[contract.KVRenewalProvider](s.raw)
	if err != nil {
		return contract.KVRenewalRecovery{}, err
	}
	current := s.KVScope()
	if old.scope.Namespace != current.Namespace || !bytes.Equal(old.scope.Prefix, current.Prefix) || old.scope.Header.ClusterID != current.Header.ClusterID || old.scope.Header.MemberID != current.Header.MemberID || old.scope.Version != current.Version {
		return contract.KVRenewalRecovery{}, contract.ErrRevoked
	}
	old.closed.Store(true)
	recovery, err := p.RecoverKVRenewal(old.raw)
	recovery.Batch = recovery.Batch.Clone()
	if err != nil {
		return recovery, err
	}
	recovery.Lease, err = s.bindKVRenewal(recovery.Lease)
	return recovery, err
}

type kvRenewalLease struct {
	session  *session
	raw      contract.KVRenewalLease
	scope    contract.KVScope
	closed   atomic.Bool
	once     sync.Once
	closeErr error
}
type kvRenewalPreparation struct {
	owner    *kvRenewalLease
	raw      contract.KVRenewalPreparation
	snapshot contract.KVRenewalRequest
}

func (p *kvRenewalPreparation) Snapshot() contract.KVRenewalRequest { return p.snapshot }
func (l *kvRenewalLease) Scope() contract.KVScope                   { return l.scope.Clone() }
func (l *kvRenewalLease) CheckKVLease() error {
	if l.closed.Load() || !l.session.alive() {
		return contract.ErrRevoked
	}
	return l.raw.CheckKVLease()
}
func (l *kvRenewalLease) Prepare(request contract.KVRenewalRequest) (contract.KVRenewalPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	p, err := l.raw.Prepare(request)
	if err != nil {
		return nil, err
	}
	if p == nil || request.LeaseID == 0 || p.Snapshot() != request {
		return nil, contract.ErrRevoked
	}
	if err = l.CheckKVLease(); err != nil {
		return nil, err
	}
	return &kvRenewalPreparation{owner: l, raw: p, snapshot: request}, nil
}
func (l *kvRenewalLease) Preconnect(ctx context.Context) error {
	if err := l.CheckKVLease(); err != nil {
		return err
	}
	return errors.Join(l.raw.Preconnect(ctx), l.CheckKVLease())
}
func (l *kvRenewalLease) Start(ctx context.Context, prepared contract.KVRenewalPreparation) (contract.KVRenewalRun, error) {
	p, ok := prepared.(*kvRenewalPreparation)
	if !ok || p == nil || p.owner != l {
		return nil, contract.ErrKVPlanUsed
	}
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	if p.raw.Snapshot() != p.snapshot {
		return nil, contract.ErrRevoked
	}
	raw, err := l.raw.Start(ctx, p.raw)
	err = errors.Join(err, l.CheckKVLease())
	if raw == nil {
		return nil, err
	}
	if raw.Request() != p.snapshot {
		return nil, errors.Join(err, contract.ErrRevoked, raw.Close())
	}
	// A late original completion remains owned by its original document, even
	// when the accompanying error reports publication replacement.
	return &kvRenewalRun{raw: raw, request: p.snapshot}, err
}
func (l *kvRenewalLease) ClearReceipts() error {
	if err := l.CheckKVLease(); err != nil {
		return err
	}
	return errors.Join(l.raw.ClearReceipts(), l.CheckKVLease())
}
func (l *kvRenewalLease) Close() error {
	l.closed.Store(true)
	l.once.Do(func() { l.closeErr = l.raw.Close() })
	return l.closeErr
}

type kvRenewalRun struct {
	raw     contract.KVRenewalRun
	request contract.KVRenewalRequest
}

func (r *kvRenewalRun) Request() contract.KVRenewalRequest { return r.request }
func (r *kvRenewalRun) Poll() contract.KVRenewalBatch      { return r.raw.Poll().Clone() }
func (r *kvRenewalRun) Stop(ctx context.Context) error     { return r.raw.Stop(ctx) }
func (r *kvRenewalRun) Close() error                       { return r.raw.Close() }
