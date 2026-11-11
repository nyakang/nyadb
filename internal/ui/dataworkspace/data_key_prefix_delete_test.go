package dataworkspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

type prefixUIFixture struct {
	mu             sync.Mutex
	requests       []contract.DataKeyPrefixDelete
	plans          []*mutationTestPlan
	submits, reads atomic.Int32
	fault          string
	mutate         func(*contract.DataMutationSnapshot)
}

func prefixUIWorkspace(t *testing.T, mode string) (*State, *Document, *Session, *prefixUIFixture) {
	t.Helper()
	s, d, active, _ := copyUIWorkspace(t, mode)
	f := &prefixUIFixture{}
	object, profile, database, tableName := d.object, d.profileID, d.database, d.table
	template := cloneSnapshot(d.page.RowSnapshots[0])
	template.Ref.Columns[0] = "_fixture_signed_identity"
	columns := append([]string(nil), d.page.Columns...)
	d.boundRuntime.PreviewKeyPrefixDelete = func(_ context.Context, id int64, db, table string, r contract.DataKeyPrefixDelete) (contract.DataMutationPlan, error) {
		if id != profile || db != database || table != tableName || r.Object != object {
			t.Error("prefix preview changed original qualified scope")
		}
		snapshot := contract.DataMutationSnapshot{Object: r.Object, KeyPrefixDelete: &r, NativeJSON: `{"Scope":{"Prefix":"scope/","CompleteOriginalResponse":true},"Value":"` + strings.Repeat("猫", 30000) + `"}`}
		// All candidates are outside the visible page. The grid preview cannot
		// supply complete values or authorize their deletion.
		for _, key := range []string{r.Prefix + "", r.Prefix + "outside//猫"} {
			original := cloneSnapshot(template)
			original.Ref.Values[0] = []byte("signed-original:" + key)
			original.Ref.TypedValues[0].Text = key
			if mode == "values" {
				original.Original[0].Text = key
			}
			snapshot.Edits = append(snapshot.Edits, contract.DataEdit{Kind: "delete", Original: original})
			snapshot.Items = append(snapshot.Items, contract.DataMutationItem{Kind: "delete", EntityID: key})
		}
		if f.mutate != nil {
			f.mutate(&snapshot)
		}
		plan := &mutationTestPlan{snapshot: contract.CloneDataMutationSnapshot(snapshot)}
		f.mu.Lock()
		f.requests = append(f.requests, r)
		f.plans = append(f.plans, plan)
		f.mu.Unlock()
		return plan, nil
	}
	d.boundRuntime.SubmitEdits = func(_ context.Context, id int64, db, table string, plan contract.DataMutationPlan) (*contract.DataEditResult, error) {
		f.submits.Add(1)
		if id != profile || db != database || table != tableName {
			t.Error("prefix submit followed foreign scope")
		}
		snapshot := plan.Snapshot()
		plan.Close()
		result := &contract.DataEditResult{Applied: len(snapshot.Edits), CommitConfirmed: true, NativeJSON: `{"original-transaction-receipt":true}`}
		for _, e := range snapshot.Edits {
			result.Outcomes = append(result.Outcomes, contract.DataEditOutcome{Kind: e.Kind, Original: cloneSnapshot(e.Original)})
		}
		switch f.fault {
		case "lost":
			result.CommitConfirmed = false
			result.CommitUncertain = true
			return result, contract.ErrDataCommitUncertain
		case "nil-unknown":
			return nil, contract.ErrDataCommitUncertain
		case "missing":
			result.Outcomes = nil
		case "foreign":
			result.Outcomes[0].Original.Ref.Object.ProfileID++
		case "incomplete":
			result.Incomplete = true
		case "conflict":
			return &contract.DataEditResult{NativeJSON: `{"conflict":true}`}, contract.ErrDataConflict
		}
		return result, nil
	}
	d.boundRuntime.Loader = func(context.Context, int64, string, string, int64, int) (*contract.DataPage, error) {
		f.reads.Add(1)
		return &contract.DataPage{Object: object, Database: database, Table: tableName, Columns: columns}, nil
	}
	return s, d, active, f
}

func waitPrefix(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for d.prefixDelete != nil && (d.prefixDelete.previewResults != nil || d.prefixDelete.results != nil) && time.Now().Before(deadline) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.prefixDelete != nil && (d.prefixDelete.previewResults != nil || d.prefixDelete.results != nil) {
		t.Fatal("prefix executor acknowledgement stalled")
	}
}

func stagePrefix(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.DeleteKeyPrefix(d)
	if d.prefixDelete == nil {
		t.Fatal("prefix input not admitted")
	}
	d.prefixDelete.prefix = "scope/"
	s.previewKeyPrefixDelete(ui.Services{}, d)
	waitPrefix(t, s, d)
}

func TestDataKeyPrefixCompleteCandidatesOutsideVisiblePageRequireOriginalConfirmation(t *testing.T) {
	for _, mode := range []string{"values", "fingerprints"} {
		t.Run(mode, func(t *testing.T) {
			s, d, _, f := prefixUIWorkspace(t, mode)
			before := captureDraft(d)
			s.DeleteKeyPrefix(d)
			if !d.HasDraft() || !d.LeavePending() || s.CanSave(d) || !s.GuardLeave(d, nil) || len(f.requests) != 0 {
				t.Fatal("input lost guard or dispatched a read")
			}
			d.prefixDelete.prefix = "scope/"
			s.previewKeyPrefixDelete(ui.Services{}, d)
			d.selectedRow = 1
			waitPrefix(t, s, d)
			if !d.prefixDelete.previewOpen || f.submits.Load() != 0 || f.reads.Load() != 0 || !bytes.Equal(d.prefixDelete.snapshot.Edits[0].Original.Ref.Values[0], []byte("signed-original:scope/")) || !strings.Contains(d.prefixEvidence.JSON, "CompleteOriginalResponse") {
				t.Fatal("complete prefix observations not retained")
			}
			current := captureDraft(d)
			current.Row = before.Row
			if !reflect.DeepEqual(before, current) || len(d.edit.Drafts) != 0 || len(d.prefixDelete.snapshot.Edits) != 2 || d.prefixDelete.snapshot.Edits[0].Original.Mode != mode {
				t.Fatal("prefix operation forged visible row drafts")
			}
			s.confirmKeyPrefixDelete(ui.Services{}, d)
			s.confirmKeyPrefixDelete(ui.Services{}, d)
			waitPrefix(t, s, d)
			if d.prefixDelete != nil || d.edit.Uncertain != nil || d.HasDraft() || f.submits.Load() != 1 || d.nativeReceipt.JSON != `{"original-transaction-receipt":true}` || d.prefixEvidence.JSON == "" {
				t.Fatal("complete receipt failed to settle original operation", d.edit.Err)
			}
		})
	}
}

func TestDataKeyPrefixMalformedPreviewNeverReachesSubmit(t *testing.T) {
	for _, fault := range []string{"descriptor", "object", "foreign-original", "outside", "duplicate", "wrong-kind", "typed-key", "truncated-key", "raw-key", "blocked", "missing-item", "missing-json", "authored-values"} {
		t.Run(fault, func(t *testing.T) {
			s, d, _, f := prefixUIWorkspace(t, "fingerprints")
			f.mutate = func(p *contract.DataMutationSnapshot) {
				switch fault {
				case "descriptor":
					p.KeyPrefixDelete.Prefix = "foreign/"
				case "object":
					p.Object.ProfileID++
				case "foreign-original":
					p.Edits[0].Original.Ref.Object.ProfileID++
				case "outside":
					p.Items[0].EntityID = "outside"
				case "duplicate":
					p.Items[1] = p.Items[0]
					p.Edits[1] = p.Edits[0]
				case "wrong-kind":
					p.Edits[0].Kind = "update"
				case "typed-key":
					p.Edits[0].Original.Ref.TypedValues[0].Kind = contract.Binary
				case "truncated-key":
					p.Edits[0].Original.Ref.TypedValues[0].Truncated = true
				case "raw-key":
					p.Edits[0].Original.Ref.Values = nil
				case "blocked":
					p.Items[0].Blocked = true
				case "missing-item":
					p.Items = p.Items[:1]
				case "missing-json":
					p.NativeJSON = ""
				case "authored-values":
					p.Edits[0].Values = map[string]contract.DataValue{"value": {Kind: contract.Text, Text: "authored"}}
				}
			}
			stagePrefix(t, s, d)
			s.confirmKeyPrefixDelete(ui.Services{}, d)
			if d.prefixDelete != nil || d.edit.Err == "" || f.submits.Load() != 0 || f.plans[0].closes.Load() != 1 {
				t.Fatal("malformed prefix plan retained authority", fault)
			}
		})
	}
}

func TestDataKeyPrefixUnknownReceiptsRetainIndependentObservationAndForbidReplay(t *testing.T) {
	for _, fault := range []string{"lost", "nil-unknown", "missing", "foreign", "incomplete"} {
		t.Run(fault, func(t *testing.T) {
			s, d, _, f := prefixUIWorkspace(t, "values")
			f.fault = fault
			stagePrefix(t, s, d)
			original := d.prefixEvidence.JSON
			s.confirmKeyPrefixDelete(ui.Services{}, d)
			waitPrefix(t, s, d)
			if d.prefixDelete == nil || !d.prefixDelete.uncertain || d.edit.Uncertain == nil || !d.HasDraft() || d.edit.Busy || d.prefixEvidence.JSON != original || !s.GuardLeave(d, nil) {
				t.Fatal("unknown prefix lost original evidence/barrier")
			}
			s.Save(ui.Services{}, d)
			s.DeleteKeyPrefix(d)
			s.confirmKeyPrefixDelete(ui.Services{}, d)
			s.Load(ui.Services{}, d, true)
			if f.submits.Load() != 1 || len(f.requests) != 1 || f.reads.Load() != 0 || mysqlDataCanEdit(d, 0, 1) {
				t.Fatal("unknown prefix allowed replay or authored changes")
			}
			s.Close(d)
			if d.prefixDelete != nil || d.prefixEvidence.JSON != "" || d.nativeReceipt.JSON != "" {
				t.Fatal("close retained prefix evidence")
			}
		})
	}
}

func TestDataKeyPrefixConfirmationCancelAndConflictRequireFreshObservation(t *testing.T) {
	s, d, _, f := prefixUIWorkspace(t, "fingerprints")
	stagePrefix(t, s, d)
	original := d.prefixEvidence.JSON
	s.cancelKeyPrefixConfirmation(d)
	if !d.prefixDelete.open || d.prefixDelete.plan != nil || f.plans[0].closes.Load() != 1 || f.submits.Load() != 0 || d.prefixEvidence.JSON != original {
		t.Fatal("confirmation cancel lost original input or retained authority")
	}
	f.fault = "conflict"
	s.previewKeyPrefixDelete(ui.Services{}, d)
	waitPrefix(t, s, d)
	s.confirmKeyPrefixDelete(ui.Services{}, d)
	waitPrefix(t, s, d)
	if d.edit.Uncertain != nil || !d.prefixDelete.open || f.submits.Load() != 1 || d.edit.Err == "" {
		t.Fatal("confirmed conflict did not return to original input")
	}
	f.fault = ""
	s.previewKeyPrefixDelete(ui.Services{}, d)
	waitPrefix(t, s, d)
	if len(f.requests) != 3 || f.submits.Load() != 1 || f.requests[2] != f.requests[0] {
		t.Fatal("retry reused dispatched plan or changed original prefix")
	}
	s.cancelKeyPrefixDelete(d)
	if d.HasDraft() || d.edit.Busy || d.prefixDelete != nil || f.plans[2].closes.Load() != 1 {
		t.Fatal("explicit cancel did not settle input")
	}
}

func TestDataKeyPrefixPendingCancelAndCloseWaitOriginalExecutor(t *testing.T) {
	for _, phase := range []string{"preview", "submit"} {
		for _, closeDoc := range []bool{false, true} {
			t.Run(phase+map[bool]string{false: "-cancel", true: "-close"}[closeDoc], func(t *testing.T) {
				s, d, _, f := prefixUIWorkspace(t, "fingerprints")
				entered, release := make(chan struct{}), make(chan struct{})
				var result <-chan error
				if phase == "preview" {
					preview := d.boundRuntime.PreviewKeyPrefixDelete
					d.boundRuntime.PreviewKeyPrefixDelete = func(ctx context.Context, id int64, db, table string, r contract.DataKeyPrefixDelete) (contract.DataMutationPlan, error) {
						close(entered)
						<-release
						return preview(ctx, id, db, table, r)
					}
					s.DeleteKeyPrefix(d)
					d.prefixDelete.prefix = "scope/"
					s.previewKeyPrefixDelete(ui.Services{}, d)
					result = d.prefixDelete.previewResults
				} else {
					stagePrefix(t, s, d)
					d.boundRuntime.SubmitEdits = func(ctx context.Context, _ int64, _, _ string, _ contract.DataMutationPlan) (*contract.DataEditResult, error) {
						close(entered)
						<-release
						return nil, ctx.Err()
					}
					// The captured original submit callback is the one prepared above.
					d.prefixDelete.submit = d.boundRuntime.SubmitEdits
					s.confirmKeyPrefixDelete(ui.Services{}, d)
				}
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("executor not entered")
				}
				m := d.prefixDelete
				if closeDoc {
					s.Close(d)
				} else {
					s.stopMySQLDataEdit(d)
					if !d.edit.Busy || d.prefixDelete != m || !s.GuardLeave(d, nil) {
						t.Fatal("cancel settled before executor acknowledgement")
					}
				}
				close(release)
				if closeDoc {
					if phase == "preview" {
						select {
						case <-result:
						case <-time.After(5 * time.Second):
							t.Fatal("closed preview stalled")
						}
					} else {
						select {
						case <-m.results:
						case <-time.After(5 * time.Second):
							t.Fatal("closed submit stalled")
						}
					}
					if d.prefixDelete != nil || d.prefixEvidence.JSON != "" {
						t.Fatal("closed operation retained caches")
					}
				} else {
					waitPrefix(t, s, d)
					if d.edit.Uncertain != nil || f.submits.Load() != 0 {
						t.Fatal("pre-write cancellation became unknown")
					}
					s.cancelKeyPrefixDelete(d)
				}
			})
		}
	}
}

func TestDataKeyPrefixParkedCompletionsAndGenerationKeepOriginalOwnership(t *testing.T) {
	for _, phase := range []string{"preview", "submit"} {
		for _, replaced := range []bool{false, true} {
			t.Run(phase+map[bool]string{false: "-same", true: "-replaced"}[replaced], func(t *testing.T) {
				s, d, active, f := prefixUIWorkspace(t, "fingerprints")
				if phase == "submit" {
					stagePrefix(t, s, d)
					s.confirmKeyPrefixDelete(ui.Services{}, d)
				} else {
					s.DeleteKeyPrefix(d)
					d.prefixDelete.prefix = "scope/"
					s.previewKeyPrefixDelete(ui.Services{}, d)
				}
				m := d.prefixDelete
				active.ProfileID = 99
				deadline := time.Now().Add(5 * time.Second)
				for (phase == "preview" && len(m.previewResults) == 0 || phase == "submit" && len(m.results) == 0) && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				s.Consume(ui.Services{})
				if phase == "preview" && m.previewResults == nil || phase == "submit" && m.results == nil {
					t.Fatal("parked completion consumed under foreign profile")
				}
				active.ProfileID = d.profileID
				if replaced {
					active.Generation++
					s.deps.SessionFor = func(int64) Session { return *active }
				}
				waitPrefix(t, s, d)
				if phase == "preview" {
					if replaced {
						if d.prefixDelete != nil || f.plans[0].closes.Load() != 1 {
							t.Fatal("late preview adopted replacement generation")
						}
					} else if !d.prefixDelete.previewOpen || f.submits.Load() != 0 {
						t.Fatal("original parked preview not recovered")
					}
				} else if replaced {
					if d.edit.Uncertain == nil || !d.prefixDelete.uncertain || f.reads.Load() != 0 {
						t.Fatal("late submitted result authorized replacement page")
					}
				} else if d.prefixDelete != nil || f.submits.Load() != 1 {
					t.Fatal("original parked submit not settled")
				}
			})
		}
	}
}

func TestDataKeyPrefixToolbarInputEscapeAndLeaveGuard(t *testing.T) {
	for _, escape := range []bool{true, false} {
		t.Run(map[bool]string{true: "escape", false: "preview"}[escape], func(t *testing.T) {
			s, d, _, f := prefixUIWorkspace(t, "fingerprints")
			before := captureDraft(d)
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).FillWidth().Children(func() { s.mysqlDataToolbar(c, d) })
				s.Modals(c)
			}, 720, 520)
			tt.Frame()
			if err := tt.Click(locales.Text("按前缀删除候选键")); err != nil {
				t.Fatal(err)
			}
			if d.prefixDelete == nil || !d.LeavePending() || !s.GuardLeave(d, nil) || len(f.requests) != 0 {
				t.Fatal("toolbar lost guarded original input")
			}
			if err := tt.Click(locales.Text("输入完整删除前缀")); err != nil {
				t.Fatal(err)
			}
			tt.Key(ui.Cmd, ui.KeyA)
			tt.Type("scope/")
			if d.prefixDelete.prefix != "scope/" {
				t.Fatal("native prefix input not applied")
			}
			if escape {
				tt.Key(0, ui.KeyEscape)
				tt.Frame()
				if d.prefixDelete != nil || d.edit.Busy || !reflect.DeepEqual(before, captureDraft(d)) || len(f.requests) != 0 {
					t.Fatal("Escape dispatched or modified grid")
				}
			} else {
				if err := tt.Click(locales.Text("预览候选键删除")); err != nil {
					t.Fatal(err)
				}
				waitPrefix(t, s, d)
				tt.Frame()
				if !tt.HasText(locales.Text("确认候选键删除")) || f.submits.Load() != 0 || len(f.requests) != 1 {
					t.Fatal("actual preview omitted confirmation")
				}
			}
		})
	}
}

func TestDataKeyPrefixEvidenceExportCapturesOriginalAndCloseRevokesLatePicker(t *testing.T) {
	s, d, _, _ := prefixUIWorkspace(t, "fingerprints")
	stagePrefix(t, s, d)
	original := d.prefixEvidence.JSON
	entered, release := make(chan struct{}), make(chan struct{})
	path := filepath.Join(t.TempDir(), "observation.json")
	s.exportKeyPrefixEvidence(ui.Services{}, d, func(mygo.SaveDialogOptions) (string, error) { close(entered); <-release; return path, nil })
	<-entered
	d.prefixEvidence.JSON = "replacement"
	close(release)
	select {
	case err := <-d.prefixEvidence.Results:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("export stalled")
	}
	d.prefixEvidence.Cancel()
	d.prefixEvidence.Cancel = nil
	d.prefixEvidence.Results = nil
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != original {
		t.Fatal("export used later observation", err)
	}
	entered, release = make(chan struct{}), make(chan struct{})
	s.exportKeyPrefixEvidence(ui.Services{}, d, func(mygo.SaveDialogOptions) (string, error) { close(entered); <-release; return path, nil })
	<-entered
	out := d.prefixEvidence.Results
	s.Close(d)
	close(release)
	select {
	case err = <-out:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("closed export wrote late file", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closed export stalled")
	}
	raw, err = os.ReadFile(path)
	if err != nil || string(raw) != original || d.prefixEvidence.JSON != "" {
		t.Fatal("close replaced file or retained evidence")
	}
	s.exportKeyPrefixEvidence(ui.Services{}, nil, nil)
	s.exportNativeWriteReceipt(ui.Services{}, nil, nil)
}
