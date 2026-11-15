package dataworkspace

import (
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func dataSequentialTestResult(object contract.ObjectRef, edits []contract.DataEdit, states ...contract.DataWriteStatementState) *contract.DataEditResult {
	r := &contract.DataEditResult{NativeJSON: `{"original_sequential_batch":"猫"}`, Sequential: &contract.DataSequentialWriteReceipt{Object: object}}
	for i, edit := range edits {
		statement := contract.DataWriteStatementReceipt{EditIndex: i, Kind: edit.Kind, Destination: object, State: states[i]}
		if statement.State == contract.DataWriteAcknowledged || statement.State == contract.DataWriteFailed {
			statement.NativeJSON = `{"original_response":true}`
		}
		if statement.State == contract.DataWriteAcknowledged {
			statement.AffectedRows = 1
			r.Applied++
			saved := cloneSnapshot(edit.Original)
			if saved != nil && len(saved.Ref.TypedValues) == 0 {
				for _, value := range saved.Ref.Values {
					saved.Ref.TypedValues = append(saved.Ref.TypedValues, contract.Cell{Kind: contract.Text, Text: string(value)})
				}
			}
			r.Outcomes = append(r.Outcomes, contract.DataEditOutcome{Kind: edit.Kind, Original: cloneSnapshot(edit.Original), Saved: saved})
		}
		r.Sequential.Statements = append(r.Sequential.Statements, statement)
	}
	return r
}

func TestDataSequentialWriteRequiresOriginalAcknowledgementsAndRetainsFailedPrefix(t *testing.T) {
	for _, mode := range []string{"success", "native-failure", "partial-no-error", "unknown", "unknown-no-error", "malformed", "wrong-object", "wrong-original-prefix", "verified", "fake-commit", "generation", "park", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, d, active := mutationWorkspace(t)
			d.boundRuntime.SequentialWrites = true
			d.boundRuntime.PreviewEditKinds = []string{"update"}
			object := d.object
			var submits, verifies atomic.Int32
			activities := make(chan contract.ActivityResult, 1)
			s.deps.RecordActivity = func(request contract.ActivityRequest) func(contract.ActivityResult) {
				if request.ProfileID != object.ProfileID || request.Generation != active.Generation {
					t.Error("activity lost original scope")
				}
				return func(result contract.ActivityResult) { activities <- result }
			}
			s.Verify = func(context.Context, int64, string, string, *contract.DataEditResult) (*contract.DataVerification, error) {
				verifies.Add(1)
				return &contract.DataVerification{Status: "applied"}, nil
			}
			d.boundRuntime.PreviewEdits = func(_ context.Context, _ int64, _, _ string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
				return mutationPlanFor(d, edits), nil
			}
			entered, release, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
			d.boundRuntime.SubmitEdits = func(ctx context.Context, _ int64, _, _ string, supplied contract.DataMutationPlan) (*contract.DataEditResult, error) {
				submits.Add(1)
				snapshot := supplied.Snapshot()
				close(entered)
				if mode == "cancel" {
					<-ctx.Done()
					close(canceled)
				}
				<-release
				states := []contract.DataWriteStatementState{contract.DataWriteAcknowledged, contract.DataWriteAcknowledged, contract.DataWriteAcknowledged}
				var err error
				if mode == "native-failure" || mode == "partial-no-error" || mode == "wrong-original-prefix" {
					states[1], states[2], err = contract.DataWriteFailed, contract.DataWriteNotSent, contract.ErrUnsupported
				}
				if mode == "unknown" || mode == "unknown-no-error" || mode == "cancel" {
					states[1], states[2], err = contract.DataWriteUnknown, contract.DataWriteNotSent, contract.ErrDataCommitUncertain
				}
				if mode == "partial-no-error" || mode == "unknown-no-error" {
					err = nil
				}
				r := dataSequentialTestResult(object, snapshot.Edits, states...)
				// Deliberately omit Incomplete/CommitUncertain on failed results:
				// original receipt structure must still prohibit batch replay.
				switch mode {
				case "malformed":
					r.Sequential.Statements[1].NativeJSON = ""
				case "wrong-object":
					r.Sequential.Object.Name = "foreign"
				case "wrong-original-prefix":
					r.Outcomes[0].Original = nil
				case "verified":
					r.Verified = true
				case "fake-commit":
					r.Sequential, r.CommitConfirmed = nil, true
				}
				return r, err
			}
			for row := 0; row < 3; row++ {
				s.setMySQLDataCell(ui.Services{}, d, row, 1, contract.DataValue{Kind: contract.Text, Text: "native changed 猫"})
			}
			page := d.page
			s.Save(ui.Services{}, d)
			waitMutation(t, s, d)
			if !d.mutation.open || submits.Load() != 0 {
				t.Fatal("native batch submitted before immutable confirmation", d.edit.Err)
			}
			s.confirmDataMutation(ui.Services{}, d)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("native executor did not start")
			}
			if mode == "cancel" {
				s.stopMySQLDataEdit(d)
				select {
				case <-canceled:
				case <-time.After(5 * time.Second):
					t.Fatal("original executor did not receive cancellation")
				}
				s.Consume(ui.Services{})
				if !d.edit.Busy || !d.HasDraft() || s.CanSave(d) || !s.GuardLeave(d, nil) {
					t.Fatal("cancellation released drafts or leave guard before executor acknowledgement")
				}
			}
			if mode == "generation" {
				active.Generation++
			}
			if mode == "park" {
				active.ProfileID = 99
			}
			close(release)
			if mode == "park" {
				// Observe the actual completed original response, return it to its
				// private channel, then verify parked UI consumption is deferred.
				select {
				case result := <-d.edit.Results:
					out := make(chan mysqlEditResult, 1)
					out <- result
					d.edit.Results = out
				case <-time.After(5 * time.Second):
					t.Fatal("parked write did not complete")
				}
				s.Consume(ui.Services{})
				if !d.edit.Busy || !d.HasDraft() || d.nativeReceipt.JSON != "" {
					t.Fatal("parked write modified the grid before original activation")
				}
				active.ProfileID = object.ProfileID
			}
			waitMutationSave(t, s, d)
			if d.page != page || submits.Load() != 1 || d.nativeReceipt.JSON != `{"original_sequential_batch":"猫"}` || d.nativeReceipt.Presentation == nil || d.nativeReceipt.Presentation.Title != "原生顺序写入回执" {
				t.Fatal("native response lost original ownership or presentation", d.edit.Err)
			}
			if mode == "success" || mode == "park" {
				if d.HasDraft() || d.edit.Uncertain != nil || d.edit.Err != "" {
					t.Fatal("complete native acknowledgements required a fabricated transaction", d.edit.Err)
				}
			} else {
				if !d.HasDraft() || d.edit.Uncertain == nil || mode != "fake-commit" && d.edit.Uncertain.Sequential == nil || s.CanSave(d) || !s.GuardLeave(d, nil) || s.verifierFor(d) != nil {
					t.Fatal("failed or unrelated native response cleared original drafts or allowed replay", d.edit.Err)
				}
				s.Save(ui.Services{}, d)
				s.verifyMySQLDataSave(ui.Services{}, d)
				if submits.Load() != 1 || verifies.Load() != 0 || d.edit.Results != nil {
					t.Fatal("native unknown receipt was retried or replaced by a read")
				}
			}
			if len(d.tools.history) != 3 {
				t.Fatal("original per-statement history lost")
			}
			if mode == "native-failure" || mode == "unknown" || mode == "cancel" {
				if d.tools.history[2].Failed || d.tools.history[2].Rows != 1 || d.tools.history[2].Message != locales.Text("原生语句已确认（无批次事务）") || !d.tools.history[1].Failed || d.tools.history[1].Rows != 0 || d.tools.history[0].Message != locales.Text("原生语句未发送") {
					t.Fatal("failed batch history erased acknowledged prefix or called it a transaction", d.tools.history)
				}
			}
			select {
			case result := <-activities:
				if (mode == "native-failure" || mode == "partial-no-error") && (result.State != "partial" || result.AffectedRows != 1) || (mode == "unknown" || mode == "unknown-no-error" || mode == "cancel") && (result.State != "uncertain" || result.AffectedRows != 1) || (mode == "wrong-object" || mode == "wrong-original-prefix" || mode == "fake-commit") && (result.State != "uncertain" || result.AffectedRows != 0) {
					t.Fatal("native activity discarded acknowledged prefix", result)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("native activity completion lost")
			}
		})
	}
}

func TestDataSequentialUnknownReceiptCannotBeResolvedByLaterStateEquality(t *testing.T) {
	for _, status := range []string{"applied", "not_applied"} {
		t.Run(status, func(t *testing.T) {
			s, d, active := mutationWorkspace(t)
			// The original receipt itself must prevent recovery even if a later
			// provider accidentally omits SequentialWrites or exposes Verify.
			original := dataSequentialTestResult(d.object, []contract.DataEdit{{Kind: "update"}}, contract.DataWriteUnknown)
			d.edit.Uncertain = cloneEditResult(original)
			d.edit.UncertainKeys = []string{"original"}
			d.edit.Drafts = map[string]*mysqlRowDraft{"original": {Key: "original", Kind: "update"}}
			s.rememberNativeWriteReceipt(d, original.NativeJSON, true)
			captured := cloneEditResult(d.edit.Uncertain)
			var calls atomic.Int32
			s.Verify = func(context.Context, int64, string, string, *contract.DataEditResult) (*contract.DataVerification, error) {
				calls.Add(1)
				return nil, nil
			}
			s.verifyMySQLDataSave(ui.Services{}, d)
			if calls.Load() != 0 {
				t.Fatal("original unknown write dispatched state-equality verification")
			}
			out := make(chan mysqlEditResult, 1)
			out <- mysqlEditResult{Profile: d.profileID, Generation: active.Generation, Request: d.edit.Request, Saved: &contract.DataEditResult{NativeJSON: `{"later_read":true}`}, Verification: &contract.DataVerification{Status: status, Recovered: &contract.DataEditResult{Verified: true, CommitConfirmed: true}}}
			d.edit.Results, d.edit.Busy = out, true
			s.Consume(ui.Services{})
			if !reflect.DeepEqual(d.edit.Uncertain, captured) || d.nativeReceipt.JSON != original.NativeJSON || !d.HasDraft() || s.CanSave(d) {
				t.Fatal("later state equality replaced original unknown receipt or unblocked replay", status)
			}
		})
	}
}

func TestDataSequentialReceiptPresentationOwnsOriginalTextAndExportsCompleteEvidence(t *testing.T) {
	for _, language := range []string{"zh-Hans", "en"} {
		t.Run(language, func(t *testing.T) {
			i18n.SetLocaleFunc(func() string { return language })
			defer i18n.SetLocaleFunc(nil)
			s, d, _ := mutationWorkspace(t)
			d.boundRuntime.SequentialWrites = true
			custom := &contract.NativeWriteReceiptPresentation{Title: "原生顺序写入回执"}
			d.boundRuntime.ReceiptPresentation = custom
			s.rememberNativeWriteReceipt(d, `{"original":"猫"}`, true)
			custom.Title = "replacement provider label"
			d.boundRuntime.SequentialWrites = false
			d.nativeReceipt.Open = true
			tt := ui.NewTester(func(c *ui.Context) { s.nativeWriteReceiptModal(c, d) }, 1280, 800)
			tt.Frame()
			if !tt.HasText(locales.Text("原生顺序写入回执")) || !tt.HasText(locales.Text("各语句独立写入；已确认的前缀会保留，后续失败不会回滚。未知回执禁止重放，后续读取不能替代原始确认。")) || tt.HasText(locales.Text("原生事务回执")) {
				t.Fatal("original sequential receipt presented transaction or replacement semantics")
			}
			if directory := os.Getenv("NYADB_DATA_SEQUENTIAL_SCREENSHOTS"); directory != "" {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
				file, err := os.Create(filepath.Join(directory, language+"-receipt.png"))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(file, tt.Image())
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Fatal(errors.Join(err, closeErr))
				}
			}
			path := filepath.Join(t.TempDir(), "original.json")
			s.exportNativeWriteReceipt(ui.Services{}, d, func(options mygo.SaveDialogOptions) (string, error) {
				if options.DefaultPath != "write-receipt.json" {
					t.Error("sequential export retained transaction filename")
				}
				return path, nil
			})
			d.nativeReceipt.JSON = `{"replacement":true}`
			select {
			case err := <-d.nativeReceipt.Results:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original native receipt export stalled")
			}
			d.nativeReceipt.Cancel()
			d.nativeReceipt.Cancel, d.nativeReceipt.Results = nil, nil
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != `{"original":"猫"}` {
				t.Fatal("native export replaced original receipt bytes", err)
			}
		})
	}
}
