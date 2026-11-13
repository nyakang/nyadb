package dbruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

type kvHTTPSessionDestroyLease struct{ *kvHTTPSessionKeysLease }
type kvDestroyRecordScope struct{ raw, public contract.KVScope }

type kvHTTPSessionDestroyPreparation struct {
	owner     *kvHTTPSessionDestroyLease
	authority *kvHTTPSessionDestroyPreparation
	raw       contract.KVHTTPSessionDestroyPreparation
	request   contract.KVRequest
	recordID  string
	input     contract.KVHTTPSessionDestroyImpactRequest
	mu        sync.Mutex
	preview   contract.KVHTTPSessionDestroyImpact
	closed    bool
}

func bindKVHTTPSessionDestroy(bound contract.KVRequestLease) contract.KVRequestLease {
	keys, ok := bound.(*kvHTTPSessionKeysLease)
	if !ok {
		return bound
	}
	_, impact := keys.raw.(contract.KVHTTPSessionDestroyImpactBrowsing)
	_, planning := keys.raw.(contract.KVHTTPSessionDestroyPlanning)
	if !impact || !planning {
		return bound
	}
	return &kvHTTPSessionDestroyLease{keys}
}

func equalKVDestroyScope(a, b contract.KVScope) bool {
	return a.Namespace == b.Namespace && a.Generation == b.Generation && bytes.Equal(a.Prefix, b.Prefix) && a.Header == b.Header && a.HTTP == b.HTTP && a.Version == b.Version
}

func translateKVDestroyImpact(o *contract.KVHTTPSessionDestroyImpact, basis kvDestroyRecordScope) bool {
	if !equalKVDestroyScope(o.Scope, basis.raw) {
		o.Complete = false
		return false
	}
	ok := true
	// A missing second read has no scope to translate. Never manufacture one.
	if o.Keys.Request != (contract.KVHTTPSessionKeysRequest{}) {
		if o.Keys.Request != (contract.KVHTTPSessionKeysRequest{ID: o.Request.ID, MaxRows: o.Request.MaxRows, MaxBytes: o.Request.MaxBytes}) {
			o.Keys.Complete, o.Keys.ProjectionComplete, o.Complete = false, false, false
			return false
		}
		if equalKVDestroyScope(o.Keys.Scope, basis.raw) {
			o.Keys.Scope.Generation = basis.public.Generation
		} else {
			o.Keys.Complete, o.Keys.ProjectionComplete, o.Complete = false, false, false
			ok = false
		}
	}
	o.Scope.Generation = basis.public.Generation
	return ok
}

func (l *kvRequestLease) currentDestroyRecordScope() kvDestroyRecordScope {
	raw := l.scope.Clone()
	raw.Generation = l.session.raw.Generation()
	return kvDestroyRecordScope{raw: raw, public: l.scope.Clone()}
}

// Local record identities preserve original publication ownership across an
// evidence-only recovery. They are not server identities or plan authority.
func (l *kvRequestLease) translateDestroyResult(r *contract.KVResult) bool {
	e := r.HTTPSessionDestroy
	if e == nil {
		return true
	}
	if e.LocalRecordID == "" || len(e.LocalRecordID) > 128 {
		e.Preview.Complete, e.Recheck.Complete, e.Revalidated = false, false, false
		return false
	}
	l.destroyMu.Lock()
	basis, ok := l.destroyRecords[e.LocalRecordID]
	l.destroyMu.Unlock()
	if !ok {
		basis = l.currentDestroyRecordScope()
	}
	ok = translateKVDestroyImpact(&e.Preview, basis)
	if e.Recheck.Request != (contract.KVHTTPSessionDestroyImpactRequest{}) {
		ok = translateKVDestroyImpact(&e.Recheck, basis) && ok
	}
	if !ok {
		e.Revalidated = false
		return false
	}
	l.destroyMu.Lock()
	if l.destroyClosed {
		l.destroyMu.Unlock()
		return true
	}
	if l.destroyRecords == nil {
		l.destroyRecords = make(map[string]kvDestroyRecordScope)
	}
	// The original document retains one last receipt and one independent
	// unknown write. A third original write cannot follow an unknown receipt.
	if len(l.destroyRecords) >= 2 {
		for id := range l.destroyRecords {
			if id != e.LocalRecordID {
				delete(l.destroyRecords, id)
			}
		}
	}
	l.destroyRecords[e.LocalRecordID] = basis
	l.destroyMu.Unlock()
	return true
}

func (l *kvRequestLease) inheritDestroyRecords(old *kvRequestLease) {
	old.destroyMu.Lock()
	records := make(map[string]kvDestroyRecordScope, len(old.destroyRecords))
	for id, basis := range old.destroyRecords {
		records[id] = kvDestroyRecordScope{raw: basis.raw.Clone(), public: basis.public.Clone()}
	}
	old.destroyMu.Unlock()
	l.destroyMu.Lock()
	l.destroyRecords = records
	l.destroyMu.Unlock()
}

func (l *kvHTTPSessionDestroyLease) ObserveHTTPSessionDestroyImpact(ctx context.Context, request contract.KVHTTPSessionDestroyImpactRequest) (contract.KVHTTPSessionDestroyImpact, error) {
	if err := l.CheckKVLease(); err != nil {
		return contract.KVHTTPSessionDestroyImpact{Scope: l.Scope(), Request: request}, err
	}
	o, err := l.raw.(contract.KVHTTPSessionDestroyImpactBrowsing).ObserveHTTPSessionDestroyImpact(ctx, request)
	o = o.Clone()
	if o.Request != request || !translateKVDestroyImpact(&o, l.currentDestroyRecordScope()) {
		o.Complete = false
		err = errors.Join(err, contract.ErrRevoked)
	}
	return o, errors.Join(err, l.CheckKVLease())
}

func (l *kvHTTPSessionDestroyLease) PrepareHTTPSessionDestroy(ctx context.Context, request contract.KVHTTPSessionDestroyImpactRequest) (contract.KVHTTPSessionDestroyPreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	raw, err := l.raw.(contract.KVHTTPSessionDestroyPlanning).PrepareHTTPSessionDestroy(ctx, request)
	if err != nil {
		if raw != nil {
			err = errors.Join(err, raw.Close())
		}
		return nil, err
	}
	if raw == nil {
		return nil, contract.ErrUnsupported
	}
	recordID := raw.LocalRecordID()
	if recordID == "" || len(recordID) > 128 {
		return nil, errors.Join(contract.ErrUnsupported, raw.Close())
	}
	snapshot := raw.Snapshot()
	if !validDestroyPreparationRequest(snapshot, request.ID) {
		return nil, errors.Join(contract.ErrRevoked, raw.Close())
	}
	preview := raw.Impact().Clone()
	if preview.Request != request || !preview.Complete || !raw.Writes() || !translateKVDestroyImpact(&preview, l.currentDestroyRecordScope()) {
		return nil, errors.Join(contract.ErrRevoked, raw.Close())
	}
	if err = l.CheckKVLease(); err != nil {
		return nil, errors.Join(err, raw.Close())
	}
	p := &kvHTTPSessionDestroyPreparation{owner: l, raw: raw, request: snapshot, input: request, recordID: recordID, preview: preview}
	p.authority = p
	l.destroyMu.Lock()
	if l.destroyPlans == nil {
		l.destroyPlans = make(map[*kvHTTPSessionDestroyPreparation]bool)
	}
	l.destroyPlans[p] = true
	l.destroyMu.Unlock()
	if err = l.CheckKVLease(); err != nil {
		return nil, errors.Join(err, p.Close())
	}
	return p, nil
}

func (p *kvHTTPSessionDestroyPreparation) Snapshot() contract.KVRequest { return p.request }
func (p *kvHTTPSessionDestroyPreparation) LocalRecordID() string        { return p.recordID }
func (*kvHTTPSessionDestroyPreparation) Writes() bool                   { return true }
func (p *kvHTTPSessionDestroyPreparation) Impact() contract.KVHTTPSessionDestroyImpact {
	if p == nil || p.authority != p {
		return contract.KVHTTPSessionDestroyImpact{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.preview.Clone()
}
func (p *kvHTTPSessionDestroyPreparation) Close() error {
	if p == nil || p.authority != p {
		return contract.ErrKVPlanUsed
	}
	err := p.raw.Close()
	p.owner.destroyMu.Lock()
	delete(p.owner.destroyPlans, p)
	p.owner.destroyMu.Unlock()
	p.mu.Lock()
	p.closed = true
	p.preview = contract.KVHTTPSessionDestroyImpact{}
	p.mu.Unlock()
	return err
}

func (l *kvHTTPSessionDestroyLease) Execute(ctx context.Context, prepared contract.KVPreparation) (contract.KVResult, error) {
	p, ok := prepared.(*kvHTTPSessionDestroyPreparation)
	if !ok {
		return l.kvRequestLease.Execute(ctx, prepared)
	}
	if p == nil || p.owner != l || p.authority != p {
		return contract.KVResult{}, contract.ErrKVPlanUsed
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return contract.KVResult{Request: p.request}, contract.ErrKVPlanUsed
	}
	if err := l.CheckKVLease(); err != nil {
		preview := p.Impact()
		return contract.KVResult{Request: p.request, HTTPSessionDestroy: &contract.KVHTTPSessionDestroyEvidence{LocalRecordID: p.recordID, Preview: preview}}, errors.Join(err, p.Close())
	}
	r, err := l.raw.Execute(ctx, p.raw)
	r = r.Clone()
	if r.Request != p.request || r.HTTPSessionDestroy != nil && (r.HTTPSessionDestroy.LocalRecordID != p.recordID || r.HTTPSessionDestroy.Preview.Request != p.input || r.HTTPSessionDestroy.Recheck.Request != (contract.KVHTTPSessionDestroyImpactRequest{}) && r.HTTPSessionDestroy.Recheck.Request != p.input) {
		err = errors.Join(err, contract.ErrRevoked)
	}
	if !l.translateDestroyResult(&r) {
		err = errors.Join(err, contract.ErrRevoked)
	}
	if !errors.Is(err, contract.ErrKVBusy) {
		err = errors.Join(err, p.Close())
	}
	return r, errors.Join(err, l.CheckKVLease())
}

var _ contract.KVHTTPSessionDestroyImpactBrowsing = (*kvHTTPSessionDestroyLease)(nil)
var _ contract.KVHTTPSessionDestroyPlanning = (*kvHTTPSessionDestroyLease)(nil)
var _ contract.KVHTTPSessionDestroyPreparation = (*kvHTTPSessionDestroyPreparation)(nil)

func (l *kvRequestLease) closeDestroyPlans() {
	l.destroyMu.Lock()
	plans := make([]*kvHTTPSessionDestroyPreparation, 0, len(l.destroyPlans))
	for p := range l.destroyPlans {
		plans = append(plans, p)
	}
	l.destroyPlans = nil
	l.destroyMu.Unlock()
	for _, p := range plans {
		p.Close()
	}
}

func validDestroyPreparationRequest(request contract.KVRequest, id string) bool {
	if request.Method != contract.KVConsulHTTPRequestMethod {
		return false
	}
	var fields map[string]json.RawMessage
	if jsondocument.Decode([]byte(request.JSON), &fields, 4096) != nil || len(fields) != 2 {
		return false
	}
	var method, path string
	return json.Unmarshal(fields["method"], &method) == nil && json.Unmarshal(fields["path"], &path) == nil && method == "PUT" && path == "/v1/session/destroy/"+id
}
