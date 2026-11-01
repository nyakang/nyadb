package dbruntime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type adminHandle struct {
	handle
	admin adminFixture
}

func (h *adminHandle) Administration() mysqlsvc.Administration { return &h.admin }

type adminFixture struct {
	mysqlsvc.Administration
	calls atomic.Int32
}

func (a *adminFixture) SessionGeneration(id int64) uint64 { return 1 }
func (a *adminFixture) Accounts(ctx context.Context, id int64) ([]mysqlsvc.MySQLAccount, error) {
	if mysqlsvc.ExpectedMySQLSessionGeneration(ctx) != 1 {
		return nil, errors.New("untranslated generation")
	}
	a.calls.Add(1)
	return []mysqlsvc.MySQLAccount{{User: "fixture"}}, nil
}
func TestAdministrationTranslatesGenerationAndRejectsReplacement(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var handles []*adminHandle
	m.Register(connections.MySQL, dbruntime.ProviderFunc(func(ctx context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &adminHandle{handle: handle{id: p.ID, caps: contract.Capabilities{DesignTables: true, Metadata: true}}}
		handles = append(handles, h)
		return h, nil
	}))
	p := connections.Profile{ID: 7, Name: "admin fixture", Kind: connections.MySQL, Host: "localhost", Port: 3306, Username: "fixture"}
	first, e := m.ConnectSession(context.Background(), p, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Disconnect(p.ID); e != nil {
		t.Fatal(e)
	}
	second, e := m.ConnectSession(context.Background(), p, "")
	if e != nil {
		t.Fatal(e)
	}
	if second.Generation() <= first.Generation() {
		t.Fatal("generation did not advance")
	}
	admin := m.Administration(p.ID)
	ctx := mysqlsvc.WithMySQLSessionGeneration(context.Background(), second.Generation())
	if _, e = admin.Accounts(ctx, p.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Accounts(mysqlsvc.WithMySQLSessionGeneration(context.Background(), first.Generation()), p.ID); !errors.Is(e, contract.ErrRevoked) {
		t.Fatalf("stale context: %v", e)
	}
	if e = m.Disconnect(p.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = m.ConnectSession(context.Background(), p, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Accounts(ctx, p.ID); !errors.Is(e, contract.ErrRevoked) {
		t.Fatalf("old admin routed to replacement: %v", e)
	}
	if handles[1].admin.calls.Load() != 1 || handles[2].admin.calls.Load() != 0 {
		t.Fatal("unexpected backend dispatch")
	}
}

func TestConnectionTestKeepsPublishedSessionAndClosesLateHandle(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	live, probe := &handle{id: 1}, &handle{id: 1}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
		if calls.Add(1) == 1 {
			return live, nil
		}
		close(started)
		<-release
		return probe, nil
	}))
	session, e := m.ConnectSession(context.Background(), profile(1), "")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Test(ctx, profile(1), "") }()
	<-started
	cancel()
	close(release)
	if e = <-done; !errors.Is(e, context.Canceled) {
		t.Fatalf("test cancellation: %v", e)
	}
	current, e := m.Session(1)
	if e != nil || current != session || live.closed.Load() != 0 || probe.closed.Load() != 1 {
		t.Fatal("test changed published session or leaked probe")
	}
}
