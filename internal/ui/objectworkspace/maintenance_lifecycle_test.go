package objectworkspace

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestMaintenanceParkedReceiptRetainsOriginalTarget(t *testing.T) {
	current := int64(1)
	w := Session{ProfileID: 1, Generation: 7, Connected: true}
	target := contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Catalog: "original", Schema: "same_name", Name: "records", Kind: "table"}, Generation: 11}
	started, release := make(chan struct{}), make(chan struct{})
	var events []MaintenanceEvent
	ops := contract.MaintenanceOperations{
		Actions: func(contract.SchemaDesignTarget) []contract.MaintenanceAction {
			return []contract.MaintenanceAction{{ID: "ANALYZE"}}
		},
		Plan: func(_ context.Context, r contract.MaintenanceRequest) (contract.MaintenancePlan, error) {
			return contract.MaintenancePlan{Request: r, SQL: "ANALYZE original.same_name.records"}, nil
		},
		Execute: func(_ context.Context, _ contract.MaintenancePlan) contract.MaintenanceOutcome {
			close(started)
			<-release
			return contract.MaintenanceOutcome{State: "completed"}
		},
	}
	s := New(Dependencies{Session: func() Session { return Session{ProfileID: current, Generation: w.Generation, Connected: true} }, SessionFor: func(int64) Session { return w }, SchemaDesignFor: func(int64) contract.SchemaDesignOperations { return contract.SchemaDesignOperations{} }, SchemaTarget: func(int64, string, string) contract.SchemaDesignTarget { return target }, MaintenanceFor: func(int64) contract.MaintenanceOperations { return ops }, Maintained: func(_ ui.Services, e MaintenanceEvent) { events = append(events, e) }})
	defer s.CloseAll()
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.consumeMaintenance(services) }, 840, 560)
	s.beginNeutralMaintenance(services, 1, "same_name", "records", "ANALYZE")
	deadline := time.Now().Add(3 * time.Second)
	for s.maintenancePanel.busy && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if s.maintenancePanel.plan == nil {
		t.Fatal("maintenance preview missing")
	}
	s.executeMaintenance(services)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance worker did not start")
	}
	current = 2
	close(release)
	for len(s.maintenancePanel.results) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	tt.Frame()
	if !s.maintenancePanel.busy || len(events) != 0 {
		t.Fatal("parked receipt changed active profile")
	}
	current, w.Generation = 1, 8
	original := target
	target.Generation++
	tt.Frame()
	if len(events) != 1 || events[0].Original != original || events[0].Generation != 7 || s.maintenancePanel.busy {
		t.Fatal("late receipt lost captured scope", events)
	}
}

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
