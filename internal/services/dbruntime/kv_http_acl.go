package dbruntime

import (
	"bytes"
	"context"
	"errors"
	"reflect"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type kvHTTPACLReadLease struct {
	contract.KVRequestLease
	base *kvRequestLease
}

type kvHTTPACLReadKeysLease struct {
	*kvHTTPACLReadLease
	contract.KVHTTPSessionKeysBrowsing
}

type kvHTTPACLReadDestroyLease struct {
	*kvHTTPACLReadKeysLease
	contract.KVHTTPSessionDestroyImpactBrowsing
	contract.KVHTTPSessionDestroyPlanning
}

type kvACLRecordScope struct{ raw, public contract.KVScope }
type kvACLRecord struct {
	id    string
	basis kvACLRecordScope
}

type kvHTTPACLReadPreparation struct {
	owner  *kvHTTPACLReadLease
	raw    contract.KVHTTPACLReadPreparation
	input  contract.KVHTTPACLReadRequest
	record string
}

func (p *kvHTTPACLReadPreparation) Request() contract.KVHTTPACLReadRequest { return p.input.Clone() }
func (p *kvHTTPACLReadPreparation) LocalRecordID() string                  { return p.record }

func bindKVHTTPACLRead(bound contract.KVRequestLease) contract.KVRequestLease {
	base := originalKVRequestLease(bound)
	if base == nil || base.scope.HTTP.Protocol != "consul-http" {
		return bound
	}
	if _, ok := base.raw.(contract.KVHTTPACLReading); !ok {
		return bound
	}
	acl := &kvHTTPACLReadLease{KVRequestLease: bound, base: base}
	if keys, ok := bound.(contract.KVHTTPSessionKeysBrowsing); ok {
		withKeys := &kvHTTPACLReadKeysLease{acl, keys}
		impact, hasImpact := bound.(contract.KVHTTPSessionDestroyImpactBrowsing)
		planning, hasPlanning := bound.(contract.KVHTTPSessionDestroyPlanning)
		if hasImpact && hasPlanning {
			return &kvHTTPACLReadDestroyLease{withKeys, impact, planning}
		}
		return withKeys
	}
	return acl
}

func sameKVACLScope(a, b contract.KVScope) bool {
	return a.Namespace == b.Namespace && a.Generation == b.Generation && bytes.Equal(a.Prefix, b.Prefix) && a.Header == b.Header && a.HTTP == b.HTTP && a.Version == b.Version
}

func revokeACLProjection(r *contract.KVHTTPACLReadResult) {
	r.Projection = nil
	r.ProjectionError = "ACL projection unavailable; original receipt retained"
}

func (l *kvRequestLease) aclOriginalRecord() kvACLRecord {
	l.aclMu.Lock()
	defer l.aclMu.Unlock()
	return kvACLRecord{l.aclRecordID, kvACLRecordScope{l.aclRecordScope.raw.Clone(), l.aclRecordScope.public.Clone()}}
}

func (l *kvRequestLease) translateACLResult(r *contract.KVHTTPACLReadResult, original kvACLRecord) bool {
	if r.Request.Resource == "" && r.Receipt.HTTP == nil && r.LocalRecordID == "" {
		return true
	}
	if r.LocalRecordID == "" || len(r.LocalRecordID) > 128 {
		revokeACLProjection(r)
		return false
	}
	basis := original.basis
	if original.id != r.LocalRecordID {
		raw := l.scope.Clone()
		raw.Generation = l.session.raw.Generation()
		basis = kvACLRecordScope{raw: raw, public: l.scope.Clone()}
	}
	if !sameKVACLScope(r.Scope, basis.raw) {
		revokeACLProjection(r)
		return false
	}
	r.Scope.Generation = basis.public.Generation
	return true
}

// Each lease inherits at most one receipt. Retain that original basis while
// newer reads use the current publication, including late cached observations.
func (l *kvRequestLease) inheritACLRecord(rawScope contract.KVScope, result *contract.KVHTTPACLReadResult) {
	if result == nil {
		return
	}
	l.aclMu.Lock()
	l.aclRecordID = result.LocalRecordID
	l.aclRecordScope = kvACLRecordScope{raw: rawScope.Clone(), public: result.Scope.Clone()}
	l.aclMu.Unlock()
}

func (l *kvHTTPACLReadLease) PrepareHTTPACLRead(request contract.KVHTTPACLReadRequest) (contract.KVHTTPACLReadPreparation, error) {
	input := request.Clone()
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	raw, err := l.base.raw.(contract.KVHTTPACLReading).PrepareHTTPACLRead(input.Clone())
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, contract.ErrUnsupported
	}
	id := raw.LocalRecordID()
	if id == "" || len(id) > 128 || !reflect.DeepEqual(raw.Request(), input) {
		return nil, contract.ErrRevoked
	}
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	return &kvHTTPACLReadPreparation{owner: l, raw: raw, input: input, record: id}, nil
}

func (l *kvHTTPACLReadLease) ExecuteHTTPACLRead(ctx context.Context, prepared contract.KVHTTPACLReadPreparation) (contract.KVHTTPACLReadResult, error) {
	p, ok := prepared.(*kvHTTPACLReadPreparation)
	if !ok || p == nil || p.owner != l {
		return contract.KVHTTPACLReadResult{}, contract.ErrKVPlanUsed
	}
	if err := l.CheckKVLease(); err != nil {
		return contract.KVHTTPACLReadResult{LocalRecordID: p.record, Scope: l.Scope(), Request: p.input.Clone()}, err
	}
	original := l.base.aclOriginalRecord()
	r, err := l.base.raw.(contract.KVHTTPACLReading).ExecuteHTTPACLRead(ctx, p.raw)
	r = r.Clone()
	if r.LocalRecordID != p.record || !reflect.DeepEqual(r.Request, p.input) {
		revokeACLProjection(&r)
		err = errors.Join(err, contract.ErrRevoked)
	} else if !l.base.translateACLResult(&r, original) {
		err = errors.Join(err, contract.ErrRevoked)
	}
	return r, errors.Join(err, l.CheckKVLease())
}

func (l *kvHTTPACLReadLease) LastHTTPACLRead() contract.KVHTTPACLReadResult {
	original := l.base.aclOriginalRecord()
	r := l.base.raw.(contract.KVHTTPACLReading).LastHTTPACLRead().Clone()
	l.base.translateACLResult(&r, original)
	return r
}

var _ contract.KVHTTPACLReading = (*kvHTTPACLReadLease)(nil)
