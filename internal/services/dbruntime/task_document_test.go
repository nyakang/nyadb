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

type tasksHandle struct {
	handle
	lease *tasksLease
}
type tasksLease struct{ reads, closed int }

func (h *tasksHandle) Namespaces(context.Context) ([]contract.Namespace, error) { return nil, nil }
func (h *tasksHandle) Browse(context.Context, contract.NamespaceRef) ([]contract.DBObject, error) {
	return nil, nil
}
func (h *tasksHandle) TableMetadata(context.Context, contract.ObjectRef) (*contract.TableStructure, error) {
	return nil, nil
}
func (h *tasksHandle) SupportsTaskDocument(o contract.ObjectRef) bool {
	return o.ProfileID == h.id && o.Kind == "tasks"
}
func (h *tasksHandle) ReserveTaskDocument(contract.ObjectRef) (contract.TaskDocumentLease, error) {
	return h.lease, nil
}
func (l *tasksLease) List(context.Context, contract.TaskQuery) (contract.TaskPage, error) {
	l.reads++
	return contract.TaskPage{}, nil
}
func (l *tasksLease) Read(context.Context, contract.TaskRecord) (contract.TaskRecord, error) {
	l.reads++
	return contract.TaskRecord{}, nil
}
func (l *tasksLease) Observe(context.Context, contract.TaskRecord, time.Duration) (contract.TaskRecord, error) {
	l.reads++
	return contract.TaskRecord{}, nil
}
func (l *tasksLease) Close() error { l.closed++; return nil }
func TestTaskDocumentPublishedFeatureStaysOriginalAndClosesOriginalLease(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var handles []*tasksHandle
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &tasksHandle{handle: handle{id: p.ID, caps: contract.Capabilities{Metadata: true}}, lease: &tasksLease{}}
		handles = append(handles, h)
		return h, nil
	}))
	first, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := m.TaskDocuments(1)
	if err != nil {
		t.Fatal(err)
	}
	o := contract.ObjectRef{ProfileID: 1, Kind: "tasks", Name: "tasks"}
	foreign := o
	foreign.ProfileID = 2
	if provider.SupportsTaskDocument(foreign) {
		t.Fatal("foreign profile scope published")
	}
	lease, err := provider.ReserveTaskDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lease.List(context.Background(), contract.TaskQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.ConnectSession(context.Background(), profile(1), "")
	if err != nil || second.Generation() <= first.Generation() {
		t.Fatal(err)
	}
	if provider.SupportsTaskDocument(o) {
		t.Fatal("old feature followed new generation")
	}
	if _, err := provider.ReserveTaskDocument(o); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old reservation followed replacement")
	}
	if _, err := lease.List(context.Background(), contract.TaskQuery{Limit: 1}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old task list followed replacement")
	}
	if _, err := lease.Read(context.Background(), contract.TaskRecord{}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old task detail followed replacement")
	}
	if _, err := lease.Observe(context.Background(), contract.TaskRecord{}, time.Second); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old observer followed replacement")
	}
	_ = lease.Close()
	if handles[0].lease.closed != 1 || handles[0].lease.reads != 1 || handles[1].lease.closed != 0 || !m.IsConnected(1) {
		t.Fatal("old task close damaged replacement")
	}
}
