package consul

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"net/url"
	"sync"
	"sync/atomic"
)

type httpArchiveRestorePreparation struct {
	owner          *documentLease
	request        contract.KVHTTPArchiveRestoreRequest
	scope          contract.KVHTTPArchiveScope
	target, record string
	source         contract.KVHTTPArchiveResult
	mu             sync.Mutex
	used, closed   bool
}

func (p *httpArchiveRestorePreparation) Request() contract.KVHTTPArchiveRestoreRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.request.Clone()
}
func (p *httpArchiveRestorePreparation) Scope() contract.KVHTTPArchiveScope { return p.scope.Clone() }
func (p *httpArchiveRestorePreparation) TargetURL() string                  { return p.target }
func (p *httpArchiveRestorePreparation) LocalRecordID() string              { return p.record }
func (p *httpArchiveRestorePreparation) Source() contract.KVHTTPArchiveResult {
	return p.source.Clone()
}
func (p *httpArchiveRestorePreparation) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	clear(p.request.Archive)
	p.request.Archive = nil
	return nil
}
func (l *documentLease) PrepareHTTPArchiveRestore(in contract.KVHTTPArchiveRestoreRequest) (contract.KVHTTPArchiveRestorePreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	if l.s.cfg.ReadOnly || !l.s.cfg.RestoreWrite {
		return nil, ErrUnsupported
	}
	if l.writeUnknown.Load() {
		return nil, contract.ErrKVWriteUnknown
	}
	captured := in.Clone()
	proof, err := inspectSnapshotArchive(context.Background(), captured.Archive, snapshotArchiveLimits{compressed: 64 << 20, expanded: int64(captured.ExpandedBytes)})
	if err != nil {
		clear(captured.Archive)
		return nil, err
	}
	target := l.s.cfg.Endpoint + "/v1/snapshot"
	if l.s.cfg.Datacenter != "" {
		q := url.Values{"dc": {l.s.cfg.Datacenter}}
		target += "?" + q.Encode()
	}
	scope := contract.KVHTTPArchiveScope{Original: l.Scope(), DocumentID: l.document}
	source := contract.KVHTTPArchiveResult{OriginalBytes: uint64(len(captured.Archive)), ExpandedBytes: uint64(proof.expandedBytes), StateBytes: uint64(proof.stateBytes), OriginalSHA256: bytes.Clone(proof.archiveHash[:]), StateSHA256: bytes.Clone(proof.stateHash[:]), Metadata: proof.metadata, Checksums: proof.checksums, Version: proof.version, Index: proof.index, Term: proof.term, SnapshotID: proof.id, SourceComplete: true, GZIPComplete: true, TARComplete: true, ChecksumsMatch: true, MetadataValid: true}
	return &httpArchiveRestorePreparation{owner: l, request: captured, scope: scope, target: target, record: rand.Text(), source: source}, nil
}
func (l *documentLease) ExecuteHTTPArchiveRestore(parent context.Context, prepared contract.KVHTTPArchiveRestorePreparation) (out contract.KVHTTPArchiveRestoreResult, err error) {
	p, ok := prepared.(*httpArchiveRestorePreparation)
	if !ok || p == nil || p.owner != l {
		return out, contract.ErrKVPlanUsed
	}
	if err = parent.Err(); err != nil {
		return out, err
	}
	if !l.ops.TryLock() {
		return out, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	if l.destroyPlan != nil || l.aclMutationPlan != nil || l.enterprisePlan != nil {
		return out, contract.ErrKVBusy
	}
	if err = l.CheckKVLease(); err != nil {
		return out, err
	}
	if l.writeUnknown.Load() {
		return out, contract.ErrKVWriteUnknown
	}
	p.mu.Lock()
	if p.closed || p.used {
		p.mu.Unlock()
		return out, contract.ErrKVPlanUsed
	}
	p.used = true
	input := p.request.Clone()
	p.mu.Unlock()
	defer func() { clear(input.Archive); _ = p.Close() }()
	out = contract.KVHTTPArchiveRestoreResult{Scope: p.Scope(), Source: p.Source(), Request: input.Clone(), TargetURL: p.target, LocalRecordID: p.record}
	if err = l.reserveReceipt(); err != nil {
		return out, err
	}
	defer l.releaseReservation()
	// Raw callers receive the same session draining guarantees as runtime callers.
	if err = l.s.QuiesceHTTPArchiveRestore(l); err != nil {
		return out, err
	}
	ctx, done, err := l.request(parent)
	if err != nil {
		return out, err
	}
	defer done()
	if err = l.connect(ctx); err != nil {
		return out, err
	}
	native := Plan{owner: l.client, request: Request{Method: "PUT", Path: "/v1/snapshot", Body: input.Archive}, kind: "snapshot-restore", write: true, used: &atomic.Bool{}}
	response, err := l.client.Execute(ctx, native)
	response.Request.Body = nil // original archive remains in the separate private request
	result := documentResult(contract.KVRequest{}, response)
	out.HTTP = *result.HTTP
	out.Attempted, out.Ended, out.Completed, out.WriteOutcomeUnknown = response.Attempted, response.Ended, response.Acknowledged, response.WriteOutcomeUnknown
	if out.WriteOutcomeUnknown {
		l.writeUnknown.Store(true)
	}
	if out.Attempted {
		l.storeRestore(out)
		l.s.mu.Lock()
		l.s.revoked = true
		l.s.mu.Unlock()
		l.s.cancel()
	}
	if !l.client.Alive() {
		err = l.markLost(err)
	}
	return out, err
}
func (l *documentLease) LastHTTPArchiveRestore() contract.KVHTTPArchiveRestoreResult {
	l.receiptMu.Lock()
	defer l.receiptMu.Unlock()
	if l.restoreLast == nil {
		return contract.KVHTTPArchiveRestoreResult{}
	}
	return l.restoreLast.Clone()
}
func (l *documentLease) storeRestore(r contract.KVHTTPArchiveRestoreResult) {
	l.receiptMu.Lock()
	defer l.receiptMu.Unlock()
	v := r.Clone()
	l.restoreLast = &v
	if v.WriteOutcomeUnknown {
		l.restoreUnknown = &v
	}
	cost := resultCost(l.last) + aclReadResultCost(l.aclLast) + aclMutationResultCost(l.aclMutationLast) + l.managementCacheCost()
	if l.unknown != nil && l.unknown != l.last {
		cost += resultCost(l.unknown)
	}
	receiptBudgetMu.Lock()
	if l.reserved {
		l.s.reserved -= pendingReceiptBudget
		l.reserved = false
	}
	l.s.retained += cost - l.cacheBytes
	l.cacheBytes = cost
	receiptBudgetMu.Unlock()
}
func restoreCost(r *contract.KVHTTPArchiveRestoreResult) int64 {
	if r == nil {
		return 0
	}
	return int64(len(r.Request.Archive)+len(r.Source.Metadata)+len(r.Source.Checksums)) + resultCost(&contract.KVResult{HTTP: &r.HTTP}) + 2048
}
func (l *documentLease) restoreCacheCost() int64 {
	cost := restoreCost(l.restoreLast)
	if l.restoreUnknown != l.restoreLast {
		cost += restoreCost(l.restoreUnknown)
	}
	return cost
}

func (s *Session) QuiesceHTTPArchiveRestore(except contract.KVRequestLease) error {
	owner, ok := except.(*documentLease)
	if !ok || owner == nil || owner.s != s {
		return contract.ErrKVPlanUsed
	}
	s.mu.Lock()
	if s.closed || s.revoked || s.restoreOwner != nil && s.restoreOwner != owner {
		s.mu.Unlock()
		return contract.ErrRevoked
	}
	s.restoreOwner = owner
	var leases []*documentLease
	for l := range s.leases {
		if l != owner {
			leases = append(leases, l)
		}
	}
	var watches []*httpWatchLease
	for l := range s.watchLeases {
		watches = append(watches, l)
	}
	var renewals []*httpSessionRenewalLease
	for l := range s.renewalLeases {
		renewals = append(renewals, l)
	}
	var data []*dataLease
	for l := range s.dataLeases {
		data = append(data, l)
	}
	s.mu.Unlock()
	var err error
	for _, l := range leases {
		err = errors.Join(err, l.retire())
	}
	for _, l := range watches {
		err = errors.Join(err, l.retire())
	}
	for _, l := range renewals {
		err = errors.Join(err, l.retire())
	}
	for _, l := range data {
		err = errors.Join(err, l.Close())
	}
	return err
}

var _ contract.KVHTTPArchiveRestoring = (*documentLease)(nil)
