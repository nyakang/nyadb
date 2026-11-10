package dbruntime

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// KVEvents binds an optional original-session feature. Product/UI publication
// remains separate; this path never selects a replacement manager or profile.
func (m *Manager) KVEvents(id int64) (contract.KVEventsProvider, error) {
	published, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	s := published.(*session)
	if _, err = contract.Require[contract.KVEventsProvider](s.raw); err != nil {
		return nil, err
	}
	if !s.alive() {
		return nil, contract.ErrRevoked
	}
	return s, nil
}

func (s *session) bindKVEvents(raw contract.KVEventsLease) (contract.KVEventsLease, error) {
	if raw == nil {
		return nil, contract.ErrUnsupported
	}
	scope := raw.Scope().Clone()
	expected := s.KVScope()
	if scope.Namespace.ProfileID != s.id || scope.Namespace != expected.Namespace || scope.Generation != s.raw.Generation() || !bytes.Equal(scope.Prefix, expected.Prefix) || scope.Header != expected.Header || scope.Version != expected.Version || !s.alive() {
		return nil, errors.Join(contract.ErrRevoked, raw.Close())
	}
	scope.Generation = s.generation
	return &kvEventsLease{session: s, raw: raw, scope: scope}, nil
}

func (s *session) ReserveKVEvents(document uint64) (contract.KVEventsLease, error) {
	if !s.alive() {
		return nil, contract.ErrRevoked
	}
	p, err := contract.Require[contract.KVEventsProvider](s.raw)
	if err != nil {
		return nil, err
	}
	raw, err := p.ReserveKVEvents(document)
	if err != nil {
		return nil, err
	}
	return s.bindKVEvents(raw)
}

func (s *session) RecoverKVEvents(previous contract.KVEventsLease) (contract.KVEventsRecovery, error) {
	if !s.alive() {
		return contract.KVEventsRecovery{}, contract.ErrRevoked
	}
	old, ok := previous.(*kvEventsLease)
	if !ok || old == nil || old.scope.Namespace.ProfileID != s.id {
		return contract.KVEventsRecovery{}, contract.ErrKVPlanUsed
	}
	p, err := contract.Require[contract.KVEventsProvider](s.raw)
	if err != nil {
		return contract.KVEventsRecovery{}, err
	}
	current := s.KVScope()
	if old.scope.Namespace != current.Namespace || !bytes.Equal(old.scope.Prefix, current.Prefix) || old.scope.Header.ClusterID != current.Header.ClusterID || old.scope.Header.MemberID != current.Header.MemberID || old.scope.Version != current.Version {
		return contract.KVEventsRecovery{}, contract.ErrRevoked
	}
	old.closed.Store(true)
	recovery, err := p.RecoverKVEvents(old.raw)
	if recovery.History != nil {
		history := recovery.History.Clone()
		recovery.History = &history
	}
	recovery.Watch = recovery.Watch.Clone()
	if err != nil {
		return recovery, err
	}
	recovery.Lease, err = s.bindKVEvents(recovery.Lease)
	return recovery, err
}

type kvEventsLease struct {
	session  *session
	raw      contract.KVEventsLease
	scope    contract.KVScope
	closed   atomic.Bool
	once     sync.Once
	closeErr error
}

func (l *kvEventsLease) Scope() contract.KVScope { return l.scope.Clone() }
func (l *kvEventsLease) CheckKVLease() error {
	if l.closed.Load() || !l.session.alive() {
		return contract.ErrRevoked
	}
	return l.raw.CheckKVLease()
}
func (l *kvEventsLease) Preconnect(ctx context.Context) error {
	if err := l.CheckKVLease(); err != nil {
		return err
	}
	return errors.Join(l.raw.Preconnect(ctx), l.CheckKVLease())
}
func (l *kvEventsLease) StartWatch(ctx context.Context, input contract.KVWatchRequest) (contract.KVWatchRun, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	request := input.Clone()
	raw, err := l.raw.StartWatch(ctx, request.Clone())
	err = errors.Join(err, l.CheckKVLease())
	if raw == nil {
		return nil, err
	}
	captured := raw.Request().Clone()
	if !sameKVWatchRequest(request, captured) {
		return nil, errors.Join(err, contract.ErrRevoked, raw.Close())
	}
	return &kvEventsRun{lease: l, raw: raw, request: captured}, err
}

func sameKVWatchRequest(a, b contract.KVWatchRequest) bool {
	return bytes.Equal(a.Key, b.Key) && bytes.Equal(a.RangeEnd, b.RangeEnd) && a.StartRevision == b.StartRevision && a.WatchID == b.WatchID && a.PrevKV == b.PrevKV && a.ProgressNotify == b.ProgressNotify && a.NoPut == b.NoPut && a.NoDelete == b.NoDelete
}
func (l *kvEventsLease) History(ctx context.Context, input contract.KVHistoryRequest) (contract.KVHistoryResult, error) {
	request := input.Clone()
	if err := l.CheckKVLease(); err != nil {
		return contract.KVHistoryResult{Authored: request.Clone(), Window: request.Clone()}, err
	}
	result, err := l.raw.History(ctx, request)
	// Original receipts survive publication replacement. Their document's UI
	// decides admission; generation changes never substitute a new request.
	return result.Clone(), errors.Join(err, l.CheckKVLease())
}
func (l *kvEventsLease) LastHistory() *contract.KVHistoryResult {
	r := l.raw.LastHistory()
	if r == nil {
		return nil
	}
	c := r.Clone()
	return &c
}
func (l *kvEventsLease) ClearReceipts() error {
	if err := l.CheckKVLease(); err != nil {
		return err
	}
	return errors.Join(l.raw.ClearReceipts(), l.CheckKVLease())
}
func (l *kvEventsLease) Close() error {
	l.closed.Store(true)
	l.once.Do(func() { l.closeErr = l.raw.Close() })
	return l.closeErr
}

type kvEventsRun struct {
	lease   *kvEventsLease
	raw     contract.KVWatchRun
	request contract.KVWatchRequest
}

func (w *kvEventsRun) Request() contract.KVWatchRequest { return w.request.Clone() }
func (w *kvEventsRun) Poll() contract.KVWatchBatch      { return w.raw.Poll().Clone() }
func (w *kvEventsRun) Progress() error {
	if err := w.lease.CheckKVLease(); err != nil {
		return err
	}
	return w.raw.Progress()
}
func (w *kvEventsRun) Stop(ctx context.Context) error { return w.raw.Stop(ctx) }
func (w *kvEventsRun) Close() error                   { return w.raw.Close() }
