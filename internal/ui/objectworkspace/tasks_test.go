package objectworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type taskTestProvider struct {
	reserve func(contract.ObjectRef) (contract.TaskDocumentLease, error)
}

func (p *taskTestProvider) SupportsTaskDocument(o contract.ObjectRef) bool { return taskObject(o) }
func (p *taskTestProvider) ReserveTaskDocument(o contract.ObjectRef) (contract.TaskDocumentLease, error) {
	return p.reserve(o)
}

type taskTestLease struct {
	list    func(context.Context, contract.TaskQuery) (contract.TaskPage, error)
	read    func(context.Context, contract.TaskRecord) (contract.TaskRecord, error)
	observe func(context.Context, contract.TaskRecord, time.Duration) (contract.TaskRecord, error)
	closes  atomic.Int32
}

func (l *taskTestLease) List(c context.Context, q contract.TaskQuery) (contract.TaskPage, error) {
	return l.list(c, q)
}
func (l *taskTestLease) Read(c context.Context, r contract.TaskRecord) (contract.TaskRecord, error) {
	if l.read != nil {
		return l.read(c, r)
	}
	return r, nil
}
func (l *taskTestLease) Observe(c context.Context, r contract.TaskRecord, b time.Duration) (contract.TaskRecord, error) {
	if l.observe != nil {
		return l.observe(c, r, b)
	}
	return r, nil
}
func (l *taskTestLease) Close() error { l.closes.Add(1); return nil }
func testTaskRecord(id string) contract.TaskRecord {
	return contract.TaskRecord{UID: id, Index: "owned", Status: "processing", Type: "documentAdditionOrUpdate", EnqueuedAt: "2026-11-08T08:05:30Z", Raw: `{"uid":` + id + `,"integer":9007199254740993,"text":"原生🐱"}`, Token: "owned-" + id}
}
func taskTestState(t *testing.T, lease *taskTestLease) (*State, *Session, *Document) {
	t.Helper()
	current := &Session{ProfileID: 7, Generation: 8, Connected: true}
	s := New(Dependencies{Session: func() Session { return *current }, SessionFor: func(id int64) Session { return Session{ProfileID: id, Generation: current.Generation, Connected: true} }, TaskDocumentsFor: func(int64) (contract.TaskDocumentsProvider, error) {
		return &taskTestProvider{reserve: func(contract.ObjectRef) (contract.TaskDocumentLease, error) { return lease, nil }}, nil
	}})
	t.Cleanup(s.CloseAll)
	d := s.OpenTasks(1, contract.ObjectRef{ProfileID: 7, Catalog: "owned", Kind: "tasks", Name: "tasks"})
	if d == nil {
		t.Fatal("task document missing")
	}
	return s, current, d
}
func waitTasks(t *testing.T, s *State, d *Document) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for d.Loading() && time.Now().Before(end) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.Loading() {
		t.Fatal("original task read did not settle")
	}
}
func TestTaskDocumentParkedPageOriginalGenerationAndClose(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	lease := &taskTestLease{list: func(context.Context, contract.TaskQuery) (contract.TaskPage, error) {
		close(entered)
		<-release
		return contract.TaskPage{Results: []contract.TaskRecord{testTaskRecord("0")}, Raw: `{"complete":"原生"}`, Total: 1}, nil
	}}
	s, current, d := taskTestState(t, lease)
	s.LoadTasks(d, nil, false)
	<-entered
	current.ProfileID = 9
	close(release)
	time.Sleep(10 * time.Millisecond)
	s.Consume(ui.Services{})
	if !d.Loading() || s.TaskPage(d) != nil {
		t.Fatal("parked task result consumed by another profile")
	}
	current.ProfileID = 7
	waitTasks(t, s, d)
	if d.Error() != "" || s.TaskPage(d).Results[0].UID != "0" {
		t.Fatal("original parked task result lost")
	}
	page := s.TaskPage(d)
	page.Results[0].UID = "edited"
	if s.TaskPage(d).Results[0].UID != "0" {
		t.Fatal("task cache exported writable slice")
	}
	s.SelectTask(d, 0)
	s.Close(d)
	s.Close(d)
	if lease.closes.Load() != 1 || d.tasks != nil || s.TaskPage(d) != nil || len(s.documents) != 0 {
		t.Fatal("task close leaked original lease/cache")
	}
}
func TestTaskCancellationWaitsOriginalExecutorAndExplicitRecovery(t *testing.T) {
	var reads atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	lease := &taskTestLease{list: func(ctx context.Context, q contract.TaskQuery) (contract.TaskPage, error) {
		if reads.Add(1) == 1 {
			return contract.TaskPage{Results: []contract.TaskRecord{testTaskRecord("1")}, Total: 1, Next: "original-cursor"}, nil
		}
		close(entered)
		<-ctx.Done()
		<-release
		return contract.TaskPage{}, errors.Join(contract.ErrTaskLeaseLost, context.Canceled)
	}}
	s, _, d := taskTestState(t, lease)
	s.LoadTasks(d, nil, false)
	waitTasks(t, s, d)
	s.TaskNext(d, nil)
	<-entered
	s.CancelTasks(d)
	if !d.Loading() || !s.TaskCancelRequested(d) || s.TaskPage(d).Results[0].UID != "1" {
		t.Fatal("cancel cleared busy/cache before acknowledgement")
	}
	s.LoadTasks(d, nil, true)
	if reads.Load() != 2 {
		t.Fatal("cancel replayed read")
	}
	close(release)
	waitTasks(t, s, d)
	if !s.TaskConnectionLost(d) || d.Error() == "" || s.TaskPage(d).Results[0].UID != "1" {
		t.Fatal("original canceled read lost cached page")
	}
	s.RecoverTasks(d)
	if reads.Load() != 2 || d.Loading() || d.tasks.fresh || d.tasks.cursors[0] != "" || lease.closes.Load() != 1 {
		t.Fatal("recovery replayed original cursor or read")
	}
}
func TestTaskGenerationMismatchAndSelectionCannotReplaceOtherTask(t *testing.T) {
	lease := &taskTestLease{list: func(context.Context, contract.TaskQuery) (contract.TaskPage, error) {
		return contract.TaskPage{Results: []contract.TaskRecord{testTaskRecord("1"), testTaskRecord("2")}, Total: 2}, nil
	}}
	s, current, d := taskTestState(t, lease)
	s.LoadTasks(d, nil, false)
	waitTasks(t, s, d)
	s.SelectTask(d, 0)
	entered, release := make(chan struct{}), make(chan struct{})
	lease.read = func(_ context.Context, r contract.TaskRecord) (contract.TaskRecord, error) {
		close(entered)
		<-release
		r.Status = "succeeded"
		return r, nil
	}
	s.ReadTask(d, nil, false)
	<-entered
	s.SelectTask(d, 1)
	close(release)
	waitTasks(t, s, d)
	if s.TaskDetail(d).UID != "2" || s.TaskPage(d).Results[0].Status != "succeeded" {
		t.Fatal("old task result replaced newly selected details")
	}
	entered, release = make(chan struct{}), make(chan struct{})
	lease.list = func(context.Context, contract.TaskQuery) (contract.TaskPage, error) {
		close(entered)
		<-release
		return contract.TaskPage{Results: []contract.TaskRecord{testTaskRecord("3")}}, nil
	}
	s.LoadTasks(d, nil, true)
	<-entered
	current.Generation++
	close(release)
	waitTasks(t, s, d)
	if !s.TaskConnectionLost(d) || s.TaskPage(d).Results[0].UID != "1" {
		t.Fatal("late new generation replaced cached original tasks")
	}
}
func TestTaskFiltersExplicitSnapshotAndLocalizedBoundedJSON(t *testing.T) {
	var queries []contract.TaskQuery
	lease := &taskTestLease{list: func(_ context.Context, q contract.TaskQuery) (contract.TaskPage, error) {
		queries = append(queries, cloneTaskQuery(q))
		record := testTaskRecord("0")
		record.Raw = `{"uid":0,"text":"` + strings.Repeat("🐱", 25000) + `"}`
		return contract.TaskPage{Results: []contract.TaskRecord{record}, Total: 1, Raw: `{"exact":9007199254740993}`}, nil
	}}
	s, _, d := taskTestState(t, lease)
	d.tasks.uids = "0,4294967295"
	d.tasks.status = "failed"
	d.tasks.order = "最早任务优先"
	d.tasks.limit = "1"
	s.ApplyTaskFilters(d, nil)
	waitTasks(t, s, d)
	if len(queries) != 1 || queries[0].UIDs[1] != "4294967295" || queries[0].Statuses[0] != "failed" || !queries[0].Reverse {
		t.Fatal("explicit native task filters changed")
	}
	snapshot := s.DocumentSnapshot(d)
	if strings.Contains(string(snapshot), "owned-0") || strings.Contains(string(snapshot), "9007199254740993") || strings.Contains(string(snapshot), "🐱") {
		t.Fatal("snapshot retained original evidence/token")
	}
	fresh := s.OpenTasks(2, d.Object())
	s.RestoreDocumentPresentation(fresh, snapshot)
	if len(queries) != 1 || fresh.tasks.uids != "0,4294967295" || fresh.tasks.query.Limit != 20 || fresh.tasks.results != nil {
		t.Fatal("snapshot replayed saved filters")
	}
	var parsed taskPresentation
	if json.Unmarshal(snapshot, &parsed) != nil {
		t.Fatal("snapshot malformed")
	}
	s.SelectTask(d, 0)
	d.tasks.tab = 1
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 720, 520)
	if !tt.HasText("任务 JSON 预览已截断；可复制完整内容。") || !tt.HasText("任务详情") {
		t.Fatal("bounded native task view unavailable")
	}
	for _, bad := range []string{"01", "9007199254740993", "0,,1", "0,0", "-1"} {
		d.tasks.uids = bad
		s.ApplyTaskFilters(d, nil)
		if len(queries) != 1 {
			t.Fatal("malformed task UID dispatched")
		}
	}
}

func TestTaskIdleGenerationLossKeepsCacheUntilExplicitRecovery(t *testing.T) {
	var reads atomic.Int32
	lease := &taskTestLease{list: func(context.Context, contract.TaskQuery) (contract.TaskPage, error) {
		reads.Add(1)
		return contract.TaskPage{Results: []contract.TaskRecord{testTaskRecord("1")}, Total: 1}, nil
	}}
	s, current, d := taskTestState(t, lease)
	s.LoadTasks(d, nil, false)
	waitTasks(t, s, d)
	current.Generation++
	s.Consume(ui.Services{})
	if !s.TaskConnectionLost(d) || d.Error() == "" || s.TaskPage(d).Results[0].UID != "1" {
		t.Fatal("idle replacement did not mark original task lease lost while preserving cache")
	}
	s.LoadTasks(d, nil, true)
	if reads.Load() != 1 {
		t.Fatal("idle replacement replayed task request")
	}
	s.RecoverTasks(d)
	if reads.Load() != 1 || s.TaskConnectionLost(d) || d.tasks.fresh {
		t.Fatal("explicit recovery replayed cached task request")
	}
}

func TestTaskSmallViewportPreservesFiltersFooterAndStableSelection(t *testing.T) {
	lease := &taskTestLease{list: func(context.Context, contract.TaskQuery) (contract.TaskPage, error) {
		return contract.TaskPage{Results: []contract.TaskRecord{testTaskRecord("1")}, Total: 1}, nil
	}}
	s, _, d := taskTestState(t, lease)
	s.LoadTasks(d, nil, false)
	waitTasks(t, s, d)
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 720, 520)
	originalKey := d.columns.Key(0)
	d.tasks.page.Results[0].Status = "succeeded"
	d.tasks.page.Results[0].Token = "new-native-body-proof"
	if d.columns.Key(0) != originalKey {
		t.Fatal("native observation changed task row identity")
	}
	if err := tt.Click("高级任务筛选"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"Batch UID 筛选", "应用任务筛选", "下一页任务"} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.H <= 0 || r.Y < 0 || r.Y+r.H > 520 {
			t.Fatal("advanced task filters displaced usable controls", label, r)
		}
	}
}
