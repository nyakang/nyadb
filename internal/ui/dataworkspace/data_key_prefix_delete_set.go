package dataworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

// Capture ownership is published separately from UI completion. Closing or
// canceling a document revokes late original handles without touching a new one.
type dataPrefixSetTicket struct {
	mu      sync.Mutex
	revoked bool
	set     contract.DataKeyPrefixDeleteSet
}

func (t *dataPrefixSetTicket) publish(set contract.DataKeyPrefixDeleteSet) {
	t.mu.Lock()
	if t.revoked {
		t.mu.Unlock()
		if set != nil {
			_ = set.Close()
		}
		return
	}
	t.set = set
	t.mu.Unlock()
}
func (t *dataPrefixSetTicket) take() contract.DataKeyPrefixDeleteSet {
	t.mu.Lock()
	defer t.mu.Unlock()
	set := t.set
	t.set = nil
	return set
}
func (t *dataPrefixSetTicket) revoke() {
	t.mu.Lock()
	t.revoked = true
	set := t.set
	t.set = nil
	t.mu.Unlock()
	if set != nil {
		_ = set.Close()
	}
}

type dataPrefixSetCompletion struct {
	result   mysqlEditResult
	approved contract.DataMutationSnapshot
	snapshot contract.DataKeyPrefixDeleteSetSnapshot
}
type dataPrefixBatchReceipt struct {
	Index, Offset, Count int
	Outcome              string
	NativeJSON           string // Complete actual receipt; empty means no receipt was obtained.
}
type dataPrefixSetLedger struct {
	Request                                 contract.DataKeyPrefixDelete
	Candidates, BatchSize, ConfirmedBatches int
	Stopped, Uncertain                      bool
	Batches                                 []dataPrefixBatchReceipt
}
type dataKeyPrefixDeleteSetState struct {
	keysJSON                           string
	object                             contract.ObjectRef
	runtime                            *contract.DataRuntime
	generation, requestID              uint64
	prefix                             string
	request                            contract.DataKeyPrefixDelete
	inputOpen, reviewOpen, confirmOpen bool
	stopping, uncertain                bool
	cancel                             context.CancelFunc
	captureTicket                      *dataPrefixSetTicket
	planTicket                         *dataMutationTicket
	captureResults, previewResults     <-chan error
	results                            <-chan dataPrefixSetCompletion
	set                                contract.DataKeyPrefixDeleteSet
	initial, current                   contract.DataKeyPrefixDeleteSetSnapshot
	plan                               contract.DataMutationPlan
	preview                            contract.DataMutationSnapshot
	capture                            func(context.Context, int64, string, string, contract.DataKeyPrefixDelete) (contract.DataKeyPrefixDeleteSet, error)
	submit                             func(context.Context, int64, string, string, contract.DataMutationPlan) (*contract.DataEditResult, error)
}

func (m *dataKeyPrefixDeleteSetState) pending() bool {
	return m.captureResults != nil || m.previewResults != nil || m.results != nil
}
func (s *State) canDeleteKeyPrefixSet(d *Document) bool {
	return d != nil && !d.closed && d.page != nil && d.profileID > 0 && d.object.ProfileID == d.profileID && d.editable() && d.capabilities.KeyValues && !d.Busy() && !d.HasDraft() && d.edit.Uncertain == nil && !d.transfer.Uncertain && !dataTransactionUnavailable(d) && !dataTransactionActive(d) && s.canUseSession(d) && s.session().ProfileID == d.profileID && s.runtimeFor(d).CaptureKeyPrefixDelete != nil && s.runtimeFor(d).SubmitEdits != nil
}

// DeleteKeyPrefixSet opens a form without I/O. No later batch starts by itself.
func (s *State) DeleteKeyPrefixSet(d *Document) {
	if !s.canDeleteKeyPrefixSet(d) {
		return
	}
	s.finishMySQLCellEdit(d, false)
	r := s.runtimeFor(d)
	d.prefixSet = &dataKeyPrefixDeleteSetState{object: d.object, runtime: d.boundRuntime, generation: s.sessionFor(d).Generation, inputOpen: true, capture: r.CaptureKeyPrefixDelete, submit: r.SubmitEdits}
	d.edit.Busy, d.edit.Err = true, ""
}
func (s *State) validKeyPrefixSet(d *Document, m *dataKeyPrefixDeleteSetState) bool {
	return m != nil && !d.closed && s.documents[d.id] == d && d.prefixSet == m && d.object == m.object && d.profileID == m.object.ProfileID && d.boundRuntime == m.runtime && s.canUseSession(d) && s.sessionFor(d).Generation == m.generation && m.capture != nil && m.submit != nil && len(d.edit.Drafts) == 0 && d.edit.Editor == nil
}
func validPrefixSetCapture(request contract.DataKeyPrefixDelete, snapshot contract.DataKeyPrefixDeleteSetSnapshot) bool {
	if snapshot.Request != request || snapshot.NativeJSON == "" || len(snapshot.Candidates) == 0 || snapshot.BatchSize <= 0 || snapshot.BatchSize > contract.DataMaxPageSize || snapshot.NextBatch < 0 || snapshot.NextBatch > (len(snapshot.Candidates)+snapshot.BatchSize-1)/snapshot.BatchSize {
		return false
	}
	seen := map[string]bool{}
	for _, row := range snapshot.Candidates {
		if row == nil || row.Ref.Object != request.Object || len(row.Ref.TypedValues) != 1 || len(row.Ref.Columns) != 1 || row.Ref.Columns[0] == "" || len(row.Ref.Values) != 1 {
			return false
		}
		key := row.Ref.TypedValues[0]
		if key.Kind != contract.Text || key.Truncated || key.Text == "" || !utf8.ValidString(key.Text) || strings.ContainsRune(key.Text, 0) || !strings.HasPrefix(key.Text, request.Prefix) || seen[key.Text] {
			return false
		}
		seen[key.Text] = true
	}
	return true
}
func samePrefixSetCapture(initial, current contract.DataKeyPrefixDeleteSetSnapshot) bool {
	return validPrefixSetCapture(initial.Request, current) && current.Request == initial.Request && current.NativeJSON == initial.NativeJSON && current.BatchSize == initial.BatchSize && reflect.DeepEqual(current.Candidates, initial.Candidates)
}
func (s *State) captureKeyPrefixSet(services ui.Services, d *Document) {
	m := d.prefixSet
	if !s.validKeyPrefixSet(d, m) || !m.inputOpen || !validKeyPrefixInput(m.prefix) || m.pending() || m.set != nil {
		return
	}
	m.request = contract.DataKeyPrefixDelete{Object: m.object, Prefix: m.prefix}
	d.edit.Request++
	m.requestID = d.edit.Request
	m.inputOpen = false
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ticket := &dataPrefixSetTicket{}
	m.captureTicket = ticket
	out := make(chan error, 1)
	m.captureResults = out
	capture, id, db, table, request := m.capture, d.profileID, d.database, d.table, m.request
	go func() {
		set, err := capture(ctx, id, db, table, request)
		ticket.publish(set)
		if err == nil {
			err = ctx.Err()
		}
		out <- err
		services.Invalidate()
	}()
}
func validPrefixSetBatch(m *dataKeyPrefixDeleteSetState, snapshot contract.DataMutationSnapshot) bool {
	if !validKeyPrefixSnapshot(m.request, snapshot) || snapshot.KeyPrefixDeleteBatch == nil {
		return false
	}
	index := m.current.NextBatch
	start := index * m.initial.BatchSize
	end := min(start+m.initial.BatchSize, len(m.initial.Candidates))
	if start < 0 || start >= end {
		return false
	}
	batch := contract.DataKeyPrefixDeleteBatch{Request: m.request, Index: index, Offset: start, Count: end - start, Total: len(m.initial.Candidates)}
	if *snapshot.KeyPrefixDeleteBatch != batch || len(snapshot.Edits) != batch.Count {
		return false
	}
	for i, e := range snapshot.Edits {
		if !reflect.DeepEqual(e.Original, m.initial.Candidates[start+i]) {
			return false
		}
	}
	return true
}
func (s *State) previewKeyPrefixSetBatch(services ui.Services, d *Document) {
	m := d.prefixSet
	if !s.validKeyPrefixSet(d, m) || m.set == nil || m.pending() || m.plan != nil || m.uncertain || m.current.Uncertain || m.current.Stopped || d.loading {
		return
	}
	current := m.set.Snapshot()
	if !samePrefixSetCapture(m.initial, current) || current.InFlight || current.Uncertain || current.Stopped || current.NextBatch != m.current.NextBatch || !reflect.DeepEqual(current.Receipts, m.current.Receipts) {
		d.edit.Err = locales.Text("原候选集合已失效，禁止继续提交")
		return
	}
	if current.NextBatch >= (len(current.Candidates)+current.BatchSize-1)/current.BatchSize {
		return
	}
	d.edit.Request++
	m.requestID = d.edit.Request
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ticket := &dataMutationTicket{}
	m.planTicket = ticket
	out := make(chan error, 1)
	m.previewResults = out
	m.reviewOpen, m.confirmOpen = false, false
	set, index := m.set, current.NextBatch
	go func() {
		plan, err := set.PreviewBatch(ctx, index)
		ticket.publish(plan)
		if err == nil {
			err = ctx.Err()
		}
		out <- err
		services.Invalidate()
	}()
}
func (s *State) confirmKeyPrefixSetBatch(services ui.Services, d *Document) {
	m := d.prefixSet
	if !s.validKeyPrefixSet(d, m) || m.pending() || m.uncertain || !m.confirmOpen || m.plan == nil || d.edit.Request != m.requestID || !validPrefixSetBatch(m, m.preview) || !reflect.DeepEqual(m.plan.Snapshot(), m.preview) {
		return
	}
	current := m.set.Snapshot()
	if !samePrefixSetCapture(m.initial, current) || current.InFlight || current.Uncertain || current.Stopped || current.NextBatch != m.current.NextBatch || !reflect.DeepEqual(current.Receipts, m.current.Receipts) {
		d.edit.Err = locales.Text("原候选集合已失效，禁止继续提交")
		return
	}
	snapshot := contract.CloneDataMutationSnapshot(m.preview)
	plan, submit, set := m.plan, m.submit, m.set
	m.plan = nil
	m.confirmOpen = false
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	d.edit.Request++
	m.requestID = d.edit.Request
	id, db, table, gen, request := d.profileID, d.database, d.table, m.generation, m.requestID
	out := make(chan dataPrefixSetCompletion, 1)
	m.results = out
	history := mysqlDataWriteHistoryRequest(db, table, snapshot.Edits, false)
	for i := range history {
		history[i].SQL, history[i].Summary = locales.Text("已确认的前缀候选键分批删除"), true
	}
	complete := s.beginActivity(d, "data_write", dataActivitySQL(history))
	go func() {
		started := time.Now()
		saved, err := submit(ctx, id, db, table, plan)
		_ = plan.Close()
		state := contract.CloneDataKeyPrefixDeleteSetSnapshot(set.Snapshot())
		saved = cloneEditResult(saved)
		contract.CompleteActivity(complete, dataMutationActivityOutcome(err, snapshot.Object, snapshot.Edits, saved))
		for i := range history {
			history[i].At, history[i].Duration = time.Now(), time.Since(started)
		}
		out <- dataPrefixSetCompletion{approved: snapshot, result: mysqlEditResult{History: history, Generation: gen, Request: request, Profile: id, MutationEdits: snapshot.Edits, Saved: saved, Err: err}, snapshot: state}
		services.Invalidate()
	}()
}
func (s *State) updateKeyPrefixSetLedger(d *Document, m *dataKeyPrefixDeleteSetState) {
	d.prefixSetProgress.Request = m.request
	d.prefixSetProgress.Candidates, d.prefixSetProgress.BatchSize = len(m.initial.Candidates), m.initial.BatchSize
	d.prefixSetProgress.ConfirmedBatches = m.current.NextBatch
	d.prefixSetProgress.Uncertain = m.uncertain
	raw, err := json.Marshal(d.prefixSetProgress)
	if err != nil {
		d.prefixSetLedger.Err = err.Error()
		return
	}
	d.prefixSetLedger.JSON = string(raw)
}

// Stop keeps independent original observation and receipts; it cannot authorize
// another capture after an unknown write. Pending cancellation awaits UI ACK.
func (s *State) stopKeyPrefixSet(d *Document) {
	m := d.prefixSet
	if m == nil || m.uncertain {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	if m.pending() {
		m.stopping = true
		m.inputOpen, m.reviewOpen, m.confirmOpen = false, false, false
		if m.captureTicket != nil {
			m.captureTicket.revoke()
		}
		if m.planTicket != nil {
			m.planTicket.revoke()
		}
		return
	}
	if m.set != nil {
		d.prefixSetProgress.Stopped = true
		s.updateKeyPrefixSetLedger(d, m)
	}
	s.disposeKeyPrefixSet(d, false)
	d.edit.Busy = false
	s.clearDataMutationContinuation(d)
}
func (s *State) disposeKeyPrefixSet(d *Document, wait bool) {
	m := d.prefixSet
	if m == nil {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	if m.captureTicket != nil {
		m.captureTicket.revoke()
	}
	if m.planTicket != nil {
		m.planTicket.revoke()
	}
	if m.plan != nil {
		_ = m.plan.Close()
		m.plan = nil
	}
	if m.set != nil {
		_ = m.set.Close()
	}
	if wait {
		if m.captureResults != nil {
			<-m.captureResults
		}
		if m.previewResults != nil {
			<-m.previewResults
		}
		if m.results != nil {
			<-m.results
		}
	}
	d.prefixSet = nil
}
func (s *State) consumeKeyPrefixSet(services ui.Services, d *Document) {
	m := d.prefixSet
	if m == nil {
		return
	}
	if m.captureResults != nil {
		select {
		case err := <-m.captureResults:
			m.captureResults = nil
			m.set = m.captureTicket.take()
			if m.cancel != nil {
				m.cancel()
				m.cancel = nil
			}
			if m.stopping {
				s.stopKeyPrefixSet(d)
				return
			}
			if err == nil && (!s.validKeyPrefixSet(d, m) || d.edit.Request != m.requestID) {
				err = contract.ErrRevoked
			}
			if err == nil && m.set == nil {
				err = contract.ErrUnsupported
			}
			if err == nil {
				initial := m.set.Snapshot()
				if !validPrefixSetCapture(m.request, initial) || initial.NextBatch != 0 || initial.InFlight || initial.Uncertain || initial.Stopped || len(initial.Receipts) != 0 {
					err = contract.ErrDataConflict
				} else {
					m.initial, m.current = contract.CloneDataKeyPrefixDeleteSetSnapshot(initial), contract.CloneDataKeyPrefixDeleteSetSnapshot(initial)
					keys := make([]string, len(initial.Candidates))
					for i, row := range initial.Candidates {
						keys[i] = row.Ref.TypedValues[0].Text
					}
					encoded, _ := json.MarshalIndent(keys, "", "  ")
					m.keysJSON = string(encoded)
					d.prefixSetObservation.JSON = initial.NativeJSON
					d.prefixSetProgress = dataPrefixSetLedger{Request: m.request, Candidates: len(initial.Candidates), BatchSize: initial.BatchSize}
					s.updateKeyPrefixSetLedger(d, m)
				}
			}
			if err != nil {
				s.disposeKeyPrefixSet(d, false)
				d.edit.Busy, d.edit.Err = false, err.Error()
				s.clearDataMutationContinuation(d)
			} else {
				m.reviewOpen = true
			}
		default:
		}
		return
	}
	if m.previewResults != nil {
		select {
		case err := <-m.previewResults:
			m.previewResults = nil
			m.plan = m.planTicket.take()
			if m.cancel != nil {
				m.cancel()
				m.cancel = nil
			}
			if m.stopping {
				s.stopKeyPrefixSet(d)
				return
			}
			if err == nil && (!s.validKeyPrefixSet(d, m) || d.edit.Request != m.requestID) {
				err = contract.ErrRevoked
			}
			if err == nil && m.plan == nil {
				err = contract.ErrUnsupported
			}
			if err == nil {
				snapshot := m.plan.Snapshot()
				if !validPrefixSetBatch(m, snapshot) {
					err = contract.ErrDataConflict
				} else {
					m.preview = contract.CloneDataMutationSnapshot(snapshot)
				}
			}
			if err != nil {
				if m.plan != nil {
					_ = m.plan.Close()
					m.plan = nil
				}
				d.edit.Err = err.Error()
				if !s.validKeyPrefixSet(d, m) {
					s.stopKeyPrefixSet(d)
				} else {
					m.reviewOpen = true
				}
			} else {
				m.confirmOpen = true
			}
		default:
		}
		return
	}
	if m.results != nil {
		select {
		case completion := <-m.results:
			m.results = nil
			if m.cancel != nil {
				m.cancel()
				m.cancel = nil
			}
			r, current, approved := completion.result, completion.snapshot, completion.approved
			saved := r.Saved
			original := s.validKeyPrefixSet(d, m) && r.Generation == m.generation && r.Profile == d.profileID && r.Request == d.edit.Request && r.Request == m.requestID && validPrefixSetBatch(m, approved) && reflect.DeepEqual(r.MutationEdits, approved.Edits)
			native := ""
			if saved != nil {
				native = saved.NativeJSON
			}
			receipts := slices.Clone(m.current.Receipts)
			if native != "" {
				receipts = append(receipts, native)
			}
			coherent := samePrefixSetCapture(m.initial, current) && !current.InFlight && reflect.DeepEqual(receipts, current.Receipts)
			complete := original && coherent && r.Err == nil && saved != nil && saved.NativeJSON != "" && saved.CommitConfirmed && !saved.CommitUncertain && !saved.Incomplete && saved.Applied == len(approved.Edits) && dataMutationReceiptComplete(m.object, approved.Edits, saved) && current.NextBatch == m.current.NextBatch+1 && !current.Uncertain && !current.Stopped
			uncertain := !original || !coherent || errors.Is(r.Err, contract.ErrDataCommitUncertain) || saved != nil && (saved.CommitUncertain || saved.Incomplete || saved.CommitConfirmed && !complete) || r.Err == nil && !complete || current.Uncertain || !complete && current.NextBatch != m.current.NextBatch
			historyErr := r.Err
			if historyErr == nil && !complete {
				historyErr = errors.New(locales.Text("提交结果不完整，请核验数据库"))
			}
			s.recordDataWriteHistory(d, r.History, saved, historyErr, !original)
			outcome := "failed"
			if complete {
				outcome = "confirmed"
			} else if uncertain {
				outcome = "unknown"
			}
			if batch := approved.KeyPrefixDeleteBatch; batch != nil {
				d.prefixSetProgress.Batches = append(d.prefixSetProgress.Batches, dataPrefixBatchReceipt{Index: batch.Index, Offset: batch.Offset, Count: batch.Count, Outcome: outcome, NativeJSON: native})
			}
			if complete {
				m.current = contract.CloneDataKeyPrefixDeleteSetSnapshot(current)
			} else {
				m.current.Receipts = receipts
			}
			if uncertain {
				m.uncertain = true
				m.reviewOpen, m.confirmOpen = false, false
				d.edit.Uncertain = cloneEditResult(saved)
				if d.edit.Uncertain == nil {
					d.edit.Uncertain = &contract.DataEditResult{}
				}
				d.edit.Uncertain.CommitUncertain = true
				d.edit.Busy, d.edit.Err = false, locales.Text("提交结果仍不确定，禁止重放；请检查数据库")
				s.updateKeyPrefixSetLedger(d, m)
				d.prefixSetLedger.Open = true
				return
			}
			if current.Stopped {
				m.current.Stopped = true
				d.prefixSetProgress.Stopped = true
			}
			s.updateKeyPrefixSetLedger(d, m)
			if !complete {
				if r.Err != nil {
					d.edit.Err = r.Err.Error()
				}
				if m.stopping || current.Stopped {
					s.stopKeyPrefixSet(d)
					d.prefixSetLedger.Open = true
				} else {
					m.reviewOpen = true
				}
				return
			}
			d.edit.Err = ""
			finished := m.current.NextBatch == (len(m.initial.Candidates)+m.initial.BatchSize-1)/m.initial.BatchSize
			if finished || m.stopping {
				if m.stopping {
					d.prefixSetProgress.Stopped = true
					s.updateKeyPrefixSetLedger(d, m)
				}
				s.disposeKeyPrefixSet(d, false)
				d.edit.Busy = false
				d.prefixSetLedger.Open = true
			} else {
				m.reviewOpen = true
			}
			busy := d.edit.Busy
			d.edit.Busy = false
			s.refreshDataAfterWrite(services, d)
			d.edit.Busy = busy
		default:
		}
		return
	}
	if !m.uncertain && !s.validKeyPrefixSet(d, m) {
		s.stopKeyPrefixSet(d)
		d.edit.Err = locales.Text("原数据库会话已失效，请刷新表数据")
	}
}
func prefixSetProgressText(ledger dataPrefixSetLedger) string {
	batches := 0
	if ledger.BatchSize > 0 {
		batches = (ledger.Candidates + ledger.BatchSize - 1) / ledger.BatchSize
	}
	confirmed, failed, unknown, started := 0, 0, 0, 0
	for _, batch := range ledger.Batches {
		started += batch.Count
		switch batch.Outcome {
		case "confirmed":
			confirmed += batch.Count
		case "failed":
			failed += batch.Count
		case "unknown":
			unknown += batch.Count
		}
	}
	return fmt.Sprintf(locales.Text("已确认 %d/%d 批（%d 键）；失败 %d 键，未知 %d 键，尚未开始 %d 键"), ledger.ConfirmedBatches, batches, confirmed, failed, unknown, max(0, ledger.Candidates-started))
}
