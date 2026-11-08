package dbruntime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type managedTasksHandle struct {
	tasksHandle
	managementLease *managedTasksLease
	recoveries      int
}
type managedTasksLease struct {
	tasksLease
	writes int
}

func (h *managedTasksHandle) ReserveTaskDocument(contract.ObjectRef) (contract.TaskDocumentLease, error) {
	return h.managementLease, nil
}
func (h *managedTasksHandle) RecoverTaskDocument(_ contract.ObjectRef, old contract.TaskDocumentLease) (contract.TaskRecovery, error) {
	h.recoveries++
	_ = old.Close()
	return contract.TaskRecovery{Lease: h.managementLease, Receipt: contract.TaskMutationReceipt{UID: "8", Raw: "original admission", Targets: []string{"1"}}}, nil
}
func (l *managedTasksLease) SupportsTaskManagement() bool { return true }
func (l *managedTasksLease) PrepareTaskMutation(a string, r []contract.TaskRecord) (contract.TaskMutationPlan, error) {
	return contract.TaskMutationPlan{Action: a, Records: r}, nil
}
func (l *managedTasksLease) MutateTasks(context.Context, contract.TaskMutationPlan) (contract.TaskMutationResult, error) {
	l.writes++
	return contract.TaskMutationResult{State: "enqueued"}, nil
}
func (l *managedTasksLease) ObserveTaskMutation(context.Context, contract.TaskMutationReceipt, time.Duration) (contract.TaskMutationResult, error) {
	l.reads++
	return contract.TaskMutationResult{State: "succeeded"}, nil
}
func TestTaskManagementPublishedGenerationRecoveryAndOriginalLease(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var handles []*managedTasksHandle
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &managedTasksHandle{tasksHandle: tasksHandle{handle: handle{id: p.ID, caps: contract.Capabilities{Metadata: true}}}, managementLease: &managedTasksLease{}}
		handles = append(handles, h)
		return h, nil
	}))
	if _, err := m.ConnectSession(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	provider, err := m.TaskDocuments(1)
	if err != nil {
		t.Fatal(err)
	}
	object := contract.ObjectRef{ProfileID: 1, Kind: "tasks", Name: "tasks"}
	lease, err := provider.ReserveTaskDocument(object)
	if err != nil {
		t.Fatal(err)
	}
	managed := lease.(contract.TaskManagementLease)
	plan, err := managed.PrepareTaskMutation("cancel", []contract.TaskRecord{{UID: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = managed.MutateTasks(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err = m.ConnectSession(context.Background(), profile(1), ""); err != nil {
		t.Fatal(err)
	}
	if managed.SupportsTaskManagement() {
		t.Fatal("old capability followed new generation")
	}
	if _, err = managed.MutateTasks(context.Background(), plan); !errors.Is(err, contract.ErrRevoked) || handles[0].managementLease.writes != 1 || handles[1].managementLease.writes != 0 {
		t.Fatal("original operation rebound", err)
	}
	if _, err = managed.ObserveTaskMutation(context.Background(), contract.TaskMutationReceipt{}, time.Second); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old observation followed replacement", err)
	}
	next, err := m.TaskDocuments(1)
	if err != nil {
		t.Fatal(err)
	}
	foreign := object
	foreign.Catalog = "another"
	if _, err = next.(contract.TaskRecoveryProvider).RecoverTaskDocument(foreign, lease); !errors.Is(err, contract.ErrTaskMutationUsed) || handles[1].recoveries != 0 {
		t.Fatal("foreign recovery dispatched", err)
	}
	recovered, err := next.(contract.TaskRecoveryProvider).RecoverTaskDocument(object, lease)
	if err != nil || recovered.Receipt.Raw != "original admission" || handles[1].managementLease.reads != 0 || handles[1].managementLease.writes != 0 {
		t.Fatal("recovery replayed or lost evidence", err)
	}
	if _, err = recovered.Lease.(contract.TaskManagementLease).ObserveTaskMutation(context.Background(), recovered.Receipt, time.Second); err != nil || handles[1].managementLease.reads != 1 {
		t.Fatal("explicit recovered observation failed", err)
	}
	_ = lease.Close()
	if !m.IsConnected(1) || handles[1].managementLease.closed != 0 {
		t.Fatal("old close damaged recovered session")
	}
}
