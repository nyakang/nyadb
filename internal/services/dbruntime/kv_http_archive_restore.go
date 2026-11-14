package dbruntime

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"reflect"
	"slices"
)

type kvHTTPRestorePreparation struct {
	owner *kvHTTPManagementLease
	raw   contract.KVHTTPArchiveRestorePreparation
	scope contract.KVHTTPArchiveScope
}

func (p *kvHTTPRestorePreparation) Request() contract.KVHTTPArchiveRestoreRequest {
	return p.raw.Request().Clone()
}
func (p *kvHTTPRestorePreparation) Scope() contract.KVHTTPArchiveScope { return p.scope.Clone() }
func (p *kvHTTPRestorePreparation) Source() contract.KVHTTPArchiveResult {
	return p.raw.Source().Clone()
}
func (p *kvHTTPRestorePreparation) LocalRecordID() string { return p.raw.LocalRecordID() }
func (p *kvHTTPRestorePreparation) TargetURL() string     { return p.raw.TargetURL() }
func (p *kvHTTPRestorePreparation) Close() error          { return p.raw.Close() }
func (l *kvHTTPManagementLease) prepareHTTPArchiveRestore(in contract.KVHTTPArchiveRestoreRequest) (contract.KVHTTPArchiveRestorePreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	f, ok := l.base.raw.(contract.KVHTTPArchiveRestoring)
	if !ok {
		return nil, contract.ErrUnsupported
	}
	p, err := f.PrepareHTTPArchiveRestore(in.Clone())
	if err != nil {
		return nil, err
	}
	rawScope := l.Scope()
	rawScope.Generation = l.base.session.raw.Generation()
	if p == nil || p.Scope().DocumentID != l.base.document || !sameKVACLScope(p.Scope().Original, rawScope) || !reflect.DeepEqual(p.Request(), in) {
		if p != nil {
			_ = p.Close()
		}
		return nil, contract.ErrRevoked
	}
	return &kvHTTPRestorePreparation{l, p, contract.KVHTTPArchiveScope{Original: l.Scope(), DocumentID: l.base.document}}, nil
}
func (l *kvHTTPManagementLease) executeHTTPArchiveRestore(ctx context.Context, prepared contract.KVHTTPArchiveRestorePreparation) (out contract.KVHTTPArchiveRestoreResult, err error) {
	p, ok := prepared.(*kvHTTPRestorePreparation)
	if !ok || p == nil || p.owner != l {
		return out, contract.ErrKVPlanUsed
	}
	if err = l.CheckKVLease(); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	original := l.base.session
	m := original.manager
	if m == nil {
		return out, contract.ErrUnsupported
	}
	ids, finish, err := m.beginHTTPRestore(original, l.base.raw)
	if err != nil {
		return out, err
	}
	defer finish()
	defer func() { err = errors.Join(err, original.Close()) }()
	request := p.Request()
	record, scope := p.LocalRecordID(), p.Scope()
	out, err = l.base.raw.(contract.KVHTTPArchiveRestoring).ExecuteHTTPArchiveRestore(ctx, p.raw)
	out = out.Clone()
	rawScope := scope.Original.Clone()
	rawScope.Generation = original.raw.Generation()
	if !sameKVACLScope(out.Scope.Original, rawScope) || out.Scope.DocumentID != scope.DocumentID || out.LocalRecordID != record || !reflect.DeepEqual(out.Request, request) {
		return out, errors.Join(err, contract.ErrRevoked)
	}
	out.Scope = scope
	out.RevokedProfiles = slices.Clone(ids)
	l.base.aclMu.Lock()
	if l.base.restoreRevocations == nil {
		l.base.restoreRevocations = map[string][]int64{}
	}
	l.base.restoreRevocations[record] = slices.Clone(ids)
	l.base.aclMu.Unlock()
	// Only the captured original handle may close here; connection publication is
	// locked for every Consul profile until all physical executors have settled.
	return out, err
}
func (l *kvHTTPManagementLease) lastHTTPArchiveRestore() contract.KVHTTPArchiveRestoreResult {
	f, ok := l.base.raw.(contract.KVHTTPArchiveRestoring)
	if !ok {
		return contract.KVHTTPArchiveRestoreResult{}
	}
	r := f.LastHTTPArchiveRestore().Clone()
	l.base.translateRestore(&r)
	return r
}
func (l *kvRequestLease) translateRestore(r *contract.KVHTTPArchiveRestoreResult) bool {
	if r == nil || r.LocalRecordID == "" {
		return r == nil || !r.Attempted
	}
	raw := l.scope.Clone()
	raw.Generation = l.session.raw.Generation()
	l.aclMu.Lock()
	basis, ok := l.managementRecords[r.LocalRecordID]
	r.RevokedProfiles = slices.Clone(l.restoreRevocations[r.LocalRecordID])
	l.aclMu.Unlock()
	if ok {
		raw = basis.raw
	}
	if !sameKVACLScope(r.Scope.Original, raw) {
		return false
	}
	if ok {
		r.Scope.Original = basis.public.Clone()
	} else {
		r.Scope.Original = l.scope.Clone()
	}
	return true
}
func (m *Manager) beginHTTPRestore(original *session, raw contract.KVRequestLease) ([]int64, func(), error) {
	coordinator, ok := original.raw.(contract.KVHTTPArchiveRestoreQuiescing)
	if !ok {
		return nil, nil, contract.ErrUnsupported
	}
	m.mu.Lock()
	if m.closed || m.consulRestoreActive || m.sessions[original.id] != original {
		m.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	m.consulRestoreActive = true
	ids := []int64{original.id}
	var captured []*session
	for id, s := range m.sessions {
		if s.kind == connections.Consul && s != original {
			ids = append(ids, id)
			captured = append(captured, s)
			delete(m.sessions, id)
		}
	}
	for id, p := range m.pending {
		if p.profile.Kind == connections.Consul {
			p.cancel()
			delete(m.pending, id)
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	finish := func() {
		m.mu.Lock()
		if m.sessions[original.id] == original {
			delete(m.sessions, original.id)
		}
		m.consulRevocations = append(m.consulRevocations, ids...)
		m.consulRestoreActive = false
		m.mu.Unlock()
	}
	var err error
	for _, s := range captured {
		err = errors.Join(err, s.Close())
	}
	err = errors.Join(err, coordinator.QuiesceHTTPArchiveRestore(raw))
	if err != nil {
		_ = original.Close()
		finish()
		return nil, nil, err
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	return ids, finish, nil
}

// ConsumeHTTPArchiveRevocations is called by the host UI thread, independently
// of whether the original document's private completion is parked.
func (m *Manager) ConsumeHTTPArchiveRevocations() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := slices.Clone(m.consulRevocations)
	m.consulRevocations = nil
	slices.Sort(ids)
	return slices.Compact(ids)
}
