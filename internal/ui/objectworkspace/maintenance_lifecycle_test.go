package objectworkspace

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestCanceledRestoreLeaveGuardReopensAndRevokesContinuation(t *testing.T) {
	w := Session{ProfileID: 1, Generation: 7, Connected: true}
	pending := false
	var continuation func(ui.Services)
	var calls atomic.Int32
	s := New(Dependencies{Session: func() Session { return w }, BeforeRestore: func(_ ui.Services, _ contract.BackupPlan, next func(ui.Services)) bool {
		pending, continuation = true, next
		return true
	}, RestoreGuardPending: func(contract.BackupPlan) bool { return pending }})
	defer s.CloseAll()
	r := contract.BackupRequest{Target: contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1}, Generation: 11}, Action: "restore", Destination: "target", Overwrite: true}
	x := &portableBackupPanel{request: r, plan: &contract.BackupPlan{Request: r, QuiesceFile: "target", Affected: []contract.BackupSession{{ProfileID: 1, Generation: 11}}}, confirmation: "target", acknowledgeSessions: true, open: true,
		ops: contract.BackupOperations{Execute: func(context.Context, contract.BackupPlan, func(contract.BackupProgress)) contract.BackupOutcome {
			calls.Add(1)
			return contract.BackupOutcome{State: "completed"}
		}}}
	s.portableBackup = x
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.consumePortableBackup(services) }, 840, 560)
	s.executePortableBackup(services)
	if !x.guarding || continuation == nil {
		t.Fatal("restore did not enter leave guard")
	}
	pending = false // The user cancels the existing document's leave dialog.
	tt.Frame()
	if x.guarding || !x.ready() {
		t.Fatal("canceled guard left the restore modal hidden")
	}
	continuation(services)
	if calls.Load() != 0 || x.busy || x.submitted {
		t.Fatal("revoked guard continuation executed a restore")
	}
}
