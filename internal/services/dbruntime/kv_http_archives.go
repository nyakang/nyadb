package dbruntime

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type kvHTTPArchivesLease struct {
	contract.KVRequestLease
	base *kvRequestLease
}
type kvHTTPArchivesKeysLease struct {
	*kvHTTPArchivesLease
	contract.KVHTTPSessionKeysBrowsing
}
type kvHTTPArchivesDestroyLease struct {
	*kvHTTPArchivesKeysLease
	contract.KVHTTPSessionDestroyImpactBrowsing
	contract.KVHTTPSessionDestroyPlanning
}
type kvHTTPArchivesACLLease struct {
	*kvHTTPArchivesLease
	contract.KVHTTPACLReading
}
type kvHTTPArchivesACLKeysLease struct {
	*kvHTTPArchivesKeysLease
	contract.KVHTTPACLReading
}
type kvHTTPArchivesACLDestroyLease struct {
	*kvHTTPArchivesDestroyLease
	contract.KVHTTPACLReading
}

// Preserve existing optional Consul facets without advertising Etcd snapshots
// or creating a public product/URI/workspace entry.
func bindKVHTTPArchives(bound contract.KVRequestLease) contract.KVRequestLease {
	return bindKVHTTPArchiveMutationFacets(bound, bindKVHTTPArchiveReadFacets(bound))
}

func bindKVHTTPArchiveReadFacets(bound contract.KVRequestLease) contract.KVRequestLease {
	base := originalKVRequestLease(bound)
	if base == nil || base.scope.HTTP.Protocol != "consul-http" {
		return bound
	}
	if _, ok := base.raw.(contract.KVHTTPArchiveCapturing); !ok {
		return bound
	}
	archive := &kvHTTPArchivesLease{KVRequestLease: bound, base: base}
	acl, hasACL := bound.(contract.KVHTTPACLReading)
	if keys, ok := bound.(contract.KVHTTPSessionKeysBrowsing); ok {
		withKeys := &kvHTTPArchivesKeysLease{archive, keys}
		impact, hasImpact := bound.(contract.KVHTTPSessionDestroyImpactBrowsing)
		planning, hasPlanning := bound.(contract.KVHTTPSessionDestroyPlanning)
		if hasImpact && hasPlanning {
			withDestroy := &kvHTTPArchivesDestroyLease{withKeys, impact, planning}
			if hasACL {
				return &kvHTTPArchivesACLDestroyLease{withDestroy, acl}
			}
			return withDestroy
		}
		if hasACL {
			return &kvHTTPArchivesACLKeysLease{withKeys, acl}
		}
		return withKeys
	}
	if hasACL {
		return &kvHTTPArchivesACLLease{archive, acl}
	}
	return archive
}

type kvHTTPArchivesMutatingLease struct {
	*kvHTTPArchivesACLLease
	contract.KVHTTPACLMutating
}
type kvHTTPArchivesMutatingKeysLease struct {
	*kvHTTPArchivesACLKeysLease
	contract.KVHTTPACLMutating
}
type kvHTTPArchivesMutatingDestroyLease struct {
	*kvHTTPArchivesACLDestroyLease
	contract.KVHTTPACLMutating
}
type kvHTTPArchivesImpactLease struct {
	*kvHTTPArchivesMutatingLease
	contract.KVHTTPACLImpactBrowsing
}
type kvHTTPArchivesImpactKeysLease struct {
	*kvHTTPArchivesMutatingKeysLease
	contract.KVHTTPACLImpactBrowsing
}
type kvHTTPArchivesImpactDestroyLease struct {
	*kvHTTPArchivesMutatingDestroyLease
	contract.KVHTTPACLImpactBrowsing
}
type kvHTTPArchivesPlannedLease struct {
	*kvHTTPArchivesImpactLease
	contract.KVHTTPACLMutationPlanning
}
type kvHTTPArchivesPlannedKeysLease struct {
	*kvHTTPArchivesImpactKeysLease
	contract.KVHTTPACLMutationPlanning
}
type kvHTTPArchivesPlannedDestroyLease struct {
	*kvHTTPArchivesImpactDestroyLease
	contract.KVHTTPACLMutationPlanning
}

// Preserve each existing ACL capability only when the original bound lease
// advertised it. Planning does not imply mutation or impact support.
func bindKVHTTPArchiveMutationFacets(original, bound contract.KVRequestLease) contract.KVRequestLease {
	if mutating, ok := original.(contract.KVHTTPACLMutating); ok {
		switch l := bound.(type) {
		case *kvHTTPArchivesACLLease:
			bound = &kvHTTPArchivesMutatingLease{l, mutating}
		case *kvHTTPArchivesACLKeysLease:
			bound = &kvHTTPArchivesMutatingKeysLease{l, mutating}
		case *kvHTTPArchivesACLDestroyLease:
			bound = &kvHTTPArchivesMutatingDestroyLease{l, mutating}
		}
	}
	if impact, ok := original.(contract.KVHTTPACLImpactBrowsing); ok {
		switch l := bound.(type) {
		case *kvHTTPArchivesMutatingLease:
			bound = &kvHTTPArchivesImpactLease{l, impact}
		case *kvHTTPArchivesMutatingKeysLease:
			bound = &kvHTTPArchivesImpactKeysLease{l, impact}
		case *kvHTTPArchivesMutatingDestroyLease:
			bound = &kvHTTPArchivesImpactDestroyLease{l, impact}
		}
	}
	if planning, ok := original.(contract.KVHTTPACLMutationPlanning); ok {
		switch l := bound.(type) {
		case *kvHTTPArchivesImpactLease:
			bound = &kvHTTPArchivesPlannedLease{l, planning}
		case *kvHTTPArchivesImpactKeysLease:
			bound = &kvHTTPArchivesPlannedKeysLease{l, planning}
		case *kvHTTPArchivesImpactDestroyLease:
			bound = &kvHTTPArchivesPlannedDestroyLease{l, planning}
		}
	}
	return bound
}

type kvHTTPArchivePreparation struct {
	owner                 *kvHTTPArchivesLease
	raw                   contract.KVHTTPArchivePreparation
	request               contract.KVHTTPArchiveRequest
	rawScope, publicScope contract.KVHTTPArchiveScope
	target, id            string
}

func (p *kvHTTPArchivePreparation) Snapshot() contract.KVHTTPArchiveRequest { return p.request }
func (p *kvHTTPArchivePreparation) Scope() contract.KVHTTPArchiveScope      { return p.publicScope.Clone() }
func (p *kvHTTPArchivePreparation) TargetURL() string                       { return p.target }
func (p *kvHTTPArchivePreparation) LocalRecordID() string                   { return p.id }

func sameHTTPArchiveScope(a, b contract.KVHTTPArchiveScope) bool {
	return a.DocumentID == b.DocumentID && sameKVACLScope(a.Original, b.Original)
}

func (l *kvHTTPArchivesLease) PrepareHTTPArchive(request contract.KVHTTPArchiveRequest) (contract.KVHTTPArchivePreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	raw, err := l.base.raw.(contract.KVHTTPArchiveCapturing).PrepareHTTPArchive(request)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, contract.ErrUnsupported
	}
	expected := contract.KVHTTPArchiveScope{Original: l.base.scope.Clone(), DocumentID: l.base.document}
	public := expected.Clone()
	expected.Original.Generation = l.base.session.raw.Generation()
	id, target := raw.LocalRecordID(), raw.TargetURL()
	if raw.Snapshot() != request || !sameHTTPArchiveScope(raw.Scope(), expected) || id == "" || len(id) > 128 || target == "" {
		return nil, contract.ErrRevoked
	}
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	return &kvHTTPArchivePreparation{owner: l, raw: raw, request: request, rawScope: expected, publicScope: public, target: target, id: id}, nil
}

func (p *kvHTTPArchivePreparation) originalResult(r contract.KVHTTPArchiveResult) bool {
	return r.Request == p.request && sameHTTPArchiveScope(r.Scope, p.rawScope) && r.TargetURL == p.target && r.LocalRecordID == p.id
}

func (l *kvHTTPArchivesLease) CaptureHTTPArchive(ctx context.Context, prepared contract.KVHTTPArchivePreparation) (contract.KVHTTPArchive, contract.KVHTTPArchiveResult, error) {
	p, ok := prepared.(*kvHTTPArchivePreparation)
	if !ok || p == nil || p.owner != l {
		return nil, contract.KVHTTPArchiveResult{}, contract.ErrKVPlanUsed
	}
	if err := l.CheckKVLease(); err != nil {
		return nil, contract.KVHTTPArchiveResult{Scope: p.Scope(), Request: p.request, TargetURL: p.target, LocalRecordID: p.id}, err
	}
	if p.raw.Snapshot() != p.request || !sameHTTPArchiveScope(p.raw.Scope(), p.rawScope) || p.raw.TargetURL() != p.target || p.raw.LocalRecordID() != p.id {
		return nil, contract.KVHTTPArchiveResult{}, contract.ErrRevoked
	}
	resource, result, err := l.base.raw.(contract.KVHTTPArchiveCapturing).CaptureHTTPArchive(ctx, p.raw)
	result = result.Clone()
	valid := p.originalResult(result)
	if resource != nil && !reflect.DeepEqual(resource.Receipt(), result) {
		valid = false
	}
	if !valid {
		// Preserve private original bytes while rejecting authority/integrity.
		// Mismatched request identity and generation are never rewritten.
		result.SourceComplete = false
		result.LocalError = "original HTTP archive identity or receipt mismatch"
		err = errors.Join(err, contract.ErrRevoked)
	} else {
		result.Scope = p.publicScope.Clone()
	}
	if resource == nil {
		return nil, result, err
	}
	return &kvHTTPArchive{raw: resource, result: result.Clone()}, result, err
}

// Capture the original publication once. Receipt inspection and readers stay
// local after disconnection, recovery or session replacement.
type kvHTTPArchive struct {
	mu     sync.Mutex
	raw    contract.KVHTTPArchive
	result contract.KVHTTPArchiveResult
	closed bool
}

func (a *kvHTTPArchive) Receipt() contract.KVHTTPArchiveResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.result.Clone()
}
func (a *kvHTTPArchive) OpenOriginal() (io.ReadCloser, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, contract.ErrRevoked
	}
	return a.raw.OpenOriginal()
}
func (a *kvHTTPArchive) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	clear(a.result.Metadata)
	clear(a.result.Checksums)
	a.result = contract.KVHTTPArchiveResult{}
	err := a.raw.Close()
	a.raw = nil
	return err
}

var _ contract.KVHTTPArchiveCapturing = (*kvHTTPArchivesLease)(nil)
var _ contract.KVHTTPArchive = (*kvHTTPArchive)(nil)
