package dataworkspace

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDataSequentialWriteWithoutSavedRowsRequiresCompleteOriginalReceipts(t *testing.T) {
	for _, kind := range []string{"insert", "update", "copy"} {
		for _, mode := range []string{"complete", "no-refresh", "no-receipt", "malformed", "wrong-original", "wrong-object", "wrong-destination", "fake-commit"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, d, _ := mutationWorkspace(t)
				_ = s
				edits := []contract.DataEdit{{Kind: kind, Values: map[string]contract.DataValue{"note": {Kind: contract.Text, Text: "native new value"}}}}
				if kind != "insert" {
					edits[0].Original = cloneSnapshot(d.page.RowSnapshots[0])
				}
				r := dataSequentialTestResult(d.object, edits, contract.DataWriteAcknowledged)
				r.Outcomes[0].Saved, r.RefreshRequired = nil, true
				destinations := []contract.ObjectRef{d.object}
				switch mode {
				case "no-refresh":
					r.RefreshRequired = false
				case "no-receipt":
					r.Sequential = nil
				case "malformed":
					r.Sequential.Statements[0].NativeJSON = "invalid"
				case "wrong-original":
					r.Outcomes[0].Original = &contract.RowSnapshot{Metadata: "foreign"}
				case "wrong-object":
					r.Sequential.Object.Name = "foreign"
				case "wrong-destination":
					r.Sequential.Statements[0].Destination.Name = "foreign"
				case "fake-commit":
					r.CommitConfirmed = true
				}
				valid := dataMutationReceiptComplete(d.object, edits, r) && dataMutationDestinationsMatch(destinations, r)
				if valid != (mode == "complete" && kind != "copy") {
					t.Fatal("incomplete or foreign receipts authorized missing saved identity", valid, r)
				}
			})
		}
	}
}

func TestDataSequentialAcknowledgementRefreshesOnceBeforeContinuation(t *testing.T) {
	for _, mode := range []string{"success", "park", "read-error", "cancel-read", "generation"} {
		t.Run(mode, func(t *testing.T) {
			s, d, active := mutationWorkspace(t)
			d.boundRuntime.SequentialWrites = true
			d.boundRuntime.PreviewEditKinds = []string{"update"}
			d.boundRuntime.PageCancellationAcknowledged = true
			fresh := cloneEditingPage(d.page)
			fresh.Rows[0][1].Text = "actual later native read"
			var reads, submits, continued atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			s.AdvancedLoader = func(ctx context.Context, _ int64, _, _ string, options contract.DataOptions) (*contract.DataPage, error) {
				if reads.Add(1) == 1 {
					close(entered)
				}
				<-release
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if mode == "read-error" {
					return nil, errors.New("original native read failed")
				}
				page := cloneEditingPage(fresh)
				page.Offset, page.Limit = options.Offset, options.Limit
				return page, nil
			}
			d.boundRuntime.PreviewEdits = func(_ context.Context, _ int64, _, _ string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
				return mutationPlanFor(d, edits), nil
			}
			d.boundRuntime.SubmitEdits = func(_ context.Context, _ int64, _, _ string, plan contract.DataMutationPlan) (*contract.DataEditResult, error) {
				submits.Add(1)
				r := dataSequentialTestResult(d.object, plan.Snapshot().Edits, contract.DataWriteAcknowledged)
				r.Outcomes[0].Saved, r.RefreshRequired = nil, true
				return r, nil
			}
			s.setMySQLDataCell(ui.Services{}, d, 0, 1, contract.DataValue{Kind: contract.Text, Text: "approved original write"})
			s.Save(ui.Services{}, d)
			waitMutation(t, s, d)
			d.edit.AfterSave = func(ui.Services) {
				continued.Add(1)
				if d.loading || d.page.Rows[0][1].Text != "actual later native read" {
					t.Error("continuation opened stale grid")
				}
			}
			s.confirmDataMutation(ui.Services{}, d)
			waitMutationSave(t, s, d)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("acknowledged write did not start owned refresh")
			}
			if d.HasDraft() || d.edit.Uncertain != nil || !d.loading || reads.Load() != 1 || continued.Load() != 0 {
				t.Fatal("receipt required fabricated saved values or continued before refresh", d.edit.Err)
			}
			if mode == "cancel-read" {
				s.Cancel(d)
			}
			if mode == "generation" {
				active.Generation++
			}
			if mode == "park" {
				active.ProfileID = 99
			}
			once.Do(func() { close(release) })
			if mode == "park" {
				select {
				case result := <-d.results:
					out := make(chan mysqlDataResult, 1)
					out <- result
					d.results = out
				case <-time.After(5 * time.Second):
					t.Fatal("parked original refresh did not finish")
				}
				s.Consume(ui.Services{})
				if !d.loading || continued.Load() != 0 {
					t.Fatal("parked refresh changed inactive document")
				}
				active.ProfileID = d.profileID
			}
			deadline := time.Now().Add(5 * time.Second)
			for d.loading && time.Now().Before(deadline) {
				s.Consume(ui.Services{})
				time.Sleep(time.Millisecond)
			}
			if d.loading || submits.Load() != 1 || reads.Load() != 1 || d.edit.AfterRefresh != nil {
				t.Fatal("original refresh duplicated reads or retained continuation")
			}
			want := int32(0)
			if mode == "success" || mode == "park" {
				want = 1
			}
			if continued.Load() != want {
				t.Fatal("continuation ignored original successful refresh", continued.Load(), want)
			}
		})
	}
}
