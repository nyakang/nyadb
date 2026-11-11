package consul

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// All shared grid writes require the original immutable preview and one-attempt
// submit. The compatibility saver uses the same original validation/executor.
const dataPlanBudget int64 = 256 << 20
const dataPreviewLimit = 64 << 20

type preparedDataEdit struct {
	edit          contract.DataEdit
	before, after contract.KVHTTPEntry
	observation   dataObservation
	rename, copy  bool
}

type dataPlan struct {
	owner          *dataLease
	closed, budget bool // guarded by owner.mu; budget transfers to dispatched worker
	preparation    contract.KVPreparation
	prepared       []preparedDataEdit
	snapshot       contract.DataMutationSnapshot
}

func (p *dataPlan) Snapshot() contract.DataMutationSnapshot {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	if p.closed || p.owner.closed || p.owner.plan != p {
		return contract.DataMutationSnapshot{}
	}
	return contract.CloneDataMutationSnapshot(p.snapshot)
}

func (l *dataLease) releasePlanBudget() {
	receiptBudgetMu.Lock()
	l.session.reserved -= dataPlanBudget
	receiptBudgetMu.Unlock()
}

func (p *dataPlan) releaseLocked() {
	p.closed = true
	p.preparation, p.prepared = nil, nil
	p.snapshot = contract.DataMutationSnapshot{}
	if p.budget {
		p.owner.releasePlanBudget()
		p.budget = false
	}
	if p.owner.plan == p {
		p.owner.plan = nil
	}
}

func (p *dataPlan) Close() error {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	p.releaseLocked()
	return nil
}

func dataHash(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func observationEntry(o dataObservation) (contract.KVHTTPEntry, error) {
	e := contract.KVHTTPEntry{Key: o.Key, Session: o.Session, SessionNull: o.SessionNull,
		PresentFields: slices.Clone(o.PresentFields), ValuePresent: o.ValuePresent, ValueNull: o.ValueNull}
	for i, target := range []*uint64{&e.Flags, &e.CreateIndex, &e.ModifyIndex, &e.LockIndex} {
		text := []string{o.Flags, o.CreateIndex, o.ModifyIndex, o.LockIndex}[i]
		if !exactUint(text) {
			return e, ErrInvalid
		}
		*target, _ = strconv.ParseUint(text, 10, 64)
	}
	if e.CreateIndex == 0 || e.ModifyIndex < e.CreateIndex || !e.ValuePresent ||
		!slices.IsSorted(e.PresentFields) || e.Session != "" && !validID(e.Session) || e.SessionNull && e.Session != "" {
		return e, ErrInvalid
	}
	want := []string{"CreateIndex", "Flags", "Key", "LockIndex", "ModifyIndex", "Value"}
	if slices.Contains(e.PresentFields, "Session") {
		want = append(want, "Session")
		slices.Sort(want)
	}
	if !slices.Equal(e.PresentFields, want) || e.SessionNull && !slices.Contains(e.PresentFields, "Session") {
		return e, ErrInvalid
	}
	return e, nil
}

// Both payload modes must agree with the signed original reference, including
// native NULL/presence. Metadata by itself cannot authorize authored cells.
func (l *dataLease) validateSnapshot(s *contract.RowSnapshot) (dataObservation, error) {
	var o dataObservation
	if s == nil || len(s.Metadata) > 64<<10 || !bytes.Equal([]byte(s.Metadata), firstRefValue(s.Ref)) {
		return o, ErrInvalid
	}
	o, err := l.refObservation(s.Ref)
	if err != nil {
		return o, err
	}
	e, err := observationEntry(o)
	if err != nil {
		return o, err
	}
	want := []string{"key", "value", "flags", "create_index", "modify_index", "lock_index", "session"}
	if !slices.Equal(s.Columns, want) || len(o.ValueHash) != 64 {
		return o, ErrInvalid
	}
	hash, err := hex.DecodeString(o.ValueHash)
	if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != o.ValueHash {
		return o, ErrInvalid
	}
	cells := dataCells(e, false)
	switch s.Mode {
	case "values":
		if len(s.Original) != 7 || len(s.Fingerprints) != 0 {
			return o, ErrInvalid
		}
		for i, c := range s.Original {
			if c.Truncated || len(c.Bytes) > valueLimit || len(c.Text) > 4096 {
				return o, ErrInvalid
			}
			if i == 1 && !cells[i].Null {
				if c.Kind != contract.Binary || c.Text != "" || dataHash(c.Bytes) != o.ValueHash {
					return o, ErrInvalid
				}
			} else if !reflect.DeepEqual(c, *cells[i].Value) {
				return o, ErrInvalid
			}
		}
	case "fingerprints":
		if len(s.Original) != 0 || len(s.Fingerprints) != 7 {
			return o, ErrInvalid
		}
		for i, c := range cells {
			f := s.Fingerprints[i]
			if c.Null {
				if f != nil {
					return o, ErrInvalid
				}
				continue
			}
			h := dataHash([]byte(c.Value.Text))
			if i == 1 {
				h = o.ValueHash
			}
			if f == nil || *f != h {
				return o, ErrInvalid
			}
		}
	default:
		return o, ErrInvalid
	}
	if o.ValueNull && o.ValueHash != dataHash(nil) {
		return o, ErrInvalid
	}
	return o, nil
}

func firstRefValue(r contract.RowRef) []byte {
	if len(r.Values) != 1 {
		return nil
	}
	return r.Values[0]
}

func dataValue(v contract.DataValue, field string) (contract.DataValue, error) {
	if v.Null || v.Default {
		return v, contract.ErrUnsupported
	}
	switch field {
	case "key":
		if v.Kind != contract.Text || v.Binary || len(v.Bytes) != 0 || !validKey(v.Text, false) {
			return v, ErrInvalid
		}
	case "value":
		if v.Kind != contract.Binary || v.Text != "" {
			return v, contract.ErrUnsupported
		}
		if len(v.Bytes) > valueLimit {
			return v, ErrBounds
		}
	case "flags":
		if v.Kind != contract.Integer || v.Binary || len(v.Bytes) != 0 || !exactUint(v.Text) {
			return v, ErrInvalid
		}
	default:
		return v, contract.ErrUnsupported
	}
	return v, nil
}

func (l *dataLease) prepareDataEdits(edits []contract.DataEdit) ([]preparedDataEdit, error) {
	if len(edits) == 0 || len(edits) > 64 {
		return nil, ErrBounds
	}
	prepared := make([]preparedDataEdit, 0, len(edits))
	touched := map[string]bool{}
	ops, size := 0, 0
	for _, e := range edits {
		if e.GraphEndpoints != nil {
			return nil, contract.ErrUnsupported
		}
		if len(e.Values) > 3 {
			return nil, contract.ErrUnsupported
		}
		p := preparedDataEdit{edit: e}
		switch e.Kind {
		case "insert":
			if e.Original != nil {
				return nil, ErrInvalid
			}
			if _, ok := e.Values["key"]; !ok {
				return nil, ErrInvalid
			}
			if _, ok := e.Values["value"]; !ok {
				return nil, ErrInvalid
			}
		case "update", "delete":
			o, err := l.validateSnapshot(e.Original)
			if err != nil {
				return nil, err
			}
			if o.Session != "" {
				return nil, contract.ErrUnsupported
			}
			p.observation = o
			p.before, _ = observationEntry(o)
			p.after = p.before
			if touched[o.Key] {
				return nil, ErrInvalid
			}
			touched[o.Key] = true
			if e.Kind == "delete" && len(e.Values) != 0 || e.Kind == "update" && len(e.Values) == 0 {
				return nil, ErrInvalid
			}
		default:
			return nil, contract.ErrUnsupported
		}
		for field, v := range e.Values {
			if _, err := dataValue(v, field); err != nil {
				return nil, err
			}
			size += len(v.Bytes) + len(v.Text)
			switch field {
			case "key":
				p.after.Key = v.Text
			case "value":
				p.after.Value = v.Bytes
			case "flags":
				p.after.Flags, _ = strconv.ParseUint(v.Text, 10, 64)
			}
		}
		ops += 3 // complete comparison/mutation/receipt, including delete absence
		if e.Kind != "delete" {
			if !validKey(p.after.Key, false) || !strings.HasPrefix(p.after.Key, string(l.session.scope.Prefix)) {
				return nil, ErrInvalid
			}
			p.rename = e.Kind == "update" && p.before.Key != p.after.Key
			if p.rename {
				ops += 3
			}
			if e.Kind == "insert" || p.rename {
				if touched[p.after.Key] {
					return nil, ErrInvalid
				}
				touched[p.after.Key] = true
			}
		}
		if ops > 64 || size > requestLimit {
			return nil, ErrBounds
		}
		// Clone only after validating every dimension of this original edit.
		p.edit = contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{e}}).Edits[0]
		if v, ok := p.edit.Values["value"]; ok {
			p.after.Value = v.Bytes
		}
		prepared = append(prepared, p)
	}
	// Prove encoded native/envelope bounds before any executor I/O, even when
	// omitted values will later be obtained from the original complete before.
	if _, _, err := mutationRequest(prepared); err != nil {
		return nil, err
	}
	return prepared, nil
}

func mutationRequest(prepared []preparedDataEdit) (Request, contract.KVRequest, error) {
	ops := []TxnOperation{}
	add := func(kv TxnKV) { ops = append(ops, TxnOperation{KV: &kv}) }
	for _, p := range prepared {
		index := p.before.ModifyIndex
		if p.edit.Kind != "insert" {
			add(TxnKV{Verb: "check-index", Key: p.before.Key, Index: &index})
		}
		if p.edit.Kind == "insert" || p.rename || p.copy {
			add(TxnKV{Verb: "check-not-exists", Key: p.after.Key})
		}
		if p.edit.Kind != "delete" {
			value := p.after.Value
			if value == nil {
				value = []byte{}
			}
			cas := index
			if p.edit.Kind == "insert" || p.rename || p.copy {
				cas = 0
			}
			flags := p.after.Flags
			add(TxnKV{Verb: "cas", Key: p.after.Key, Value: &value, Flags: &flags, Index: &cas})
		}
		if p.edit.Kind == "delete" || p.rename {
			add(TxnKV{Verb: "delete-cas", Key: p.before.Key, Index: &index})
		}
		if p.edit.Kind != "delete" {
			if p.copy {
				add(TxnKV{Verb: "get", Key: p.before.Key})
			}
			add(TxnKV{Verb: "get", Key: p.after.Key})
		}
		if p.edit.Kind == "delete" || p.rename {
			add(TxnKV{Verb: "check-not-exists", Key: p.before.Key})
		}
	}
	r, err := TxnRequest(ops)
	if err != nil {
		return r, contract.KVRequest{}, err
	}
	d, err := DocumentRequest(r)
	return r, d, err
}

func (l *dataLease) preview(parent context.Context, id int64, catalog, name string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
	return l.previewPrepared(parent, id, catalog, name, func() ([]preparedDataEdit, error) { return l.prepareDataEdits(edits) })
}

func (l *dataLease) previewPrepared(parent context.Context, id int64, catalog, name string, prepare func() ([]preparedDataEdit, error)) (contract.DataMutationPlan, error) {
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
	if l.plan != nil {
		l.plan.releaseLocked()
	}
	if l.uncertain {
		l.mu.Unlock()
		return nil, contract.ErrDataCommitUncertain
	}
	if l.pageLost {
		l.mu.Unlock()
		return nil, errDataReadLost
	}
	l.mu.Unlock()
	receiptBudgetMu.Lock()
	if l.session.retained+l.session.reserved+dataPlanBudget+pendingReceiptBudget > l.session.budget {
		receiptBudgetMu.Unlock()
		return nil, ErrBounds
	}
	l.session.reserved += dataPlanBudget
	receiptBudgetMu.Unlock()
	transferred := false
	defer func() {
		if !transferred {
			l.releasePlanBudget()
		}
	}()
	prepared, err := prepare()
	if err != nil {
		return nil, err
	}
	readOps := []TxnOperation{}
	for _, p := range prepared {
		if p.edit.Kind != "insert" {
			readOps = append(readOps, TxnOperation{KV: &TxnKV{Verb: "get", Key: p.before.Key}})
		}
		if p.edit.Kind == "insert" || p.rename || p.copy {
			readOps = append(readOps, TxnOperation{KV: &TxnKV{Verb: "check-not-exists", Key: p.after.Key}})
		}
	}
	r, err := TxnRequest(readOps)
	if err != nil {
		return nil, err
	}
	d, err := DocumentRequest(r)
	if err != nil {
		return nil, err
	}
	prep, err := l.write.Prepare(d)
	if err != nil {
		return nil, err
	}
	result, err := l.write.Execute(ctx, prep)
	if errors.Is(err, ErrConflict) {
		return nil, errors.Join(dataTransactionConflict(result), err)
	}
	if err != nil {
		return nil, err
	}
	entries, err := l.entries(result)
	if err != nil {
		return nil, err
	}
	byKey := map[string]contract.KVHTTPEntry{}
	for _, entry := range entries {
		byKey[entry.Key] = entry
	}
	before := []contract.KVHTTPEntry{}
	for i := range prepared {
		p := &prepared[i]
		if p.edit.Kind == "insert" {
			continue
		}
		e, ok := byKey[p.before.Key]
		if !ok || !matchesObservation(e, p.observation) {
			return nil, contract.ErrDataConflict
		}
		delete(byKey, p.before.Key)
		p.before = e
		before = append(before, e)
		if _, set := p.edit.Values["value"]; !set {
			p.after.Value = e.Value
		}
	}
	if len(byKey) != 0 {
		return nil, ErrProtocol
	}
	r, d, err = mutationRequest(prepared)
	if err != nil {
		return nil, err
	}
	prep, err = l.write.Prepare(d)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(struct {
		Object       contract.ObjectRef
		Method, Path string
		Operations   json.RawMessage
		BeforeImages []contract.KVHTTPEntry
	}{l.object, r.Method, r.Path, r.Body, before})
	if err != nil || len(raw) > dataPreviewLimit {
		return nil, ErrBounds
	}
	p := &dataPlan{owner: l, preparation: prep, prepared: prepared, snapshot: contract.DataMutationSnapshot{Object: l.object, NativeJSON: string(raw)}}
	for _, e := range prepared {
		p.snapshot.Edits = append(p.snapshot.Edits, e.edit)
		key := e.after.Key
		if e.edit.Kind == "delete" {
			key = e.before.Key
		}
		p.snapshot.Items = append(p.snapshot.Items, contract.DataMutationItem{Kind: e.edit.Kind, EntityID: key})
	}
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
	p.budget, transferred = true, true
	l.plan = p
	return p, nil
}

func matchesObservation(e contract.KVHTTPEntry, o dataObservation) bool {
	expected, err := observationEntry(o)
	if err != nil || dataHash(e.Value) != o.ValueHash {
		return false
	}
	e.Value = nil
	return reflect.DeepEqual(e, expected)
}

func (l *dataLease) submit(parent context.Context, id int64, catalog, name string, supplied contract.DataMutationPlan) (*contract.DataEditResult, error) {
	if err := l.check(id, catalog, name); err != nil {
		return nil, err
	}
	p, ok := supplied.(*dataPlan)
	if !ok || p == nil || p.owner != l {
		return nil, contract.ErrKVPlanUsed
	}
	if !l.writeMu.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer l.writeMu.Unlock()
	if !l.pageMu.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer l.pageMu.Unlock()
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
	if p.closed || l.plan != p || l.closed || l.pageLost {
		l.mu.Unlock()
		return nil, contract.ErrKVPlanUsed
	}
	preparation, prepared := p.preparation, p.prepared
	p.budget = false // worker owns the same reservation until acknowledgement
	p.releaseLocked()
	l.mu.Unlock()
	defer l.releasePlanBudget()
	r, executeErr := l.write.Execute(ctx, preparation)
	out := &contract.DataEditResult{CommitUncertain: r.WriteOutcomeUnknown}
	h := r.HTTP
	// Optional header/cleanup projections remain original receipt warnings.
	// The native ACK and complete transaction body authorize correspondence;
	// missing typed transaction results still cannot establish a commit.
	complete := h != nil && r.Completed && h.Ended && h.Captured && h.EOF && h.Acknowledged
	if complete && h.Status == 409 && h.Txn != nil && !h.Txn.Committed && len(h.Txn.Errors) > 0 && len(h.Txn.Results) == 0 {
		err = dataTransactionConflict(r)
	} else if complete && h.Status == 200 && h.Succeeded && h.Txn != nil && h.Txn.Committed && len(h.Txn.Errors) == 0 {
		out.Outcomes, err = l.dataOutcomes(prepared, h.Txn.Results, h.Metadata)
		if err != nil {
			out.Incomplete = true
		} else {
			out.CommitConfirmed, out.RefreshRequired = true, true
			out.Applied = len(prepared)
		}
	} else if r.Attempted && !(complete && h.Status >= 400 && h.Status < 500 && h.Status != 408 && h.Status != 409) {
		out.CommitUncertain = true
	}
	if out.CommitUncertain || out.Incomplete {
		// Transport ACK alone cannot validate application correspondence. Keep
		// that original ACK unchanged and retain this receipt as an unknown write.
		r.WriteOutcomeUnknown = true
		l.write.writeUnknown.Store(true)
		l.write.ops.Lock()
		l.write.storeOwned(r)
		l.write.ops.Unlock()
		l.mu.Lock()
		l.uncertain = true
		l.mu.Unlock()
		err = errors.Join(err, contract.ErrDataCommitUncertain)
	}
	raw, receiptErr := json.Marshal(r)
	out.NativeJSON = string(raw)
	if out.CommitConfirmed {
		l.mu.Lock()
		l.epoch++
		l.mu.Unlock()
		return out, receiptErr
	}
	return out, errors.Join(executeErr, receiptErr, err)
}

func (l *dataLease) save(ctx context.Context, id int64, catalog, name string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
	plan, err := l.preview(ctx, id, catalog, name, edits)
	if err != nil {
		return nil, err
	}
	defer plan.Close()
	return l.submit(ctx, id, catalog, name, plan)
}

func dataTransactionConflict(r contract.KVResult) error {
	err := contract.ErrDataConflict
	if r.HTTP != nil && r.HTTP.Txn != nil {
		for _, native := range r.HTTP.Txn.Errors {
			err = errors.Join(err, fmt.Errorf("Consul transaction operation %d: %s", native.OpIndex, native.What))
		}
	}
	return err
}

func (l *dataLease) dataOutcomes(prepared []preparedDataEdit, results []contract.KVHTTPEntry, metadata contract.KVHTTPMetadata) ([]contract.DataEditOutcome, error) {
	index := 0
	take := func() (contract.KVHTTPEntry, bool) {
		if index >= len(results) {
			return contract.KVHTTPEntry{}, false
		}
		e := results[index]
		index++
		return e, true
	}
	out := make([]contract.DataEditOutcome, 0, len(prepared))
	var txnIndex uint64
	for _, p := range prepared {
		o := contract.DataEditOutcome{Kind: p.edit.Kind, Original: p.edit.Original}
		if p.edit.Kind != "insert" {
			check, ok := take()
			want := p.before.Clone()
			want.Value = nil
			want.ValuePresent, want.ValueNull = true, true
			if !ok || !reflect.DeepEqual(check, want) {
				return nil, ErrProtocol
			}
		}
		if p.edit.Kind != "delete" {
			projected, ok := take()
			if !ok {
				return nil, ErrProtocol
			}
			if p.copy {
				source, ok := take()
				if !ok || !reflect.DeepEqual(source, p.before) {
					return nil, ErrProtocol
				}
			}
			saved, ok := take()
			if !ok {
				return nil, ErrProtocol
			}
			if !validSavedEntry(saved, p) {
				return nil, ErrProtocol
			}
			want := saved.Clone()
			want.Value = nil
			want.ValuePresent, want.ValueNull = true, true
			if !reflect.DeepEqual(projected, want) {
				return nil, ErrProtocol
			}
			if txnIndex != 0 && saved.ModifyIndex != txnIndex {
				return nil, ErrProtocol
			}
			txnIndex = saved.ModifyIndex
			o.Saved = l.snapshot(saved, metadata)
			o.Previews = dataCells(saved, true)
			for _, c := range dataColumns() {
				o.PreviewColumns = append(o.PreviewColumns, c.Name)
			}
		}
		out = append(out, o)
	}
	if index != len(results) {
		return nil, ErrProtocol
	}
	return out, nil
}

func validSavedEntry(e contract.KVHTTPEntry, p preparedDataEdit) bool {
	if e.Key != p.after.Key || !e.ValuePresent || !bytes.Equal(e.Value, p.after.Value) || e.ValueNull && len(p.after.Value) != 0 ||
		e.Flags != p.after.Flags || e.Session != "" || e.SessionNull || e.LockIndex != 0 || e.CreateIndex == 0 || e.ModifyIndex < e.CreateIndex {
		return false
	}
	if !slices.Equal(e.PresentFields, []string{"CreateIndex", "Flags", "Key", "LockIndex", "ModifyIndex", "Value"}) {
		return false
	}
	if p.edit.Kind == "insert" || p.rename || p.copy {
		return e.CreateIndex == e.ModifyIndex && (p.edit.Kind == "insert" || e.ModifyIndex > p.before.ModifyIndex)
	}
	return e.CreateIndex == p.before.CreateIndex && e.ModifyIndex > p.before.ModifyIndex
}

var _ contract.DataMutationPlan = (*dataPlan)(nil)
