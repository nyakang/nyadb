package objectworkspace

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type taskManagementTestLease struct {
	*taskTestLease
	mutate          func(context.Context, contract.TaskMutationPlan) (contract.TaskMutationResult, error)
	observeMutation func(context.Context, contract.TaskMutationReceipt, time.Duration) (contract.TaskMutationResult, error)
	prepares        atomic.Int32
}

func (l *taskManagementTestLease) SupportsTaskManagement() bool { return true }
func (l *taskManagementTestLease) PrepareTaskMutation(action string, records []contract.TaskRecord) (contract.TaskMutationPlan, error) {
	l.prepares.Add(1)
	return contract.TaskMutationPlan{Action: action, Records: records, Request: "DELETE /tasks?uids=1,2&indexUids=owned", Token: "original-plan"}, nil
}
func (l *taskManagementTestLease) MutateTasks(c context.Context, p contract.TaskMutationPlan) (contract.TaskMutationResult, error) {
	return l.mutate(c, p)
}
func (l *taskManagementTestLease) ObserveTaskMutation(c context.Context, r contract.TaskMutationReceipt, b time.Duration) (contract.TaskMutationResult, error) {
	return l.observeMutation(c, r, b)
}
func taskManagementTestState(t *testing.T) (*State, *Session, *Document, *taskManagementTestLease) {
	t.Helper()
	base := &taskTestLease{list: func(context.Context, contract.TaskQuery) (contract.TaskPage, error) {
		return contract.TaskPage{Results: []contract.TaskRecord{testTaskRecord("1"), testTaskRecord("2")}, Total: 2}, nil
	}}
	s, current, d := taskTestState(t, base)
	s.LoadTasks(d, nil, false)
	waitTasks(t, s, d)
	l := &taskManagementTestLease{taskTestLease: base}
	d.tasks.lease = l
	return s, current, d, l
}
func taskManagementTestReceipt() contract.TaskMutationReceipt {
	return contract.TaskMutationReceipt{UID: "8", Raw: `{"taskUid":8,"future":9007199254740993}`, Token: "original-receipt", Targets: []string{"1", "2"}}
}
func TestTaskManagementSelectionPreviewParkedAdmissionAndLeaveGuard(t *testing.T) {
	s, current, d, l := taskManagementTestState(t)
	d.tasks.selection.Add(taskSelectionKey(d.tasks.page.Results[0]))
	d.tasks.selection.Add(taskSelectionKey(d.tasks.page.Results[1]))
	if rows := s.TaskSelectionRows(d); len(rows) != 2 {
		t.Fatal("multi selection not captured", rows)
	}
	if err := s.PrepareTaskManagement(d, "delete", nil); err != nil || l.prepares.Load() != 1 {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 720, 520)
	if !tt.HasText("删除原任务记录预览") || !tt.HasText("仅操作以下 2 个原任务 UID") {
		t.Fatal("concrete task preview missing")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var submits atomic.Int32
	l.mutate = func(_ context.Context, p contract.TaskMutationPlan) (contract.TaskMutationResult, error) {
		submits.Add(1)
		close(entered)
		<-release
		if p.Records[0].UID != "1" {
			t.Error("worker request mutated")
		}
		return contract.TaskMutationResult{State: "enqueued", Request: p.Request, Receipt: taskManagementTestReceipt(), Targets: []string{"1", "2"}}, nil
	}
	s.SubmitTaskManagement(d, nil)
	<-entered
	if !d.DesignBusy() || !d.DesignLeavePending() || s.CanManageTasks(d) {
		t.Fatal("pending submission not guarded")
	}
	current.ProfileID = 9
	close(release)
	time.Sleep(10 * time.Millisecond)
	s.Consume(ui.Services{})
	if !d.Loading() || s.TaskManagementResult(d).Receipt.Raw != "" {
		t.Fatal("parked receipt consumed by foreign profile")
	}
	current.ProfileID = 7
	waitTasks(t, s, d)
	result := s.TaskManagementResult(d)
	if result.State != "enqueued" || result.CountsKnown || result.Receipt.Raw == "" || submits.Load() != 1 || s.CanManageTasks(d) {
		t.Fatal("admission mistaken for completion")
	}
	result.Receipt.Targets[0] = "changed"
	result.Targets[0] = "changed"
	if s.TaskManagementResult(d).Receipt.Targets[0] != "1" || s.TaskManagementResult(d).Targets[0] != "1" {
		t.Fatal("mutable receipt exported")
	}
	if !s.GuardDesignLeave(d, nil) {
		t.Fatal("pending original receipt not guarded")
	}
	raw := string(s.DocumentSnapshot(d))
	if strings.Contains(raw, "original-receipt") || strings.Contains(raw, "9007199254740993") || strings.Contains(raw, "DELETE /tasks") {
		t.Fatal("workspace persisted management evidence")
	}
	s.Close(d)
	if l.closes.Load() != 1 || d.tasks != nil {
		t.Fatal("close leaked original management lease")
	}
}
func TestTaskManagementCancellationAcknowledgementAndGenerationEvidence(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "admission", true: "unknown"}[uncertain], func(t *testing.T) {
			s, current, d, l := taskManagementTestState(t)
			if err := s.PrepareTaskManagement(d, "cancel", []int{0}); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			l.mutate = func(c context.Context, p contract.TaskMutationPlan) (contract.TaskMutationResult, error) {
				close(entered)
				<-c.Done()
				<-release
				if uncertain {
					return contract.TaskMutationResult{State: "uncertain", Request: p.Request, Targets: []string{"1"}}, errors.Join(contract.ErrTaskLeaseLost, context.Canceled)
				}
				return contract.TaskMutationResult{State: "enqueued", Receipt: taskManagementTestReceipt()}, nil
			}
			s.SubmitTaskManagement(d, nil)
			<-entered
			s.CancelTasks(d)
			if !d.Loading() || !d.DesignBusy() || !s.TaskCancelRequested(d) {
				t.Fatal("cancel settled without executor acknowledgement")
			}
			current.Generation++
			close(release)
			waitTasks(t, s, d)
			if !s.TaskConnectionLost(d) || s.TaskPage(d) == nil || s.TaskManagementUncertain(d) != uncertain {
				t.Fatal("generation loss discarded original evidence")
			}
			if !uncertain && s.TaskManagementResult(d).Receipt.Raw == "" {
				t.Fatal("admitted receipt lost on generation mismatch")
			}
			if uncertain && s.TaskManagementResult(d).Request == "" {
				t.Fatal("unknown request evidence lost")
			}
			if !s.GuardDesignLeave(d, nil) {
				t.Fatal("lost evidence not guarded")
			}
		})
	}
}
func TestTaskManagementExplicitObservationRetainsCountsAndRefreshRequirement(t *testing.T) {
	s, _, d, l := taskManagementTestState(t)
	if err := s.PrepareTaskManagement(d, "delete", []int{0}); err != nil {
		t.Fatal(err)
	}
	l.mutate = func(_ context.Context, p contract.TaskMutationPlan) (contract.TaskMutationResult, error) {
		return contract.TaskMutationResult{State: "enqueued", Request: p.Request, Receipt: taskManagementTestReceipt()}, nil
	}
	var observations atomic.Int32
	l.observeMutation = func(_ context.Context, r contract.TaskMutationReceipt, b time.Duration) (contract.TaskMutationResult, error) {
		observations.Add(1)
		if b != 30*time.Second || r.UID != "8" {
			t.Error("original receipt/bound lost")
		}
		return contract.TaskMutationResult{State: "succeeded", Receipt: r, TaskRaw: `{"uid":8,"details":{"deletedTasks":1}}`, CountsKnown: true, Matched: 1, Changed: 1}, nil
	}
	s.SubmitTaskManagement(d, nil)
	waitTasks(t, s, d)
	if observations.Load() != 0 {
		t.Fatal("automatic management polling")
	}
	s.ObserveTaskManagement(d, nil)
	waitTasks(t, s, d)
	result := s.TaskManagementResult(d)
	if result.State != "succeeded" || result.Changed != 1 || !result.CountsKnown || result.Request == "" || d.tasks.management.receipt.Raw != "" || s.CanManageTasks(d) {
		t.Fatal("completion/counts or refresh requirement lost")
	}
	s.ObserveTaskManagement(d, nil)
	if observations.Load() != 1 {
		t.Fatal("completed receipt observed automatically")
	}
}

func TestTaskManagementUnavailableObservationRetainsGuardedAdmission(t *testing.T) {
	s, _, d, l := taskManagementTestState(t)
	if err := s.PrepareTaskManagement(d, "delete", []int{0}); err != nil {
		t.Fatal(err)
	}
	l.mutate = func(_ context.Context, p contract.TaskMutationPlan) (contract.TaskMutationResult, error) {
		return contract.TaskMutationResult{State: "enqueued", Request: p.Request, Receipt: taskManagementTestReceipt()}, nil
	}
	l.observeMutation = func(_ context.Context, r contract.TaskMutationReceipt, _ time.Duration) (contract.TaskMutationResult, error) {
		return contract.TaskMutationResult{State: "enqueued", Receipt: r, TaskRaw: `{"code":"task_not_found"}`}, &contract.TaskRequestError{Raw: `{"code":"task_not_found"}`, Cause: contract.ErrTaskMutationUnavailable}
	}
	s.SubmitTaskManagement(d, nil)
	waitTasks(t, s, d)
	s.ObserveTaskManagement(d, nil)
	waitTasks(t, s, d)
	result := s.TaskManagementResult(d)
	if result.State != "enqueued" || result.CountsKnown || result.Receipt.Raw == "" || s.TaskConnectionLost(d) || s.CanManageTasks(d) || !d.DesignLeavePending() || !strings.Contains(d.Error(), "权限不足") {
		t.Fatal("unavailable original task discarded or completed admission", d.Error(), result)
	}
	if !s.GuardDesignLeave(d, nil) {
		t.Fatal("unavailable original receipt lost leave guard")
	}
}
