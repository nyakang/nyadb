package dataworkspace

import (
	"bytes"
	"context"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

type copyUIFixture struct {
	mu                sync.Mutex
	requests          []contract.DataKeyCopy
	plans             []*mutationTestPlan
	ordinary, submits atomic.Int32
	fault             string
}

func copyUIWorkspace(t *testing.T, mode string) (*State, *Document, *Session, *copyUIFixture) {
	t.Helper()
	active := Session{ProfileID: 19, Connected: true, Generation: 7}
	original := active
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session {
		if id == original.ProfileID {
			return original
		}
		return active
	}})
	o := contract.ObjectRef{ProfileID: 19, Catalog: "kv", Kind: "keyspace", Name: "keys"}
	p := &contract.DataPage{Object: o, Database: "kv", Table: "keys", Columns: []string{"key", "value", "flags", "create_index", "modify_index", "lock_index", "session"}, ReadOnlyRows: map[int]bool{1: true}}
	for i, name := range p.Columns {
		p.ColumnMetadata = append(p.ColumnMetadata, contract.StructureColumn{Name: name, DataType: "text", Generated: i >= 3})
	}
	for _, key := range []string{"scope/source//猫", "scope/locked"} {
		value := bytes.Repeat([]byte{0, 255}, (512<<10)/2)
		row := []contract.DataCell{mysqlDataValuePreview(contract.DataValue{Kind: contract.Text, Text: key}), {Text: "0x00ff…", Truncated: true, Value: &contract.Cell{Kind: contract.Binary, Bytes: []byte{0, 255}, Truncated: true}}, mysqlDataValuePreview(contract.DataValue{Kind: contract.Integer, Text: "18446744073709551615"}), {Text: "40"}, {Text: "41"}, {Text: "0"}, {Null: true}}
		ref := contract.RowRef{Object: o, Database: "kv", Table: "keys", Columns: []string{"key"}, Values: [][]byte{[]byte(key)}, TypedValues: []contract.Cell{{Kind: contract.Text, Text: key}}}
		fp := "original-source-proof"
		original := &contract.RowSnapshot{Mode: mode, Ref: ref, Columns: slices.Clone(p.Columns), Metadata: "complete-original-source", Fingerprints: []*string{&fp}}
		if mode == "values" {
			original.Fingerprints = nil
			original.Original = []contract.Cell{{Kind: contract.Text, Text: key}, {Kind: contract.Binary, Bytes: value}}
		}
		p.Rows = append(p.Rows, row)
		p.RowRefs = append(p.RowRefs, &original.Ref)
		p.RowSnapshots = append(p.RowSnapshots, original)
	}
	d := &Document{id: 2, profileID: 19, generation: 7, database: "kv", table: "keys", object: o, page: p, selectedRow: 0, hiddenColumns: map[string]bool{}, capabilities: contract.Capabilities{EditableTables: true, KeyValues: true}, edit: mysqlDataEditState{Batch: true}}
	f := &copyUIFixture{}
	s.documents[d.id] = d
	d.boundRuntime = &Runtime{NativeReceipts: true, Capabilities: d.capabilities, EditKinds: []string{"insert", "update", "delete", "copy"}, PreviewEditKinds: []string{"insert", "update", "delete", "copy"}}
	d.boundRuntime.PreviewEdits = func(context.Context, int64, string, string, []contract.DataEdit) (contract.DataMutationPlan, error) {
		f.ordinary.Add(1)
		return nil, contract.ErrUnsupported
	}
	d.boundRuntime.Saver = func(context.Context, int64, string, string, []contract.DataEdit) (*contract.DataEditResult, error) {
		f.ordinary.Add(1)
		return nil, contract.ErrUnsupported
	}
	d.boundRuntime.PreviewKeyCopy = func(_ context.Context, id int64, db, table string, r contract.DataKeyCopy) (contract.DataMutationPlan, error) {
		if id != o.ProfileID || db != o.Catalog || table != o.Name {
			t.Error("copy followed another original object")
		}
		r = contract.CloneDataKeyCopy(r)
		e := contract.DataEdit{Kind: "copy", Original: r.Source, Values: map[string]contract.DataValue{"key": {Kind: contract.Text, Text: r.Destination}}}
		plan := &mutationTestPlan{snapshot: contract.DataMutationSnapshot{Object: o, Edits: []contract.DataEdit{e}, Items: []contract.DataMutationItem{{Kind: "copy", EntityID: r.Destination}}, NativeJSON: `{"complete-source":true,"conditional-destination":true}`}}
		f.mu.Lock()
		f.requests = append(f.requests, r)
		f.plans = append(f.plans, plan)
		f.mu.Unlock()
		return plan, nil
	}
	d.boundRuntime.SubmitEdits = func(_ context.Context, _ int64, _, _ string, plan contract.DataMutationPlan) (*contract.DataEditResult, error) {
		f.submits.Add(1)
		e := plan.Snapshot().Edits[0]
		plan.Close()
		saved := cloneSnapshot(e.Original)
		key := e.Values["key"].Text
		saved.Ref.Values[0] = []byte(key)
		saved.Ref.TypedValues[0].Text = key
		row := clonePreviews(p.Rows[0])
		row[0] = mysqlDataValuePreview(e.Values["key"])
		r := &contract.DataEditResult{CommitConfirmed: true, Applied: 1, NativeJSON: `{"original-copy-receipt":true}`, Outcomes: []contract.DataEditOutcome{{Kind: "copy", Original: cloneSnapshot(e.Original), Saved: saved, PreviewColumns: slices.Clone(p.Columns), Previews: row}}}
		switch f.fault {
		case "missing-target":
			r.Outcomes[0].Saved = nil
		case "foreign-target":
			r.Outcomes[0].Saved.Ref.Object.ProfileID = 99
		case "wrong-source":
			r.Outcomes[0].Original.Metadata = "foreign"
		case "lost":
			r.CommitConfirmed = false
			r.CommitUncertain = true
			return r, contract.ErrDataCommitUncertain
		}
		return r, nil
	}
	t.Cleanup(s.CloseAll)
	return s, d, &active, f
}

func TestDataKeyCopyFormCapturesOriginalSourceBothPayloadsAndNeverUsesOrdinarySaver(t *testing.T) {
	for _, mode := range []string{"values", "fingerprints"} {
		t.Run(mode, func(t *testing.T) {
			s, d, _, f := copyUIWorkspace(t, mode)
			before := captureDraft(d)
			if !s.canCopyDataKey(d, 0) || s.canCopyDataKey(d, 1) {
				t.Fatal("source or locked source admission wrong")
			}
			s.beginDataKeyCopy(d, 0)
			if d.keyCopy == nil || !d.edit.Busy || len(f.requests) != 0 {
				t.Fatal("target form read or wrote the original source")
			}
			d.selectedRow = 1
			d.selection.Clear()
			d.selection.Add(1)
			d.keyCopy.destination = "scope/target//猫"
			s.stageDataKeyCopy(ui.Services{}, d)
			waitMutation(t, s, d)
			if !d.mutation.open || len(d.edit.Drafts) != 1 || len(d.page.Rows) != 3 || d.page.RowRefs[2] != nil || d.page.RowSnapshots[2] != nil || f.ordinary.Load() != 0 || f.submits.Load() != 0 {
				t.Fatal("copy did not require its original preview")
			}
			for row := 0; row < len(d.page.Rows); row++ {
				if mysqlDataCanEdit(d, row, 0) {
					t.Fatal("conditional copy draft allowed unrelated authored changes")
				}
			}
			draft := mysqlDataDraftForRow(d, 2)
			if draft == nil || draft.Kind != "copy" || len(draft.Values) != 1 || !reflect.DeepEqual(draft.Original, before.Originals[0]) || !reflect.DeepEqual(d.page.Rows[:2], before.Rows) || !reflect.DeepEqual(d.page.RowSnapshots[:2], before.Originals) {
				t.Fatal("copy authored preview bytes or changed original source")
			}
			if f.requests[0].Source.Mode != mode || f.requests[0].Destination != "scope/target//猫" || !reflect.DeepEqual(f.requests[0].Source, before.Originals[0]) {
				t.Fatal("selection change redirected original source")
			}
			s.confirmDataMutation(ui.Services{}, d)
			waitMutationSave(t, s, d)
			if d.HasDraft() || d.edit.Uncertain != nil || f.submits.Load() != 1 || d.page.RowRefs[2] == nil || !reflect.DeepEqual(d.page.Rows[:2], before.Rows) || !reflect.DeepEqual(d.page.RowSnapshots[:2], before.Originals) || !mysqlDataCanEdit(d, 2, 0) || !d.page.ReadOnlyRows[1] {
				t.Fatal("confirmed copy changed source or failed to admit saved target")
			}
		})
	}
}

func TestDataKeyCopyCancellationUndoRedoDiscardRetainConditionalSourceAndReadOnlyRows(t *testing.T) {
	s, d, _, f := copyUIWorkspace(t, "values")
	s.beginDataKeyCopy(d, 0)
	d.keyCopy.destination = "scope/target"
	s.stageDataKeyCopy(ui.Services{}, d)
	waitMutation(t, s, d)
	s.cancelDataMutation(d)
	if !d.HasDraft() || f.plans[0].closes.Load() != 1 || f.submits.Load() != 0 {
		t.Fatal("preview cancel discarded copy draft or submitted")
	}
	if !s.undoDraft(d, false) || d.HasDraft() || len(d.page.Rows) != 2 || !d.page.ReadOnlyRows[1] {
		t.Fatal("copy undo changed original rows")
	}
	if !s.undoDraft(d, true) || mysqlDataDraftForRow(d, 2).Kind != "copy" || mysqlDataCanEdit(d, 2, 0) {
		t.Fatal("copy redo became editable insert")
	}
	s.saveMySQLData(ui.Services{}, d)
	waitMutation(t, s, d)
	if len(f.requests) != 2 || f.ordinary.Load() != 0 || !reflect.DeepEqual(f.requests[0], f.requests[1]) {
		t.Fatal("copy retry downgraded original condition")
	}
	s.cancelDataMutation(d)
	s.discardMySQLData(d)
	if d.HasDraft() || len(d.page.Rows) != 2 || len(d.page.RowRefs) != 2 || !d.page.ReadOnlyRows[1] {
		t.Fatal("copy discard retained virtual target or changed locked source")
	}
}

func TestDataKeyCopyFormInvalidSourceGenerationDraftsTargetsAndExplicitClose(t *testing.T) {
	for _, change := range []string{"source", "generation", "dirty", "close", "cancel", "stop", "invalid-target"} {
		t.Run(change, func(t *testing.T) {
			s, d, active, f := copyUIWorkspace(t, "fingerprints")
			s.beginDataKeyCopy(d, 0)
			switch change {
			case "source":
				d.page.RowSnapshots[0].Metadata = "foreign"
			case "generation":
				active.Generation++
				s.deps.SessionFor = func(int64) Session { return *active }
			case "dirty":
				d.edit.Drafts = map[string]*mysqlRowDraft{"foreign": {Kind: "update", Row: 0, Values: map[string]contract.DataValue{"flags": {Kind: contract.Integer, Text: "7"}}}}
			case "close":
				s.Close(d)
			case "cancel":
				s.cancelDataKeyCopy(d)
			case "stop":
				s.stopMySQLDataEdit(d)
			case "invalid-target":
				for _, target := range []string{"", d.keyCopy.sourceKey, "bad\x00key"} {
					d.keyCopy.destination = target
					if validDataKeyCopyDestination(d.keyCopy) {
						t.Fatal("invalid destination admitted")
					}
				}
				s.stageDataKeyCopy(ui.Services{}, d)
				if d.keyCopy == nil || d.HasDraft() {
					t.Fatal("invalid target discarded form or created draft")
				}
				s.cancelDataKeyCopy(d)
			}
			if !d.closed {
				s.consumeDataKeyCopy(d)
			}
			if d.keyCopy != nil || d.edit.Busy || len(f.requests) != 0 || f.submits.Load() != 0 {
				t.Fatal("changed/closed target form retained authority", change)
			}
		})
	}
}

func TestDataKeyCopyMissingOrForeignReceiptsKeepOriginalDraftAndForbidReplay(t *testing.T) {
	for _, fault := range []string{"missing-target", "foreign-target", "wrong-source", "lost"} {
		t.Run(fault, func(t *testing.T) {
			s, d, _, f := copyUIWorkspace(t, "fingerprints")
			f.fault = fault
			s.beginDataKeyCopy(d, 0)
			s.stageDataKeyCopy(ui.Services{}, d)
			waitMutation(t, s, d)
			s.confirmDataMutation(ui.Services{}, d)
			waitMutationSave(t, s, d)
			if d.edit.Uncertain == nil || !d.HasDraft() || d.page.RowRefs[2] != nil || f.submits.Load() != 1 {
				t.Fatal("incomplete copy became an editable saved target")
			}
			s.saveMySQLData(ui.Services{}, d)
			if f.submits.Load() != 1 || len(f.requests) != 1 || mysqlDataCanEdit(d, 0, 0) {
				t.Fatal("unknown copy replayed")
			}
		})
	}
}

func TestDataKeyCopyPreviewParkingLateGenerationAndMissingFeatureNeverDowngrade(t *testing.T) {
	s, d, active, f := copyUIWorkspace(t, "fingerprints")
	entered, release := make(chan struct{}), make(chan struct{})
	preview := d.boundRuntime.PreviewKeyCopy
	d.boundRuntime.PreviewKeyCopy = func(ctx context.Context, id int64, db, table string, r contract.DataKeyCopy) (contract.DataMutationPlan, error) {
		close(entered)
		<-release
		return preview(ctx, id, db, table, r)
	}
	s.beginDataKeyCopy(d, 0)
	s.stageDataKeyCopy(ui.Services{}, d)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("copy preview did not enter")
	}
	active.ProfileID = 99
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.requests)
		f.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("copy did not complete")
		}
		time.Sleep(time.Millisecond)
	}
	s.Consume(ui.Services{})
	if d.mutation.open || d.mutation.results == nil {
		t.Fatal("parked copy preview consumed under another profile")
	}
	active.ProfileID = d.profileID
	s.deps.SessionFor = func(int64) Session { x := *active; x.Generation++; return x }
	waitMutation(t, s, d)
	if d.mutation.open || f.plans[0].closes.Load() != 1 || !d.HasDraft() || f.submits.Load() != 0 {
		t.Fatal("late original copy followed replacement generation")
	}
	s.deps.SessionFor = func(int64) Session { return *active }
	d.boundRuntime.PreviewKeyCopy = nil
	s.saveMySQLData(ui.Services{}, d)
	if f.ordinary.Load() != 0 || d.edit.Busy || d.edit.Err == "" {
		t.Fatal("copy silently downgraded when original feature disappeared")
	}
}

func TestDataKeyCopyToolbarInputEscapeAndLeaveGuard(t *testing.T) {
	for _, escape := range []bool{true, false} {
		t.Run(map[bool]string{true: "escape", false: "preview"}[escape], func(t *testing.T) {
			s, d, _, f := copyUIWorkspace(t, "fingerprints")
			before := captureDraft(d)
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).FillWidth().Children(func() { s.mysqlDataFooter(c, d, d.page) })
				s.Modals(c)
			}, 720, 520)
			tt.Frame()
			if err := tt.Click(locales.Text("复制键到新键")); err != nil {
				t.Fatal(err)
			}
			if d.keyCopy == nil || !d.LeavePending() || !s.GuardLeave(d, func(ui.Services) { t.Fatal("input form left") }) || s.CanSave(d) || len(f.requests) != 0 {
				t.Fatal("toolbar lost original form guard")
			}
			if err := tt.Click(locales.Text("输入目标完整键")); err != nil {
				t.Fatal(err)
			}
			tt.Key(ui.Cmd, ui.KeyA)
			tt.Type("scope/widget-target//猫")
			if d.keyCopy.destination != "scope/widget-target//猫" {
				t.Fatal("native target input not applied", d.keyCopy.destination)
			}
			if escape {
				tt.Key(0, ui.KeyEscape)
				tt.Frame()
				if d.keyCopy != nil || d.edit.Busy || !reflect.DeepEqual(before, captureDraft(d)) || len(f.requests) != 0 {
					t.Fatal("Escape changed source or dispatched")
				}
			} else {
				if err := tt.Click(locales.Text("预览复制")); err != nil {
					t.Fatal(err)
				}
				waitMutation(t, s, d)
				if len(f.requests) != 1 || f.requests[0].Destination != "scope/widget-target//猫" || !d.mutation.open || f.submits.Load() != 0 {
					t.Fatal("preview lost exact input or submitted without confirmation")
				}
			}
		})
	}
}

func TestDataKeyCopyMixedMalformedDraftsRejectedBeforeDispatch(t *testing.T) {
	for _, fault := range []string{"mixed", "nil-source", "extra-value", "null-key", "binary-key", "default-key", "graph"} {
		t.Run(fault, func(t *testing.T) {
			s, d, _, f := copyUIWorkspace(t, "fingerprints")
			e := contract.DataEdit{Kind: "copy", Original: cloneSnapshot(d.page.RowSnapshots[0]), Values: map[string]contract.DataValue{"key": {Kind: contract.Text, Text: "scope/target"}}}
			edits := []contract.DataEdit{e}
			switch fault {
			case "mixed":
				edits = append(edits, contract.DataEdit{Kind: "delete", Original: cloneSnapshot(e.Original)})
			case "nil-source":
				edits[0].Original = nil
			case "extra-value":
				e.Values["flags"] = contract.DataValue{Kind: contract.Integer, Text: "1"}
			case "null-key":
				e.Values["key"] = contract.DataValue{Kind: contract.Text, Null: true}
			case "binary-key":
				e.Values["key"] = contract.DataValue{Kind: contract.Text, Binary: true}
			case "default-key":
				e.Values["key"] = contract.DataValue{Kind: contract.Text, Default: true}
			case "graph":
				edits[0].GraphEndpoints = &contract.DataGraphEndpoints{}
			}
			s.previewDataMutation(ui.Services{}, d, *d.boundRuntime, []string{"copy"}, edits)
			if d.edit.Err == "" || d.edit.Busy || d.mutation.results != nil || len(f.requests) != 0 || f.ordinary.Load() != 0 || f.submits.Load() != 0 {
				t.Fatal("malformed copy reached an executor")
			}
		})
	}
}
