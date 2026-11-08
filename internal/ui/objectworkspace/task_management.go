package objectworkspace

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type taskManagementState struct {
	plan                                          *contract.TaskMutationPlan
	receipt                                       contract.TaskMutationReceipt
	last                                          contract.TaskMutationResult
	preview, guard, busy, uncertain, acknowledged bool
	next                                          func(ui.Services)
}

func cloneTaskMutationReceipt(r contract.TaskMutationReceipt) contract.TaskMutationReceipt {
	r.Targets = slices.Clone(r.Targets)
	return r
}
func cloneTaskMutationResult(r contract.TaskMutationResult) contract.TaskMutationResult {
	r.Receipt = cloneTaskMutationReceipt(r.Receipt)
	r.Targets = slices.Clone(r.Targets)
	return r
}
func taskSelectionKey(r contract.TaskRecord) [4]string {
	return [4]string{r.UID, r.Index, r.Type, r.EnqueuedAt}
}
func (s *State) TaskSelectionRows(d *Document) []int {
	if d == nil || d.closed || d.tasks == nil || d.tasks.page == nil {
		return nil
	}
	x := d.tasks
	rows := []int{}
	for i, r := range x.page.Results {
		if x.selection.Has(taskSelectionKey(r)) {
			rows = append(rows, i)
		}
	}
	if len(rows) == 0 && x.selected >= 0 && x.selected < len(x.page.Results) {
		rows = append(rows, x.selected)
	}
	return rows
}
func (s *State) CanManageTasks(d *Document) bool {
	if !s.taskReady(d) || !d.tasks.fresh || d.tasks.management.uncertain || d.tasks.management.receipt.Raw != "" {
		return false
	}
	p, ok := d.tasks.lease.(contract.TaskManagementLease)
	return ok && p.SupportsTaskManagement()
}
func (s *State) PrepareTaskManagement(d *Document, action string, rows []int) error {
	if !s.CanManageTasks(d) {
		return contract.ErrTaskMutationUsed
	}
	if rows == nil {
		rows = s.TaskSelectionRows(d)
	}
	if len(rows) == 0 || len(rows) > 256 {
		d.err = "请选择 1–256 个原任务"
		return contract.ErrTaskMutationState
	}
	x := d.tasks
	records := make([]contract.TaskRecord, 0, len(rows))
	seen := map[int]bool{}
	for _, i := range rows {
		if x.page == nil || i < 0 || i >= len(x.page.Results) || seen[i] {
			return contract.ErrTaskMutationState
		}
		seen[i] = true
		records = append(records, x.page.Results[i])
	}
	p, err := x.lease.(contract.TaskManagementLease).PrepareTaskMutation(action, records)
	if err != nil {
		d.err = err.Error()
		return err
	}
	p.Records = slices.Clone(p.Records)
	x.management.plan = &p
	x.management.preview = true
	x.management.acknowledged = false
	d.err = ""
	return nil
}
func (s *State) runTaskManagement(d *Document, invalidate func(), operation string, run func(context.Context, contract.TaskManagementLease) (contract.TaskMutationResult, error)) {
	if !s.taskReady(d) || d.tasks.lease == nil {
		return
	}
	lease := d.tasks.lease
	p, ok := lease.(contract.TaskManagementLease)
	if !ok || !p.SupportsTaskManagement() {
		return
	}
	x := d.tasks
	request, generation := d.request+1, x.generation
	d.request = request
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.loading, d.err = cancel, true, ""
	x.management.busy = true
	x.management.acknowledged = false
	x.cancelRequested = false
	out := make(chan taskResult, 1)
	x.results = out
	go func() {
		result, err := run(ctx, p)
		out <- taskResult{request: request, generation: generation, lease: lease, operation: operation, mutation: cloneTaskMutationResult(result), err: err}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) SubmitTaskManagement(d *Document, invalidate func()) {
	if !s.CanManageTasks(d) || d.tasks.management.plan == nil || !d.tasks.management.preview {
		return
	}
	p := *d.tasks.management.plan
	p.Records = slices.Clone(p.Records)
	d.tasks.management.plan = nil
	d.tasks.management.preview = false
	d.tasks.fresh = false
	s.runTaskManagement(d, invalidate, "mutate", func(ctx context.Context, l contract.TaskManagementLease) (contract.TaskMutationResult, error) {
		return l.MutateTasks(ctx, p)
	})
}
func (s *State) ObserveTaskManagement(d *Document, invalidate func()) {
	if !s.taskReady(d) || d.tasks.management.receipt.Raw == "" {
		return
	}
	r := cloneTaskMutationReceipt(d.tasks.management.receipt)
	s.runTaskManagement(d, invalidate, "manage-observe", func(ctx context.Context, l contract.TaskManagementLease) (contract.TaskMutationResult, error) {
		return l.ObserveTaskMutation(ctx, r, 30*time.Second)
	})
}
func (s *State) TaskManagementResult(d *Document) contract.TaskMutationResult {
	if d == nil || d.closed || d.tasks == nil {
		return contract.TaskMutationResult{}
	}
	return cloneTaskMutationResult(d.tasks.management.last)
}
func (s *State) TaskManagementUncertain(d *Document) bool {
	return d != nil && !d.closed && d.tasks != nil && d.tasks.management.uncertain
}
func (s *State) consumeTaskManagement(d *Document, reply taskResult, current Session) bool {
	if reply.operation != "mutate" && reply.operation != "manage-observe" {
		return false
	}
	x := d.tasks
	m := &x.management
	m.busy = false
	result := cloneTaskMutationResult(reply.mutation)
	if result.Request == "" && reply.operation == "manage-observe" {
		result.Request = m.last.Request
	}
	m.last = result
	if reply.mutation.Receipt.Raw != "" {
		m.receipt = cloneTaskMutationReceipt(reply.mutation.Receipt)
	}
	if reply.mutation.State == "uncertain" {
		m.uncertain = true
	}
	canceled := x.cancelRequested
	x.cancelRequested = false
	var diagnostic *contract.TaskRequestError
	if errors.As(reply.err, &diagnostic) && diagnostic.Raw != "" {
		x.failureRaw = diagnostic.Raw
	}
	if current.Generation != reply.generation || !current.Connected {
		x.lost, x.fresh = true, false
		d.err = "原任务会话已失效；管理回执保留，请显式恢复"
		return true
	}
	if errors.Is(reply.err, contract.ErrTaskLeaseLost) || errors.Is(reply.err, contract.ErrRevoked) {
		x.lost, x.fresh = true, false
	}
	if reply.err != nil {
		d.err = reply.err.Error()
	} else if reply.operation == "mutate" && reply.mutation.State == "enqueued" {
		d.err = "管理请求已入队；请显式观察原管理任务"
	} else if reply.operation == "manage-observe" && (reply.mutation.State == "succeeded" || reply.mutation.State == "failed" || reply.mutation.State == "canceled") {
		m.receipt = contract.TaskMutationReceipt{}
		x.fresh = false
		d.err = "原管理任务已结束；请显式刷新任务列表"
	}
	if errors.Is(reply.err, contract.ErrTaskMutationUnavailable) {
		d.err = "原管理任务暂不可见；可能权限不足或记录已删除。入队回执保留，请在原服务器确认"
	}
	if canceled {
		d.err = "已停止等待；原管理任务仍需显式确认"
	}
	if reply.mutation.State == "uncertain" {
		d.err = "管理提交结果未知；证据保留，禁止重放"
	}
	return true
}
func taskManagementPending(d *Document) bool {
	if d == nil || d.closed || d.tasks == nil {
		return false
	}
	m := &d.tasks.management
	return m.preview || m.guard || m.busy || m.receipt.Raw != "" || m.uncertain
}
func (s *State) guardTaskManagementLeave(d *Document, next func(ui.Services)) bool {
	if !taskManagementPending(d) || d.tasks.management.acknowledged && !d.loading {
		return false
	}
	m := &d.tasks.management
	if s.deps.ActivateDesign != nil {
		s.deps.ActivateDesign(d)
	}
	m.guard = true
	m.next = next
	return true
}
