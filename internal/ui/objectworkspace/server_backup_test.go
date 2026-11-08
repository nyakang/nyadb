package objectworkspace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type backupTestLease struct{ *taskManagementTestLease }

func (l *backupTestLease) SupportsServerBackup(a string) bool { return serverBackupAction(a) }
func (l *backupTestLease) PrepareServerBackup(a string) (contract.TaskMutationPlan, error) {
	l.prepares.Add(1)
	return contract.TaskMutationPlan{Action: a, Request: "POST /dumps", Token: "backup-plan"}, nil
}
func TestServerBackupWithoutSelectionParkedReceiptAndOriginalArtifact(t *testing.T) {
	s, current, d, base := taskManagementTestState(t)
	d.object.Catalog = ""
	d.tasks.page = nil
	d.tasks.fresh = false
	l := &backupTestLease{base}
	d.tasks.lease = l
	if !s.CanCreateServerBackup(d, "dump") {
		t.Fatal("backup depends on loaded selection")
	}
	if err := s.PrepareServerBackup(d, "dump"); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 720, 520)
	if !tt.HasText("创建服务器 dump 预览") || !tt.HasText("范围：原连接服务器的全部索引、设置与密钥") || tt.HasText("仅操作以下 0 个原任务 UID") {
		t.Fatal("global backup preview missing")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	l.mutate = func(_ context.Context, p contract.TaskMutationPlan) (contract.TaskMutationResult, error) {
		close(entered)
		<-release
		if len(p.Records) != 0 {
			t.Error("backup inherited task selection")
		}
		return contract.TaskMutationResult{State: "enqueued", Request: p.Request, Receipt: contract.TaskMutationReceipt{Action: "dump", UID: "8", Raw: "original dump admission", Token: "original"}}, nil
	}
	l.observeMutation = func(_ context.Context, r contract.TaskMutationReceipt, _ time.Duration) (contract.TaskMutationResult, error) {
		return contract.TaskMutationResult{State: "succeeded", Receipt: r, TaskRaw: "original artifact evidence", Artifact: contract.TaskServerArtifact{Kind: "dump", UID: "20261108-123456789"}}, nil
	}
	s.SubmitTaskManagement(d, nil)
	<-entered
	current.ProfileID = 9
	close(release)
	time.Sleep(10 * time.Millisecond)
	s.Consume(ui.Services{})
	if !d.Loading() || s.TaskManagementResult(d).Receipt.Raw != "" {
		t.Fatal("parked backup consumed")
	}
	current.ProfileID = 7
	waitTasks(t, s, d)
	if s.CanCreateServerBackup(d, "dump") || s.TaskManagementResult(d).Artifact.Kind != "" || !s.GuardDesignLeave(d, nil) {
		t.Fatal("admission confused with completion")
	}
	d.tasks.management.guard = false
	s.ObserveTaskManagement(d, nil)
	waitTasks(t, s, d)
	result := s.TaskManagementResult(d)
	if result.Artifact.UID != "20261108-123456789" || result.CountsKnown || result.Request != "POST /dumps" {
		t.Fatal("artifact evidence lost", result)
	}
	d.tasks.tab = 3
	tt.Frame()
	if !tt.HasText("dumpUid: 20261108-123456789") {
		t.Fatal("artifact identity not shown")
	}
	d.object.Catalog = "owned"
	if s.CanCreateServerBackup(d, "dump") || !errors.Is(s.PrepareServerBackup(d, "dump"), contract.ErrUnsupported) {
		t.Fatal("index backup enabled")
	}
}
