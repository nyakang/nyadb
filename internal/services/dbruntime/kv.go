package dbruntime

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// KVRequests binds an explicitly installed optional native KV provider. Builtin
// registration and product/UI publication are separate from this feature path.
func (m *Manager) KVRequests(id int64) (contract.KVRequestsProvider, error) {
	published, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	s := published.(*session)
	if _, err = contract.Require[contract.KVRequestsProvider](s.raw); err != nil {
		return nil, err
	}
	if !s.alive() {
		return nil, contract.ErrRevoked
	}
	return s, nil
}
func (s *session) KVScope() contract.KVScope {
	p, err := contract.Require[interface{ KVScope() contract.KVScope }](s.raw)
	if err != nil {
		return contract.KVScope{}
	}
	scope := p.KVScope().Clone()
	if scope.Namespace.ProfileID != s.id || scope.Generation != s.raw.Generation() {
		return contract.KVScope{}
	}
	scope.Generation = s.generation
	return scope
}
func (s *session) bindKV(raw contract.KVRequestLease) (contract.KVRequestLease, error) {
	if raw == nil {
		return nil, contract.ErrUnsupported
	}
	scope := raw.Scope().Clone()
	expected := s.KVScope()
	if scope.Namespace.ProfileID != s.id || scope.Namespace != expected.Namespace || scope.Generation != s.raw.Generation() || !bytes.Equal(scope.Prefix, expected.Prefix) || scope.Header != expected.Header || scope.Version != expected.Version || !s.alive() {
		return nil, errors.Join(contract.ErrRevoked, raw.Close())
	}
	scope.Generation = s.generation
	bound := &kvRequestLease{session: s, raw: raw, scope: scope}
	if _, ok := raw.(contract.KVLeaseBrowsing); ok {
		return &kvLeaseBrowsingLease{bound}, nil
	}
	return bound, nil
}
func (s *session) ReserveKV(document uint64) (contract.KVRequestLease, error) {
	if !s.alive() {
		return nil, contract.ErrRevoked
	}
	p, err := contract.Require[contract.KVRequestsProvider](s.raw)
	if err != nil {
		return nil, err
	}
	raw, err := p.ReserveKV(document)
	if err != nil {
		return nil, err
	}
	return s.bindKV(raw)
}
func (s *session) RecoverKV(previous contract.KVRequestLease) (contract.KVRecovery, error) {
	if !s.alive() {
		return contract.KVRecovery{}, contract.ErrRevoked
	}
	old := originalKVRequestLease(previous)
	if old == nil || old.scope.Namespace.ProfileID != s.id {
		return contract.KVRecovery{}, contract.ErrKVPlanUsed
	}
	p, err := contract.Require[contract.KVRequestsProvider](s.raw)
	if err != nil {
		return contract.KVRecovery{}, err
	}
	current := s.KVScope()
	if old.scope.Namespace != current.Namespace || !bytes.Equal(old.scope.Prefix, current.Prefix) || old.scope.Header.ClusterID != current.Header.ClusterID || old.scope.Header.MemberID != current.Header.MemberID || old.scope.Version != current.Version {
		return contract.KVRecovery{}, contract.ErrRevoked
	}
	// Revoke the original wrapper immediately; the raw provider waits for its
	// executor and transfers only evidence, never an old prepared request.
	old.closed.Store(true)
	recovery, err := p.RecoverKV(old.raw)
	recovery.Last = recovery.Last.Clone()
	if err != nil {
		return recovery, err
	}
	recovery.Lease, err = s.bindKV(recovery.Lease)
	return recovery, err
}

type kvRequestLease struct {
	session  *session
	raw      contract.KVRequestLease
	scope    contract.KVScope
	closed   atomic.Bool
	once     sync.Once
	closeErr error
}
type kvRequestPreparation struct {
	owner    *kvRequestLease
	raw      contract.KVPreparation
	snapshot contract.KVRequest
	write    bool
	record   contract.KVRequestRecord
}

func (p *kvRequestPreparation) Snapshot() contract.KVRequest { return p.snapshot }
func (p *kvRequestPreparation) Writes() bool                 { return p.write }
func (p *kvRequestPreparation) RecordedRequest() contract.KVRequestRecord {
	return p.record.Clone()
}
func (l *kvRequestLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *kvRequestLease) CheckKVLease() error {
	if l.closed.Load() || !l.session.alive() {
		return contract.ErrRevoked
	}
	return l.raw.CheckKVLease()
}
func (l *kvRequestLease) Prepare(r contract.KVRequest) (contract.KVPreparation, error) {
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
	if err = l.CheckKVLease(); err != nil {
		return nil, err
	}
	snapshot := p.Snapshot()
	record := contract.KVRequestRecord{Request: snapshot}
	if recorder, ok := p.(contract.KVPreparationRecord); ok {
		record = recorder.RecordedRequest().Clone()
	}
	return &kvRequestPreparation{owner: l, raw: p, snapshot: snapshot, write: p.Writes(), record: record}, nil
}
func (l *kvRequestLease) Preconnect(ctx context.Context) error {
	if err := l.CheckKVLease(); err != nil {
		return err
	}
	err := l.raw.Preconnect(ctx)
	return errors.Join(err, l.CheckKVLease())
}
func (l *kvRequestLease) Execute(ctx context.Context, p contract.KVPreparation) (contract.KVResult, error) {
	original, ok := p.(*kvRequestPreparation)
	if !ok || original == nil || original.owner != l {
		return contract.KVResult{}, contract.ErrKVPlanUsed
	}
	if err := l.CheckKVLease(); err != nil {
		record := original.RecordedRequest()
		return contract.KVResult{Request: record.Request, RequestOmittedFields: record.OmittedFields}, err
	}
	r, err := l.raw.Execute(ctx, original.raw)
	r = r.Clone()
	// Never erase an actual original receipt merely because the publication was
	// revoked while the captured executor finished. The UI owns admission.
	return r, errors.Join(err, l.CheckKVLease())
}
func (l *kvRequestLease) LastResult() contract.KVResult { return l.raw.LastResult().Clone() }
func (l *kvRequestLease) Close() error {
	l.closed.Store(true)
	l.once.Do(func() { l.closeErr = l.raw.Close() })
	return l.closeErr
}
