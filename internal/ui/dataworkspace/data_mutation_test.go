package dataworkspace

import (
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type mutationTestPlan struct {
	mu       sync.Mutex
	snapshot contract.DataMutationSnapshot
	closed   bool
	closes   atomic.Int32
}

func (p *mutationTestPlan) Snapshot() contract.DataMutationSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return contract.DataMutationSnapshot{}
	}
	return contract.CloneDataMutationSnapshot(p.snapshot)
}
func (p *mutationTestPlan) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		p.snapshot = contract.DataMutationSnapshot{}
		p.closes.Add(1)
	}
	return nil
}

func mutationWorkspace(t *testing.T) (*State, *Document, *Session) {
	t.Helper()
	s, d, _, _ := editingTestWorkspace(t)
	active := s.session()
	d.generation = active.Generation
	d.object = contract.ObjectRef{ProfileID: d.profileID, Catalog: d.database, Kind: "label", Name: d.table}
	for i := range d.page.RowRefs {
		d.page.RowRefs[i].Object = d.object
		d.page.RowSnapshots[i].Ref.Object = d.object
	}
	d.capabilities = contract.Capabilities{EditableTables: true, Documents: true}
	d.edit.Batch = true
	d.boundRuntime = &Runtime{NativeReceipts: true, EditKinds: []string{"insert", "update", "delete", "detach_delete"}, PreviewEditKinds: []string{"insert", "delete", "detach_delete"}}
	s.deps.Session = func() Session { return active }
	t.Cleanup(s.CloseAll)
	return s, d, &active
}
func mutationPlanFor(d *Document, edits []contract.DataEdit) *mutationTestPlan {
	snapshot := contract.DataMutationSnapshot{Object: d.object, Edits: edits, NativeJSON: `{"original":"完整猫预览","parameters":[],"relationships":[]}`}
	for _, e := range edits {
		snapshot.Items = append(snapshot.Items, contract.DataMutationItem{Kind: e.Kind, EntityID: "original-entity"})
	}
	return &mutationTestPlan{snapshot: contract.CloneDataMutationSnapshot(snapshot)}
}
func waitMutation(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for d.mutation.results != nil && time.Now().Before(deadline) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.mutation.results != nil {
		t.Fatal("original preview acknowledgement stalled")
	}
}
func waitMutationSave(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for d.edit.Results != nil && time.Now().Before(deadline) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.edit.Results != nil {
		t.Fatal("original submission acknowledgement stalled")
	}
}
func stageMutationDelete(s *State, d *Document) { s.deleteMySQLDataRecords(ui.Services{}, d, 0) }

func TestDataMutationRequiresPreviewAndExplicitConfirmationOfOriginalPlan(t *testing.T) {
	s, d, _ := mutationWorkspace(t)
	var plan *mutationTestPlan
	var submits atomic.Int32
	object := d.object
	d.boundRuntime.PreviewEdits = func(_ context.Context, profile int64, database, table string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
		if profile != object.ProfileID || database != object.Catalog || table != object.Name {
			t.Error("preview adopted another identity")
		}
		plan = mutationPlanFor(d, edits)
		return plan, nil
	}
	d.boundRuntime.SubmitEdits = func(_ context.Context, profile int64, database, table string, supplied contract.DataMutationPlan) (*contract.DataEditResult, error) {
		submits.Add(1)
		if supplied != plan || profile != object.ProfileID || database != object.Catalog || table != object.Name {
			t.Error("submit lost original handle or identity")
		}
		snapshot := supplied.Snapshot()
		supplied.Close() // Native plans are empty after dispatch; the UI must own evidence.
		return &contract.DataEditResult{CommitConfirmed: true, Applied: 1, NativeJSON: `{"CommitConfirmed":true}`, Outcomes: []contract.DataEditOutcome{{Kind: snapshot.Edits[0].Kind, Original: snapshot.Edits[0].Original}}}, nil
	}
	stageMutationDelete(s, d)
	before := clonePreviews(d.page.Rows[0])
	s.Save(ui.Services{}, d)
	waitMutation(t, s, d)
	if !d.edit.Busy || !d.LeavePending() || !d.mutation.open || submits.Load() != 0 || s.CanSave(d) || mysqlDataCanEdit(d, 0, 1) || !s.GuardLeave(d, nil) {
		t.Fatal("preview admitted editing, leave or write before confirmation", d.edit.Err)
	}
	captured := d.mutation.snapshot.NativeJSON
	tt := ui.NewTester(func(c *ui.Context) { s.Modals(c) }, 1280, 800)
	tt.Frame()
	if !tt.HasText(locales.Text("确认实体更改")) || !tt.HasText(locales.Text("实体更改完整预览")) {
		t.Fatal("immutable confirmation not presented")
	}
	if err := tt.Click(locales.Text("确认并提交更改")); err != nil {
		t.Fatal(err)
	}
	waitMutationSave(t, s, d)
	if submits.Load() != 1 || len(d.page.Rows) != 2 || d.HasDraft() || d.edit.Uncertain != nil || d.edit.Err != "" || d.mutation.snapshot.NativeJSON != captured || plan.closes.Load() != 1 {
		t.Fatal("confirmed commit did not settle original plan exactly once", d.edit.Err)
	}
	if reflect.DeepEqual(before, d.page.Rows[0]) {
		t.Fatal("confirmed deleted row retained")
	}
	s.Save(ui.Services{}, d)
	if submits.Load() != 1 || d.edit.Busy {
		t.Fatal("confirmed plan automatically replayed")
	}
}

func TestDataMutationCancellationBlockedBatchAndModalDismissalKeepDrafts(t *testing.T) {
	for _, mode := range []string{"button", "blocked", "dismiss", "stop"} {
		t.Run(mode, func(t *testing.T) {
			s, d, _ := mutationWorkspace(t)
			var plan *mutationTestPlan
			var submits atomic.Int32
			d.boundRuntime.PreviewEdits = func(_ context.Context, _ int64, _, _ string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
				plan = mutationPlanFor(d, edits)
				if mode == "blocked" {
					plan.snapshot.Items[0].Blocked = true
					plan.snapshot.Items[0].AffectedRelationships = 2
				}
				return plan, nil
			}
			d.boundRuntime.SubmitEdits = func(context.Context, int64, string, string, contract.DataMutationPlan) (*contract.DataEditResult, error) {
				submits.Add(1)
				return nil, nil
			}
			stageMutationDelete(s, d)
			original := captureDraft(d)
			continued := false
			d.edit.AfterSave = func(ui.Services) { continued = true }
			s.guardDoc = d
			s.Save(ui.Services{}, d)
			waitMutation(t, s, d)
			if mode == "stop" {
				s.stopMySQLDataEdit(d)
			} else if mode == "dismiss" {
				tt := ui.NewTester(func(c *ui.Context) { s.Modals(c) }, 1280, 800)
				tt.Frame()
				tt.Key(0, ui.KeyEscape)
				tt.Frame()
			} else {
				tt := ui.NewTester(func(c *ui.Context) { s.Modals(c) }, 1280, 800)
				tt.Frame()
				if mode == "blocked" {
					if !tt.HasText(locales.Text("普通删除包含有关联关系的节点。请取消后选择删除方式。此批次尚未写入。")) {
						t.Fatal("blocked relationship warning missing")
					}
					s.confirmDataMutation(ui.Services{}, d)
					if submits.Load() != 0 {
						t.Fatal("blocked batch dispatched")
					}
				}
				if err := tt.Click(locales.Text("取消")); err != nil {
					t.Fatal(err)
				}
			}
			if d.edit.Busy || d.mutation.plan != nil || plan.closes.Load() != 1 || submits.Load() != 0 || continued || d.edit.AfterSave != nil || d.edit.LeaveNext != nil || s.guardDoc != nil || !reflect.DeepEqual(original, captureDraft(d)) {
				t.Fatal("cancel/dismiss lost drafts or retained authorization")
			}
		})
	}
}

func TestDataMutationPreviewCloseAndCancelReleaseUnconsumedAndLateHandles(t *testing.T) {
	for _, state := range []string{"late-close", "unconsumed-close", "late-cancel", "unconsumed-cancel"} {
		t.Run(state, func(t *testing.T) {
			s, d, _ := mutationWorkspace(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var plan *mutationTestPlan
			d.boundRuntime.PreviewEdits = func(_ context.Context, _ int64, _, _ string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
				plan = mutationPlanFor(d, edits)
				close(entered)
				<-release
				return plan, nil
			}
			d.boundRuntime.SubmitEdits = func(context.Context, int64, string, string, contract.DataMutationPlan) (*contract.DataEditResult, error) {
				t.Error("canceled preview submitted")
				return nil, nil
			}
			stageMutationDelete(s, d)
			s.Save(ui.Services{}, d)
			<-entered
			results, ticket := d.mutation.results, d.mutation.ticket
			if strings.HasPrefix(state, "unconsumed") {
				close(release)
				select {
				case err := <-results:
					if err != nil {
						t.Fatal(err)
					}
					out := make(chan error, 1)
					out <- err
					d.mutation.results = out
					results = out
				case <-time.After(5 * time.Second):
					t.Fatal("preview stalled")
				}
			}
			if strings.HasSuffix(state, "close") {
				s.Close(d)
				s.Close(d)
			} else {
				s.stopMySQLDataEdit(d)
				if !d.edit.Busy {
					t.Fatal("cancel preceded reader acknowledgement")
				}
			}
			if strings.HasPrefix(state, "late") {
				close(release)
			}
			if strings.HasSuffix(state, "close") {
				select {
				case <-results:
				case <-time.After(5 * time.Second):
					t.Fatal("closed preview stalled")
				}
				if d.page != nil || d.mutation.ticket != nil || d.mutation.snapshot.NativeJSON != "" {
					t.Fatal("closed document retained resources")
				}
			} else {
				waitMutation(t, s, d)
				if d.edit.Busy || !d.HasDraft() || d.edit.Uncertain != nil {
					t.Fatal("canceled read became unknown write")
				}
			}
			if plan.closes.Load() != 1 || ticket.take() != nil {
				t.Fatal("late/unconsumed preview leaked")
			}
		})
	}
}

func TestDataMutationParkedPreviewGenerationAndDraftMismatchNeverWrite(t *testing.T) {
	for _, change := range []string{"park", "generation", "draft-values", "draft-snapshot", "replacement", "replaced-plan"} {
		t.Run(change, func(t *testing.T) {
			s, d, active := mutationWorkspace(t)
			object := d.object
			var plan *mutationTestPlan
			d.boundRuntime.PreviewEdits = func(_ context.Context, _ int64, _, _ string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
				plan = mutationPlanFor(d, edits)
				return plan, nil
			}
			d.boundRuntime.SubmitEdits = func(context.Context, int64, string, string, contract.DataMutationPlan) (*contract.DataEditResult, error) {
				t.Error("mismatched preview wrote")
				return nil, nil
			}
			stageMutationDelete(s, d)
			s.Save(ui.Services{}, d)
			if change == "park" {
				active.ProfileID = 99
				select {
				case err := <-d.mutation.results:
					out := make(chan error, 1)
					out <- err
					d.mutation.results = out
				case <-time.After(5 * time.Second):
					t.Fatal("parked preview stalled")
				}
				s.Consume(ui.Services{})
				if d.mutation.open || !d.edit.Busy || plan.closes.Load() != 0 {
					t.Fatal("parked preview consumed/released")
				}
				active.ProfileID = object.ProfileID
			}
			waitMutation(t, s, d)
			switch change {
			case "generation":
				active.Generation++
			case "draft-values":
				for _, draft := range d.edit.Drafts {
					draft.Values["extra"] = contract.DataValue{Bytes: []byte("changed")}
				}
			case "draft-snapshot":
				for _, draft := range d.edit.Drafts {
					*draft.Original.Fingerprints[0] = "changed"
				}
			case "replacement":
				s.documents[d.id] = &Document{id: d.id}
			case "replaced-plan":
				plan.Close()
			case "park":
				s.stopMySQLDataEdit(d)
				return
			}
			s.confirmDataMutation(ui.Services{}, d)
			if d.edit.Busy || d.mutation.plan != nil || !d.HasDraft() || d.edit.Uncertain != nil || plan.closes.Load() != 1 {
				t.Fatal("mismatched preview retained authorization or lost original drafts", d.edit.Err)
			}
			s.documents[d.id] = d
		})
	}
}

func TestDataMutationRejectsWrongPreviewAndMissingProviderWithoutSaverFallback(t *testing.T) {
	for _, mode := range []string{"nil", "error-handle", "wrong-object", "wrong-edits", "wrong-items", "empty-evidence", "missing-preview", "missing-submit"} {
		t.Run(mode, func(t *testing.T) {
			s, d, _ := mutationWorkspace(t)
			var plan *mutationTestPlan
			d.boundRuntime.PreviewEdits = func(_ context.Context, _ int64, _, _ string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
				plan = mutationPlanFor(d, edits)
				switch mode {
				case "nil":
					return nil, nil
				case "error-handle":
					return plan, errors.New("original read failed")
				case "wrong-object":
					plan.snapshot.Object.Name = "other"
				case "wrong-edits":
					plan.snapshot.Edits[0].Kind = "detach_delete"
				case "wrong-items":
					plan.snapshot.Items = nil
				case "empty-evidence":
					plan.snapshot.NativeJSON = ""
				}
				return plan, nil
			}
			d.boundRuntime.SubmitEdits = func(context.Context, int64, string, string, contract.DataMutationPlan) (*contract.DataEditResult, error) {
				t.Error("invalid preview submitted")
				return nil, nil
			}
			if mode == "missing-preview" {
				d.boundRuntime.PreviewEdits = nil
			}
			if mode == "missing-submit" {
				d.boundRuntime.SubmitEdits = nil
			}
			s.Saver = func(context.Context, int64, string, string, []contract.DataEdit) (*contract.DataEditResult, error) {
				t.Error("required preview fell back to Saver")
				return nil, nil
			}
			stageMutationDelete(s, d)
			s.Save(ui.Services{}, d)
			waitMutation(t, s, d)
			if d.edit.Busy || d.edit.Err == "" || !d.HasDraft() || d.edit.Uncertain != nil {
				t.Fatal("invalid read lost drafts or created unknown write")
			}
			if plan != nil && mode != "nil" && plan.closes.Load() != 1 {
				t.Fatal("invalid read handle retained")
			}
		})
	}
}

func TestDataMutationReceiptNeedsActualCommitCompleteOutcomesAndOriginalDrafts(t *testing.T) {
	for _, mode := range []string{"no-commit", "incomplete", "applied-count", "missing-outcome", "wrong-kind", "wrong-original", "changed-drafts", "generation", "success"} {
		t.Run(mode, func(t *testing.T) {
			s, d, active := mutationWorkspace(t)
			var plan *mutationTestPlan
			var submits atomic.Int32
			d.boundRuntime.PreviewEdits = func(_ context.Context, _ int64, _, _ string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
				plan = mutationPlanFor(d, edits)
				return plan, nil
			}
			release := make(chan struct{})
			d.boundRuntime.SubmitEdits = func(context.Context, int64, string, string, contract.DataMutationPlan) (*contract.DataEditResult, error) {
				submits.Add(1)
				<-release
				snapshot := plan.Snapshot()
				result := &contract.DataEditResult{CommitConfirmed: true, Applied: 1, NativeJSON: `{"captured":"receipt"}`, Outcomes: []contract.DataEditOutcome{{Kind: snapshot.Edits[0].Kind, Original: snapshot.Edits[0].Original}}}
				switch mode {
				case "no-commit":
					result.CommitConfirmed = false
				case "applied-count":
					result.Applied = 0
				case "incomplete":
					result.Incomplete = true
				case "missing-outcome":
					result.Outcomes = nil
				case "wrong-kind":
					result.Outcomes[0].Kind = "insert"
				case "wrong-original":
					result.Outcomes[0].Original = nil
				}
				return result, nil
			}
			stageMutationDelete(s, d)
			s.Save(ui.Services{}, d)
			waitMutation(t, s, d)
			s.confirmDataMutation(ui.Services{}, d)
			if mode == "generation" {
				active.Generation++
			}
			if mode == "changed-drafts" {
				for _, draft := range d.edit.Drafts {
					draft.Values["changed"] = contract.DataValue{Text: "external"}
				}
			}
			close(release)
			waitMutationSave(t, s, d)
			if mode == "success" {
				if d.HasDraft() || len(d.page.Rows) != 2 || d.edit.Uncertain != nil {
					t.Fatal("complete committed receipt not accepted")
				}
			} else {
				if !d.HasDraft() || len(d.page.Rows) != 3 || d.edit.Uncertain == nil || !d.edit.Uncertain.CommitUncertain {
					t.Fatal("unconfirmed/incomplete result authorized row removal")
				}
				if mode != "changed-drafts" && (len(d.tools.history) == 0 || !d.tools.history[0].Failed) {
					t.Fatal("unconfirmed or incomplete original receipt recorded as successful history")
				}
				s.Save(ui.Services{}, d)
			}
			if submits.Load() != 1 || d.nativeReceipt.JSON != `{"captured":"receipt"}` || plan.closes.Load() != 1 {
				t.Fatal("original receipt lost or submit replayed")
			}
		})
	}
}

func TestDataMutationTicketConcurrentRevocationReleasesExactlyOnePlan(t *testing.T) {
	for i := 0; i < 1000; i++ {
		ticket := &dataMutationTicket{}
		plan := &mutationTestPlan{}
		var work sync.WaitGroup
		work.Add(2)
		go func() { defer work.Done(); ticket.publish(plan) }()
		go func() { defer work.Done(); ticket.revoke() }()
		work.Wait()
		ticket.revoke()
		if ticket.take() != nil || plan.closes.Load() != 1 {
			t.Fatal("publication/revocation race leaked or double-owned plan")
		}
	}
}

func TestDataMutationTypedBytesAndFingerprintsAreCapturedIndependently(t *testing.T) {
	s, d, _ := mutationWorkspace(t)
	var plan *mutationTestPlan
	entered, release := make(chan struct{}), make(chan struct{})
	d.boundRuntime.PreviewEditKinds = []string{"update"}
	d.boundRuntime.PreviewEdits = func(_ context.Context, _ int64, _, _ string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
		close(entered)
		<-release
		if string(edits[0].Values["extra"].Bytes) != "original-bytes" || *edits[0].Original.Fingerprints[0] != "snapshot-0" || edits[0].Original.Ref.TypedValues[0].Text != "original-id" {
			t.Error("preview adopted UI bytes/fingerprints/typed identity")
		}
		plan = mutationPlanFor(d, edits)
		return plan, nil
	}
	d.boundRuntime.SubmitEdits = func(context.Context, int64, string, string, contract.DataMutationPlan) (*contract.DataEditResult, error) {
		t.Error("changed drafts submitted")
		return nil, nil
	}
	d.page.RowSnapshots[0].Ref.TypedValues = []contract.Cell{{Kind: contract.Text, Text: "original-id"}}
	s.setMySQLDataCell(ui.Services{}, d, 0, 2, contract.DataValue{Kind: contract.Binary, Binary: true, Bytes: []byte("original-bytes")})
	s.Save(ui.Services{}, d)
	<-entered
	for _, draft := range d.edit.Drafts {
		draft.Values["extra"].Bytes[0] = 'X'
		*draft.Original.Fingerprints[0] = "changed"
		draft.Original.Ref.TypedValues[0].Text = "changed"
	}
	close(release)
	waitMutation(t, s, d)
	if d.edit.Busy || d.edit.Uncertain != nil || !d.HasDraft() || plan.closes.Load() != 1 {
		t.Fatal("changed draft preview did not revoke")
	}
}

func TestDataMutationPropertyOnlySaveKeepsExistingSaver(t *testing.T) {
	s, d, _ := mutationWorkspace(t)
	d.boundRuntime.PreviewEdits = func(context.Context, int64, string, string, []contract.DataEdit) (contract.DataMutationPlan, error) {
		t.Error("ordinary update unexpectedly previewed")
		return nil, nil
	}
	s.setMySQLDataCell(ui.Services{}, d, 0, 1, contract.DataValue{Text: "ordinary property edit"})
	s.Save(ui.Services{}, d)
	waitMutationSave(t, s, d)
	if d.mutation.open || d.edit.Err != "" || d.HasDraft() {
		t.Fatal("existing update Saver changed", d.edit.Err)
	}
}

func TestDataMutationDetachDraftCannotEditAndCommittedRowsAreRemoved(t *testing.T) {
	s, d, _ := mutationWorkspace(t)
	stageMutationDelete(s, d)
	for _, draft := range d.edit.Drafts {
		draft.Kind = "detach_delete"
	}
	if mysqlDataCanEdit(d, 0, 1) || mysqlDataCanDeleteSelection(d, 0) {
		t.Fatal("detach-deleted draft still editable")
	}
	entries := mysqlDataWriteHistoryRequest(d.database, d.table, []contract.DataEdit{{Kind: "detach_delete"}}, false)
	if len(entries) != 1 || entries[0].Kind != "DETACH_DELETE" {
		t.Fatal("detach draft missing from history")
	}
	keys := slices.Collect(func(yield func(string) bool) {
		for k := range d.edit.Drafts {
			if !yield(k) {
				return
			}
		}
	})
	result := &contract.DataEditResult{CommitConfirmed: true, Outcomes: []contract.DataEditOutcome{{Kind: "detach_delete"}}}
	s.acceptMySQLDataSaved(d, keys, result)
	if len(d.page.Rows) != 2 || d.HasDraft() {
		t.Fatal("detach deletion retained original row")
	}
}

func TestDataMutationLocalizedConfirmationControlsFitWideAndNarrowWindows(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, blocked := range []bool{false, true} {
				s, d, _ := mutationWorkspace(t)
				d.boundRuntime.PreviewEdits = func(_ context.Context, _ int64, _, _ string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
					plan := mutationPlanFor(d, edits)
					plan.snapshot.NativeJSON = strings.Repeat("完整预览猫 ", 12000)
					plan.snapshot.Items[0].Blocked, plan.snapshot.Items[0].AffectedRelationships = blocked, 3
					return plan, nil
				}
				d.boundRuntime.SubmitEdits = func(context.Context, int64, string, string, contract.DataMutationPlan) (*contract.DataEditResult, error) {
					t.Error("rendering submitted a preview")
					return nil, nil
				}
				stageMutationDelete(s, d)
				s.Save(ui.Services{}, d)
				waitMutation(t, s, d)
				original := d.mutation.snapshot.NativeJSON
				tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Modals(c) }, size[0], size[1])
				tt.Frame()
				for _, label := range []string{"确认实体更改", "复制完整预览", "取消", "确认并提交更改"} {
					rect, found := tt.Find(locales.Text(label))
					if !found || rect.X < 0 || rect.Y < 0 || rect.X+rect.W > float32(size[0])+1 || rect.Y+rect.H > float32(size[1])+1 {
						t.Fatal("confirmation control clipped", language, size, blocked, label, rect)
					}
				}
				if d.mutation.snapshot.NativeJSON != original || !strings.Contains(dataMutationPreview(original), locales.Text("预览已截断；可复制完整内容")) {
					t.Fatal("bounded preview narrowed the owned evidence")
				}
				if output := os.Getenv("NYADB_DATA_MUTATION_IMAGES"); output != "" {
					if err := os.MkdirAll(output, 0700); err != nil {
						t.Fatal(err)
					}
					name := language + "-" + strconv.Itoa(size[0]) + "x" + strconv.Itoa(size[1]) + "-" + strconv.FormatBool(blocked) + ".png"
					file, err := os.Create(filepath.Join(output, name))
					if err != nil {
						t.Fatal(err)
					}
					err = png.Encode(file, tt.Image())
					file.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				tt.Key(0, ui.KeyEscape)
				tt.Frame()
				if d.edit.Busy || d.mutation.plan != nil || !d.HasDraft() {
					t.Fatal("localized dismissal retained plan or lost drafts")
				}
				s.CloseAll()
			}
		}
	}
}

func TestDataMutationGeneratedIdentityRequiresCommittedOriginalInsert(t *testing.T) {
	for _, mode := range []string{"success", "no-commit", "missing-saved", "wrong-object", "missing-typed", "missing-bytes"} {
		t.Run(mode, func(t *testing.T) {
			s, d, _ := mutationWorkspace(t)
			var plan *mutationTestPlan
			d.boundRuntime.PreviewEdits = func(_ context.Context, _ int64, _, _ string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
				plan = mutationPlanFor(d, edits)
				return plan, nil
			}
			object := d.object
			d.boundRuntime.SubmitEdits = func(context.Context, int64, string, string, contract.DataMutationPlan) (*contract.DataEditResult, error) {
				saved := &contract.RowSnapshot{Ref: contract.RowRef{Object: object, Columns: []string{"_entity_identity"}, Values: [][]byte{[]byte("signed original identity")}, TypedValues: []contract.Cell{{Kind: contract.Text, Text: "generated-entity"}}}}
				result := &contract.DataEditResult{CommitConfirmed: true, Applied: 1, Outcomes: []contract.DataEditOutcome{{Kind: "insert", Saved: saved}}}
				switch mode {
				case "no-commit":
					result.CommitConfirmed = false
				case "missing-saved":
					result.Outcomes[0].Saved = nil
				case "wrong-object":
					saved.Ref.Object.Name = "other"
				case "missing-typed":
					saved.Ref.TypedValues = nil
				case "missing-bytes":
					saved.Ref.Values = nil
				}
				return result, nil
			}
			s.addMySQLDataRecord(ui.Services{}, d)
			s.finishMySQLCellEdit(d, false)
			s.Save(ui.Services{}, d)
			waitMutation(t, s, d)
			if len(d.page.RowRefs) != 4 || d.page.RowRefs[3] != nil {
				t.Fatal("preview generated a provisional row identity")
			}
			s.confirmDataMutation(ui.Services{}, d)
			waitMutationSave(t, s, d)
			if mode == "success" {
				if d.HasDraft() || d.page.RowRefs[3] == nil || d.page.RowRefs[3].TypedValues[0].Text != "generated-entity" {
					t.Fatal("actual committed identity not accepted", d.edit.Err)
				}
			} else if !d.HasDraft() || d.page.RowRefs[3] != nil || d.edit.Uncertain == nil {
				t.Fatal("unconfirmed/generated foreign identity replaced insert draft", d.edit.Err)
			}
		})
	}
}

func TestDataMutationActivityRecordsOnlyCompleteConfirmedOriginalOutcomesAsSuccess(t *testing.T) {
	object := contract.ObjectRef{ProfileID: 7, Catalog: "original", Kind: "label", Name: "entities"}
	edits := []contract.DataEdit{{Kind: "insert"}}
	saved := &contract.RowSnapshot{Ref: contract.RowRef{Object: object, Columns: []string{"identity"}, Values: [][]byte{[]byte("signed")}, TypedValues: []contract.Cell{{Kind: contract.Text, Text: "original"}}}}
	original := &contract.DataEditResult{CommitConfirmed: true, Applied: 1, Outcomes: []contract.DataEditOutcome{{Kind: "insert", Saved: saved}}}
	for _, mode := range []string{"success", "no-commit", "wrong-kind", "foreign-identity", "missing-outcomes"} {
		result := cloneEditResult(original)
		switch mode {
		case "no-commit":
			result.CommitConfirmed = false
		case "wrong-kind":
			result.Outcomes[0].Kind = "delete"
		case "foreign-identity":
			result.Outcomes[0].Saved.Ref.Object.Name = "other"
		case "missing-outcomes":
			result.Outcomes = nil
		}
		outcome := dataMutationActivityOutcome(nil, object, edits, result)
		if mode == "success" {
			if outcome.State != "success" || outcome.AffectedRows != 1 {
				t.Fatal("complete confirmed receipt not logged", outcome)
			}
		} else if outcome.State == "success" || outcome.AffectedRows != 0 || outcome.Error == "" {
			t.Fatal("unconfirmed or unrelated receipt recorded as successful", mode, outcome)
		}
	}
}
