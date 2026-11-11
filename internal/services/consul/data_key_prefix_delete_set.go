package consul

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const dataPrefixSetBudget int64 = 64 << 20

// Reserve the complete serialized request/body/typed receipt before a batch
// reaches the original executor; retain its actual bytes until set closure.
const dataPrefixReceiptBudget int64 = 256 << 20
const dataPrefixSetCandidateLimit = 4096

// All mutable fields use owner.mu. A set owns one complete observation and
// an ordered receipt ledger; it never recaptures a prefix between batches.
type dataKeyPrefixDeletionSet struct {
	owner                     *dataLease
	ctx                       context.Context
	cancel                    context.CancelFunc
	request                   contract.DataKeyPrefixDelete
	original                  contract.KVHTTPResult
	snapshot                  contract.DataKeyPrefixDeleteSetSnapshot
	closed, budget, preparing bool
	plan                      *dataPlan
	receiptBytes              int64
	failure                   error
}

func (s *dataKeyPrefixDeletionSet) Snapshot() contract.DataKeyPrefixDeleteSetSnapshot {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	if s.closed || s.owner.closed || s.owner.prefixSet != s {
		return contract.DataKeyPrefixDeleteSetSnapshot{}
	}
	return contract.CloneDataKeyPrefixDeleteSetSnapshot(s.snapshot)
}

func (s *dataKeyPrefixDeletionSet) closeLocked() {
	if s.closed {
		return
	}
	s.closed = true
	s.cancel()
	if s.plan != nil {
		s.plan.releaseLocked()
	}
	s.plan = nil
	s.original = contract.KVHTTPResult{}
	s.snapshot = contract.DataKeyPrefixDeleteSetSnapshot{}
	if s.owner.prefixSet == s {
		s.owner.prefixSet = nil
	}
	if s.budget {
		receiptBudgetMu.Lock()
		s.owner.session.reserved -= dataPrefixSetBudget + s.receiptBytes
		receiptBudgetMu.Unlock()
		s.budget = false
		s.receiptBytes = 0
	}
}
func (s *dataKeyPrefixDeletionSet) Close() error {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	s.closeLocked()
	return nil
}

func (l *dataLease) captureKeyPrefixDelete(parent context.Context, id int64, catalog, name string, request contract.DataKeyPrefixDelete) (contract.DataKeyPrefixDeleteSet, error) {
	if request.Object != l.object || !l.validKeyPrefixDeletion(request.Prefix) {
		return nil, ErrInvalid
	}
	if err := l.check(id, catalog, name); err != nil {
		return nil, err
	}
	if !l.writeMu.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer l.writeMu.Unlock()
	ctx, done, err := l.begin(parent)
	if err != nil {
		return nil, err
	}
	defer done()
	l.mu.Lock()
	if l.uncertain {
		l.mu.Unlock()
		return nil, contract.ErrDataCommitUncertain
	}
	if l.pageLost {
		l.mu.Unlock()
		return nil, errDataReadLost
	}
	if l.plan != nil || l.prefixSet != nil {
		l.mu.Unlock()
		return nil, contract.ErrKVBusy
	}
	l.mu.Unlock()
	receiptBudgetMu.Lock()
	if l.session.retained+l.session.reserved+dataPrefixSetBudget+dataPlanBudget+dataPrefixReceiptBudget+pendingReceiptBudget > l.session.budget {
		receiptBudgetMu.Unlock()
		return nil, ErrBounds
	}
	l.session.reserved += dataPrefixSetBudget
	receiptBudgetMu.Unlock()
	transferred := false
	defer func() {
		if !transferred {
			receiptBudgetMu.Lock()
			l.session.reserved -= dataPrefixSetBudget
			receiptBudgetMu.Unlock()
		}
	}()
	native, err := KeyListRequest(request.Prefix, false)
	if err != nil {
		return nil, err
	}
	document, err := DocumentRequest(native)
	if err != nil {
		return nil, err
	}
	preparation, err := l.write.Prepare(document)
	if err != nil {
		return nil, err
	}
	result, err := l.write.Execute(ctx, preparation)
	if err != nil {
		return nil, err
	}
	entries, err := l.entries(result)
	if err != nil {
		return nil, err
	}
	if result.HTTP.Metadata.ACLFiltered != nil && *result.HTTP.Metadata.ACLFiltered {
		return nil, contract.ErrUnsupported
	}
	if len(entries) == 0 {
		return nil, ErrInvalid
	}
	if len(entries) > dataPrefixSetCandidateLimit {
		return nil, ErrBounds
	}
	retained := len(result.HTTP.Body) + len(result.HTTP.Request.Body)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Key, request.Prefix) {
			return nil, ErrProtocol
		}
		if entry.Session != "" {
			return nil, contract.ErrUnsupported
		}
		retained += len(entry.Value)
	}
	if retained > dataKeyPrefixObservationBudget {
		return nil, ErrBounds
	}
	snapshot := contract.DataKeyPrefixDeleteSetSnapshot{Request: request, BatchSize: 64 / 3}
	for _, entry := range entries {
		snapshot.Candidates = append(snapshot.Candidates, l.snapshot(entry, result.HTTP.Metadata))
	}
	raw, err := json.Marshal(struct {
		Object     contract.ObjectRef
		Prefix     string
		Candidates int
		Original   contract.KVHTTPResult
	}{l.object, request.Prefix, len(entries), *result.HTTP})
	if err != nil || len(raw) > dataPreviewLimit {
		return nil, ErrBounds
	}
	snapshot.NativeJSON = string(raw)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if l.closed || !l.session.Alive() {
		return nil, contract.ErrRevoked
	}
	if l.pageLost {
		return nil, errDataReadLost
	}
	setContext, setCancel := context.WithCancel(l.ctx)
	set := &dataKeyPrefixDeletionSet{owner: l, ctx: setContext, cancel: setCancel, request: request, original: result.HTTP.Clone(), snapshot: snapshot, budget: true}
	l.prefixSet, transferred = set, true
	return set, nil
}

func (s *dataKeyPrefixDeletionSet) PreviewBatch(parent context.Context, index int) (contract.DataMutationPlan, error) {
	l := s.owner
	l.mu.Lock()
	if s.closed || l.closed || l.prefixSet != s {
		l.mu.Unlock()
		return nil, contract.ErrRevoked
	}
	if s.snapshot.Uncertain || l.uncertain {
		l.mu.Unlock()
		return nil, contract.ErrDataCommitUncertain
	}
	if s.snapshot.Stopped {
		failure := s.failure
		l.mu.Unlock()
		return nil, failure
	}
	if s.preparing || s.plan != nil || s.snapshot.InFlight {
		l.mu.Unlock()
		return nil, contract.ErrKVBusy
	}
	if index < 0 || index != s.snapshot.NextBatch || index >= (len(s.snapshot.Candidates)+s.snapshot.BatchSize-1)/s.snapshot.BatchSize {
		l.mu.Unlock()
		return nil, ErrInvalid
	}
	start := index * s.snapshot.BatchSize
	end := min(start+s.snapshot.BatchSize, len(s.snapshot.Candidates))
	batch := &contract.DataKeyPrefixDeleteBatch{Request: s.request, Index: index, Offset: start, Count: end - start, Total: len(s.snapshot.Candidates)}
	selected := contract.CloneDataKeyPrefixDeleteSetSnapshot(contract.DataKeyPrefixDeleteSetSnapshot{Candidates: s.snapshot.Candidates[start:end]}).Candidates
	scope := &dataKeyPrefixDeletionScope{Prefix: s.request.Prefix, Candidates: len(s.snapshot.Candidates), Original: s.original.Clone(), Batch: batch, set: s}
	s.preparing = true
	l.mu.Unlock()
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	plan, err := l.previewPreparedScope(ctx, l.object.ProfileID, l.object.Catalog, l.object.Name, scope, func(context.Context) ([]preparedDataEdit, error) {
		edits := make([]contract.DataEdit, len(selected))
		for i, original := range selected {
			edits[i] = contract.DataEdit{Kind: "delete", Original: original}
		}
		return l.prepareDataEdits(edits)
	})
	l.mu.Lock()
	s.preparing = false
	if err == nil && (s.closed || l.prefixSet != s) {
		err = contract.ErrRevoked
	}
	if err != nil && plan != nil {
		plan.(*dataPlan).releaseLocked()
		plan = nil
	}
	if err == nil {
		s.plan = plan.(*dataPlan)
	}
	l.mu.Unlock()
	return plan, err
}

// Called only after the original submit classified its actual executor result.
func (s *dataKeyPrefixDeletionSet) acceptBatchLocked(batch int, count int, out *contract.DataEditResult, failure error) {
	receiptBudgetMu.Lock()
	s.owner.session.reserved -= dataPrefixReceiptBudget
	if !s.closed {
		s.receiptBytes += int64(len(out.NativeJSON))
		s.owner.session.reserved += int64(len(out.NativeJSON))
	}
	receiptBudgetMu.Unlock()
	if s.closed {
		return
	}
	s.snapshot.InFlight = false
	if out.NativeJSON != "" {
		s.snapshot.Receipts = append(s.snapshot.Receipts, out.NativeJSON)
	}
	if out.CommitUncertain || out.Incomplete {
		s.snapshot.Uncertain = true
		return
	}
	if out.CommitConfirmed && out.Applied == count && len(out.Outcomes) == count && batch == s.snapshot.NextBatch {
		s.snapshot.NextBatch++
		return
	}
	s.snapshot.Stopped = true
	s.failure = failure
	if s.failure == nil {
		s.failure = contract.ErrDataConflict
	}
}

var _ contract.DataKeyPrefixDeleteSet = (*dataKeyPrefixDeletionSet)(nil)
