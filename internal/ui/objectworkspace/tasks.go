package objectworkspace

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type taskDocument struct {
	management                                                                                                   taskManagementState
	selection                                                                                                    ui.Selection[[4]string]
	lease                                                                                                        contract.TaskDocumentLease
	generation                                                                                                   uint64
	query                                                                                                        contract.TaskQuery
	cursors                                                                                                      []string
	page                                                                                                         *contract.TaskPage
	selected                                                                                                     int
	detail                                                                                                       *contract.TaskRecord
	uids, indexes, status, kind, limit, order                                                                    string
	filtersOpen                                                                                                  bool
	failureRaw                                                                                                   string
	batch, canceledBy, beforeEnqueued, afterEnqueued, beforeStarted, afterStarted, beforeFinished, afterFinished string
	tab                                                                                                          int
	previewTab                                                                                                   int
	previewRaw                                                                                                   string
	previewVisible                                                                                               bool
	cancelRequested, lost, fresh                                                                                 bool
	results                                                                                                      <-chan taskResult
}
type taskResult struct {
	request, generation uint64
	lease               contract.TaskDocumentLease
	operation           string
	page                contract.TaskPage
	record              contract.TaskRecord
	err                 error
	mutation            contract.TaskMutationResult
}

func taskObject(o contract.ObjectRef) bool {
	return o.ProfileID > 0 && o.Kind == "tasks" && o.Name == "tasks" && o.Schema == ""
}
func (s *State) TaskScopeSupported(o contract.ObjectRef) bool {
	if !taskObject(o) || s.deps.TaskDocumentsFor == nil {
		return false
	}
	provider, err := s.deps.TaskDocumentsFor(o.ProfileID)
	return err == nil && provider != nil && provider.SupportsTaskDocument(o)
}
func (s *State) OpenTasks(id uint64, o contract.ObjectRef) *Document {
	if !taskObject(o) || !s.TaskScopeSupported(o) {
		return nil
	}
	if old := s.documents[id]; old != nil {
		s.Close(old)
	}
	name := o.Catalog
	d := &Document{id: id, profileID: o.ProfileID, database: name, table: o.Name, object: o, objectKind: "任务"}
	d.tasks = &taskDocument{query: contract.TaskQuery{Limit: 20}, cursors: []string{""}, selected: -1, limit: "20", status: "全部状态", order: "最新任务优先"}
	s.documents[id] = d
	return d
}
func (s *State) reserveTasks(d *Document) error {
	if d == nil || d.closed || d.tasks == nil || s.deps.TaskDocumentsFor == nil {
		return contract.ErrUnsupported
	}
	snapshot := s.sessionFor(d.profileID)
	if !snapshot.Connected || snapshot.ProfileID != d.profileID || s.session().ProfileID != d.profileID {
		return contract.ErrRevoked
	}
	provider, err := s.deps.TaskDocumentsFor(d.profileID)
	if err != nil {
		return err
	}
	lease, err := provider.ReserveTaskDocument(d.object)
	if err != nil {
		return err
	}
	d.tasks.lease, d.tasks.generation = lease, snapshot.Generation
	return nil
}
func cloneTaskQuery(q contract.TaskQuery) contract.TaskQuery {
	q.UIDs = slices.Clone(q.UIDs)
	q.Indexes = slices.Clone(q.Indexes)
	q.Statuses = slices.Clone(q.Statuses)
	q.Types = slices.Clone(q.Types)
	q.BatchUIDs = slices.Clone(q.BatchUIDs)
	q.CanceledBy = slices.Clone(q.CanceledBy)
	return q
}
func (s *State) taskReady(d *Document) bool {
	if d == nil || d.closed || d.tasks == nil || d.loading || d.tasks.lost || s.session().ProfileID != d.profileID {
		return false
	}
	original := s.sessionFor(d.profileID)
	return original.Connected && original.ProfileID == d.profileID && (d.tasks.lease == nil || original.Generation == d.tasks.generation)
}
func (s *State) LoadTasks(d *Document, invalidate func(), refresh bool) {
	if !s.taskReady(d) {
		return
	}
	x := d.tasks
	if !refresh && (x.page != nil || d.err != "") {
		return
	}
	if x.lease == nil {
		if err := s.reserveTasks(d); err != nil {
			d.err = err.Error()
			return
		}
	}
	query := cloneTaskQuery(x.query)
	query.From = x.cursors[len(x.cursors)-1]
	x.fresh = false
	s.runTaskRead(d, invalidate, "page", func(ctx context.Context, lease contract.TaskDocumentLease) (contract.TaskPage, contract.TaskRecord, error) {
		page, err := lease.List(ctx, query)
		return page, contract.TaskRecord{}, err
	})
}
func (s *State) runTaskRead(d *Document, invalidate func(), operation string, run func(context.Context, contract.TaskDocumentLease) (contract.TaskPage, contract.TaskRecord, error)) {
	if !s.taskReady(d) || d.tasks.lease == nil {
		return
	}
	x := d.tasks
	lease, generation := x.lease, x.generation
	d.request++
	request := d.request
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.loading, d.err = cancel, true, ""
	x.cancelRequested = false
	out := make(chan taskResult, 1)
	x.results = out
	go func() {
		page, record, err := run(ctx, lease)
		out <- taskResult{request: request, generation: generation, lease: lease, operation: operation, page: page, record: record, err: err}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) ReadTask(d *Document, invalidate func(), observe bool) {
	if !s.taskReady(d) || !d.tasks.fresh || d.tasks.selected < 0 || d.tasks.page == nil || d.tasks.selected >= len(d.tasks.page.Results) {
		return
	}
	original := d.tasks.page.Results[d.tasks.selected]
	operation := "detail"
	if observe {
		operation = "observe"
	}
	s.runTaskRead(d, invalidate, operation, func(ctx context.Context, lease contract.TaskDocumentLease) (contract.TaskPage, contract.TaskRecord, error) {
		var record contract.TaskRecord
		var err error
		if observe {
			record, err = lease.Observe(ctx, original, 30*time.Second)
		} else {
			record, err = lease.Read(ctx, original)
		}
		return contract.TaskPage{}, record, err
	})
}
func (s *State) SelectTask(d *Document, row int) {
	if d == nil || d.closed || d.tasks == nil || d.tasks.page == nil || row < 0 || row >= len(d.tasks.page.Results) {
		return
	}
	d.tasks.selected = row
	record := d.tasks.page.Results[row]
	d.tasks.detail = &record
}
func (s *State) CancelTasks(d *Document) {
	if d == nil || d.closed || d.tasks == nil || !d.loading || d.tasks.cancelRequested {
		return
	}
	d.tasks.cancelRequested = true
	if d.cancel != nil {
		d.cancel()
	}
}
func (s *State) RecoverTasks(d *Document) {
	if d == nil || d.closed || d.tasks == nil || d.loading || s.session().ProfileID != d.profileID {
		return
	}
	x := d.tasks
	if x.lease != nil && s.deps.TaskDocumentsFor != nil {
		provider, err := s.deps.TaskDocumentsFor(d.profileID)
		if err != nil {
			d.err = err.Error()
			return
		}
		if recovery, ok := provider.(contract.TaskRecoveryProvider); ok {
			r, err := recovery.RecoverTaskDocument(d.object, x.lease)
			if err == nil {
				x.lease = r.Lease
				x.generation = s.sessionFor(d.profileID).Generation
				x.management.receipt = cloneTaskMutationReceipt(r.Receipt)
				if r.Receipt.Raw != "" {
					x.management.last.Receipt = cloneTaskMutationReceipt(r.Receipt)
				}
				x.management.uncertain = x.management.uncertain || r.Uncertain
				x.management.preview = false
				x.management.plan = nil
				x.lost, x.fresh = false, false
				x.cursors = []string{""}
				d.err = "任务连接已恢复；请显式刷新任务或观察原管理任务"
				return
			}
			if !errors.Is(err, contract.ErrUnsupported) || x.management.receipt.Raw != "" || x.management.uncertain {
				d.err = err.Error()
				x.lost = true
				return
			}
		}
		if x.management.receipt.Raw != "" || x.management.uncertain {
			d.err = "原管理任务证据无法恢复；禁止重放请求"
			x.lost = true
			return
		}
	}
	if x.management.receipt.Raw != "" || x.management.uncertain {
		d.err = "原管理任务证据无法恢复；禁止重放请求"
		x.lost = true
		return
	}
	if x.lease != nil {
		_ = x.lease.Close()
		x.lease = nil
	}
	if err := s.reserveTasks(d); err != nil {
		d.err = err.Error()
		x.lost = true
		return
	}
	x.lost, x.fresh = false, false
	x.cursors = []string{""}
	d.err = "任务连接已恢复；请显式刷新任务"
}
func (s *State) TaskPage(d *Document) *contract.TaskPage {
	if d == nil || d.closed || d.tasks == nil || d.tasks.page == nil {
		return nil
	}
	page := *d.tasks.page
	page.Results = slices.Clone(page.Results)
	return &page
}
func (s *State) TaskDetail(d *Document) *contract.TaskRecord {
	if d == nil || d.closed || d.tasks == nil || d.tasks.detail == nil {
		return nil
	}
	record := *d.tasks.detail
	return &record
}
func (s *State) TaskCancelRequested(d *Document) bool {
	return d != nil && d.tasks != nil && d.tasks.cancelRequested
}
func (s *State) TaskConnectionLost(d *Document) bool {
	return d != nil && d.tasks != nil && d.tasks.lost
}
func (s *State) consumeTasks() {
	active := s.session()
	for _, d := range s.documents {
		if d.closed || d.tasks == nil || d.profileID != active.ProfileID {
			continue
		}
		x := d.tasks
		current := s.sessionFor(d.profileID)
		if x.lease != nil && (current.Generation != x.generation || !current.Connected) {
			x.lost = true
			x.fresh = false
			if !d.loading {
				d.err = "原任务会话已失效，请显式恢复"
			}
		}
		if x.results == nil {
			continue
		}
		select {
		case result := <-x.results:
			if result.request != d.request {
				continue
			}
			x.results = nil
			d.loading = false
			if d.cancel != nil {
				d.cancel()
				d.cancel = nil
			}
			if s.consumeTaskManagement(d, result, current) {
				continue
			}
			if result.generation != current.Generation || !current.Connected {
				d.err = "原任务会话已失效，请显式恢复"
				x.lost = true
				x.fresh = false
				continue
			}
			if errors.Is(result.err, contract.ErrTaskLeaseLost) || errors.Is(result.err, contract.ErrRevoked) {
				x.lost = true
				x.fresh = false
			}
			if x.cancelRequested {
				x.cancelRequested = false
				d.err = "任务观察已停止；服务器任务状态需显式读取"
				continue
			}
			if result.operation == "page" && result.err == nil {
				page := result.page
				x.page = &page
				x.fresh = true
				x.selected = -1
				x.detail = nil
				d.columns.Selection = nil
				x.selection.Clear()
				d.columns.Selected = nil
			} else if result.record.Token != "" {
				record := result.record
				if x.page != nil && x.selected >= 0 && x.selected < len(x.page.Results) && x.page.Results[x.selected].UID == record.UID {
					x.detail = &record
				}
				if x.page != nil {
					for i, old := range x.page.Results {
						if old.UID == record.UID && old.Index == record.Index && old.EnqueuedAt == record.EnqueuedAt && old.Type == record.Type {
							x.page.Results[i] = record
							break
						}
					}
				}
			}
			if result.err != nil {
				var diagnostic *contract.TaskRequestError
				if errors.As(result.err, &diagnostic) && diagnostic.Raw != "" {
					x.failureRaw = diagnostic.Raw
				}
				if errors.Is(result.err, context.DeadlineExceeded) {
					d.err = "任务观察超时；服务器任务可能仍在执行"
				} else {
					d.err = result.err.Error()
				}
				// Full native failure evidence remains separately copyable; it never
				// replaces cached successful rows with an empty response.
				if result.operation == "page" && result.page.Raw != "" {
					x.failureRaw = result.page.Raw
				}
			} else {
				x.failureRaw = ""
			}
		default:
		}
	}
}
func taskFilterList(raw string, ids bool) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 256 {
		return nil, errors.New("任务筛选最多允许 256 个值")
	}
	seen := map[string]bool{}
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return nil, errors.New("任务筛选不能包含空值或重复值")
		}
		if ids {
			n, err := strconv.ParseUint(p, 10, 32)
			if err != nil || strconv.FormatUint(n, 10) != p {
				return nil, errors.New("任务 UID 必须为精确的非负整数")
			}
		}
		seen[p] = true
		parts[i] = p
	}
	return parts, nil
}
func (s *State) ResetTaskFilters(d *Document, invalidate func()) {
	if !s.taskReady(d) {
		return
	}
	x := d.tasks
	x.uids, x.indexes, x.kind, x.batch, x.canceledBy = "", "", "", "", ""
	x.beforeEnqueued, x.afterEnqueued, x.beforeStarted, x.afterStarted, x.beforeFinished, x.afterFinished = "", "", "", "", "", ""
	x.status, x.limit, x.order = "全部状态", "20", "最新任务优先"
	s.ApplyTaskFilters(d, invalidate)
}

func (s *State) ApplyTaskFilters(d *Document, invalidate func()) {
	if !s.taskReady(d) {
		return
	}
	x := d.tasks
	q := contract.TaskQuery{Reverse: x.order == "最早任务优先", BeforeEnqueuedAt: x.beforeEnqueued, AfterEnqueuedAt: x.afterEnqueued, BeforeStartedAt: x.beforeStarted, AfterStartedAt: x.afterStarted, BeforeFinishedAt: x.beforeFinished, AfterFinishedAt: x.afterFinished}
	n, err := strconv.Atoi(x.limit)
	if err != nil || n < 1 || n > 1000 {
		d.err = "任务页大小必须为 1–1000"
		return
	}
	q.Limit = n
	for _, entry := range []struct {
		raw string
		ids bool
		out *[]string
	}{{x.uids, true, &q.UIDs}, {x.indexes, false, &q.Indexes}, {x.kind, false, &q.Types}, {x.batch, true, &q.BatchUIDs}, {x.canceledBy, true, &q.CanceledBy}} {
		*entry.out, err = taskFilterList(entry.raw, entry.ids)
		if err != nil {
			d.err = err.Error()
			return
		}
	}
	if x.status != "全部状态" && x.status != "" {
		q.Statuses = []string{x.status}
	}
	x.query = q
	x.cursors = []string{""}
	s.LoadTasks(d, invalidate, true)
}
func (s *State) TaskNext(d *Document, invalidate func()) {
	if !s.taskReady(d) || !d.tasks.fresh || d.tasks.page == nil || d.tasks.page.Next == "" {
		return
	}
	x := d.tasks
	x.cursors = append(x.cursors, x.page.Next)
	s.LoadTasks(d, invalidate, true)
}
func (s *State) TaskPrevious(d *Document, invalidate func()) {
	if !s.taskReady(d) || !d.tasks.fresh || len(d.tasks.cursors) < 2 {
		return
	}
	x := d.tasks
	x.cursors = x.cursors[:len(x.cursors)-1]
	s.LoadTasks(d, invalidate, true)
}
func (s *State) closeTasks(d *Document) {
	if d.tasks == nil {
		return
	}
	x := d.tasks
	x.management.next = nil
	x.management.plan = nil
	if d.cancel != nil {
		d.cancel()
	}
	if x.lease != nil {
		_ = x.lease.Close()
	}
	x.page, x.detail, x.lease, x.results = nil, nil, nil, nil
	d.tasks = nil
}
