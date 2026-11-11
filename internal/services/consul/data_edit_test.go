package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func dataBinary(raw []byte) contract.DataValue {
	return contract.DataValue{Kind: contract.Binary, Binary: true, Bytes: raw}
}
func dataText(text string) contract.DataValue {
	return contract.DataValue{Kind: contract.Text, Text: text}
}

func dataEditSnapshot(t *testing.T, l *dataLease, mode string) *contract.RowSnapshot {
	t.Helper()
	raw, _ := json.Marshal(dataTestEntry("scope/key", []byte{0, 255}))
	e, err := decodeEntry(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := l.snapshot(neutralEntry(e), contract.KVHTTPMetadata{})
	if mode == "values" {
		s.Mode = "values"
		s.Fingerprints = nil
		for _, c := range dataCells(neutralEntry(e), false) {
			s.Original = append(s.Original, c.Value.Clone())
		}
	}
	return s
}

func TestDataMutationBothPayloadModesValidateBeforeIOAndOwnBytes(t *testing.T) {
	f := &dataFixture{}
	l := dataTestLease(t, dataTestSession(t, "scope", f))
	for _, mode := range []string{"values", "fingerprints"} {
		t.Run(mode, func(t *testing.T) {
			s := dataEditSnapshot(t, l, mode)
			edit := contract.DataEdit{Kind: "update", Original: s, Values: map[string]contract.DataValue{"value": dataBinary([]byte{255, 0}), "flags": {Kind: contract.Integer, Text: strconv.FormatUint(math.MaxUint64, 10)}}}
			p, err := l.prepareDataEdits([]contract.DataEdit{edit})
			if err != nil || len(p) != 1 || p[0].after.Flags != math.MaxUint64 {
				t.Fatal(err)
			}
			edit.Values["value"].Bytes[0] = 1
			s.Ref.Values[0][0] = '!'
			if p[0].after.Value[0] != 255 || p[0].edit.Original.Ref.Values[0][0] == '!' {
				t.Fatal("prepared inputs share mutable caller bytes")
			}
		})
	}
	if f.reads.Load() != 0 {
		t.Fatal("pure validation made I/O")
	}
}

func TestDataMutationRejectsMalformedSnapshotsLockedRowsFieldsOverlapAndBoundsBeforeIO(t *testing.T) {
	f := &dataFixture{}
	l := dataTestLease(t, dataTestSession(t, "scope", f))
	for _, mode := range []string{"values", "fingerprints"} {
		mutators := []func(*contract.RowSnapshot){
			func(s *contract.RowSnapshot) { s.Metadata = "{}" },
			func(s *contract.RowSnapshot) { s.Ref.Object.ProfileID++ },
			func(s *contract.RowSnapshot) { s.Columns[1] = "other" },
			func(s *contract.RowSnapshot) {
				if mode == "values" {
					s.Original[1].Truncated = true
				} else {
					*s.Fingerprints[1] = "bad"
				}
			},
			func(s *contract.RowSnapshot) {
				if mode == "values" {
					s.Original[2].Text = "01"
				} else {
					s.Fingerprints[2] = nil
				}
			},
			func(s *contract.RowSnapshot) {
				if mode == "values" {
					s.Original[6] = contract.Cell{Kind: contract.Text}
				} else {
					v := dataHash(nil)
					s.Fingerprints[6] = &v
				}
			},
		}
		for i, mutate := range mutators {
			s := dataEditSnapshot(t, l, mode)
			mutate(s)
			if _, err := l.prepareDataEdits([]contract.DataEdit{{Kind: "delete", Original: s}}); err == nil {
				t.Fatal("malformed original authorized", mode, i)
			}
		}
	}
	s := dataEditSnapshot(t, l, "fingerprints")
	var o dataObservation
	if l.decode(s.Ref.Values[0], &o) != nil {
		t.Fatal("observation missing")
	}
	o.Session = testAgentID
	o.SessionNull = false
	s.Ref.Values[0] = l.encode(o)
	s.Metadata = string(s.Ref.Values[0])
	h := dataHash([]byte(o.Session))
	s.Fingerprints[6] = &h
	if _, err := l.prepareDataEdits([]contract.DataEdit{{Kind: "delete", Original: s}}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("original locked row allowed", err)
	}
	for _, field := range []string{"create_index", "modify_index", "lock_index", "session", "unknown"} {
		if _, err := l.prepareDataEdits([]contract.DataEdit{{Kind: "update", Original: dataEditSnapshot(t, l, "values"), Values: map[string]contract.DataValue{field: dataText("x")}}}); err == nil {
			t.Fatal("generated or unknown field allowed", field)
		}
	}
	for _, v := range []contract.DataValue{{Kind: contract.Binary, Null: true}, {Kind: contract.Binary, Default: true}, dataBinary(bytes.Repeat([]byte{1}, valueLimit+1))} {
		if _, err := l.prepareDataEdits([]contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText("scope/new"), "value": v}}}); err == nil {
			t.Fatal("native value bounds/default/null ignored")
		}
	}
	insert := contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText("scope/key"), "value": dataBinary(nil)}}
	delete := contract.DataEdit{Kind: "delete", Original: dataEditSnapshot(t, l, "values")}
	for _, batch := range [][]contract.DataEdit{{insert, insert}, {insert, delete}, {delete, insert}} {
		if _, err := l.prepareDataEdits(batch); err == nil {
			t.Fatal("overlapping native keys allowed")
		}
	}
	batch := []contract.DataEdit{}
	for i := 0; i < 22; i++ {
		batch = append(batch, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText("scope/" + strconv.Itoa(i)), "value": dataBinary(nil)}})
	}
	if _, err := l.prepareDataEdits(batch); !errors.Is(err, ErrBounds) {
		t.Fatal("64-op bound ignored", err)
	}
	batch = batch[:10]
	for i := range batch {
		batch[i].Values["value"] = dataBinary(bytes.Repeat([]byte{255}, valueLimit))
	}
	if _, err := l.prepareDataEdits(batch); !errors.Is(err, ErrBounds) {
		t.Fatal("base64 envelope expansion bound ignored", err)
	}
	if f.reads.Load() != 0 {
		t.Fatal("invalid edit made I/O")
	}
}

func TestDataMutationPlanBudgetReplacementCloseAndPurePublicReadOnlyBoundary(t *testing.T) {
	f := &dataFixture{}
	f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/v1/txn" {
			return false
		}
		w.Write([]byte(`{"Results":null,"Errors":null}`))
		return true
	}
	s := dataTestSession(t, "scope", f)
	l := dataTestLease(t, s)
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText("scope/new"), "value": dataBinary(nil)}}}
	p, err := l.preview(context.Background(), 11, "kv", "keys", edits)
	if err != nil {
		t.Fatal(err)
	}
	copy := p.Snapshot()
	copy.Edits[0].Values["value"] = dataBinary([]byte{1})
	copy.Items[0].EntityID = "foreign"
	if p.Snapshot().Items[0].EntityID != "scope/new" {
		t.Fatal("inspection changed authorization")
	}
	if _, err := l.preview(context.Background(), 11, "kv", "keys", nil); err == nil || p.Snapshot().Object != (contract.ObjectRef{}) {
		t.Fatal("replacement did not revoke preview")
	}
	p, err = l.preview(context.Background(), 11, "kv", "keys", edits)
	if err != nil {
		t.Fatal(err)
	}
	r := l.Runtime()
	if r.PreviewEdits != nil || r.SubmitEdits != nil || r.Saver != nil || r.Capabilities.EditableTables {
		t.Fatal("private foundation published prematurely")
	}
	l.Close()
	if p.Snapshot().Object != (contract.ObjectRef{}) || l.write.last != nil || l.write.client != nil {
		t.Fatal("close retained plan or executor")
	}
	receiptBudgetMu.Lock()
	reserved := s.reserved
	receiptBudgetMu.Unlock()
	if reserved != 0 {
		t.Fatal("plan budget leaked", reserved)
	}
}

func TestDataMutationSuccessfulIncompleteApplicationReceiptRetainsOriginalACKAndUnknownBarrier(t *testing.T) {
	f := &dataFixture{}
	f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/v1/txn" {
			return false
		}
		w.Write([]byte(`{"Results":null,"Errors":null}`))
		return true
	}
	l := dataTestLease(t, dataTestSession(t, "scope", f))
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText("scope/new"), "value": dataBinary(nil)}}}
	p, err := l.preview(context.Background(), 11, "kv", "keys", edits)
	if err != nil {
		t.Fatal(err)
	}
	out, err := l.submit(context.Background(), 11, "kv", "keys", p)
	if !errors.Is(err, contract.ErrDataCommitUncertain) || out.CommitConfirmed || !out.Incomplete || !l.uncertain {
		t.Fatal("unverified HTTP200 accepted", out, err)
	}
	r := l.write.recoveryOwned()
	if r.Unknown == nil || !r.Unknown.HTTP.Acknowledged || !r.Unknown.HTTP.Succeeded || !r.Unknown.WriteOutcomeUnknown {
		t.Fatal("application unknown replaced original transport ACK")
	}
	before := f.reads.Load()
	if _, err := l.submit(context.Background(), 11, "kv", "keys", p); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("old write replayed", err)
	}
	if _, err := l.preview(context.Background(), 11, "kv", "keys", edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("new write crossed unknown barrier", err)
	}
	if _, err := l.count(context.Background(), 11, "kv", "keys"); err != nil {
		t.Fatal("unknown blocked independent auxiliary read", err)
	}
	if f.reads.Load() != before+1 || !reflect.DeepEqual(l.write.recoveryOwned().Unknown, r.Unknown) {
		t.Fatal("read changed original unknown write")
	}
}

func TestTxnConflictErrorsRequireOriginalUniqueExplicitOperationIndices(t *testing.T) {
	for _, raw := range []string{`{"Results":null,"Errors":[{"What":"conflict"}]}`, `{"Results":null,"Errors":[{"OpIndex":null,"What":"conflict"}]}`, `{"Results":null,"Errors":[{"OpIndex":0,"What":"a"},{"OpIndex":0,"What":"b"}]}`} {
		if _, err := decodeTxn([]byte(raw), 409, 3); err == nil {
			t.Fatal("invalid conflict acknowledged", raw)
		}
	}
}

func TestDataMutationPlanBudgetRejectsBeforeIOAndPageLossDoesNotEraseUnknown(t *testing.T) {
	f := &dataFixture{}
	s := dataTestSession(t, "scope", f)
	l := dataTestLease(t, s)
	receiptBudgetMu.Lock()
	s.budget = s.retained + s.reserved + dataPlanBudget + pendingReceiptBudget - 1
	receiptBudgetMu.Unlock()
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": dataText("scope/new"), "value": dataBinary(nil)}}}
	if _, err := l.preview(context.Background(), 11, "kv", "keys", edits); !errors.Is(err, ErrBounds) || f.reads.Load() != 0 {
		t.Fatal("plan budget made I/O or admitted excess memory", err)
	}
	l.mu.Lock()
	l.uncertain, l.pageLost = true, true
	l.mu.Unlock()
	if _, err := l.preview(context.Background(), 11, "kv", "keys", edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("read failure hid unknown write barrier", err)
	}
	receiptBudgetMu.Lock()
	reserved := s.reserved
	receiptBudgetMu.Unlock()
	if reserved != 0 {
		t.Fatal("rejected plan budget leaked", reserved)
	}
}
