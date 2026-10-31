package sqlworkspace

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/engines"
	"path/filepath"
	"testing"
	"time"
)

func TestEngineRuntimeReleaseCancelsOwningPhysicalLeaseWithoutLeakingTempState(t *testing.T) {
	m := engines.NewManager()
	defer m.Close()
	if e := m.Connect(context.Background(), connections.Profile{ID: 1, Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "lease.sqlite")}, ""); e != nil {
		t.Fatal(e)
	}
	runtime := EngineRuntime(func(id int64) (contract.Session, error) { return m.Session(id) })
	l, e := runtime.Reserve(1, 2)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = l.Execute(context.Background(), "main", "CREATE TEMP TABLE private_state(v)"); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := l.Execute(context.Background(), "main", `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<1000000000) SELECT sum(i) FROM n`)
		done <- e
	}()
	time.Sleep(20 * time.Millisecond)
	l.Release()
	select {
	case e = <-done:
		if e == nil {
			t.Fatal("released query completed without cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("physical lease release did not receive executor acknowledgement")
	}
	next, e := runtime.Reserve(1, 3)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Release()
	r, e := next.Execute(context.Background(), "main", `SELECT name FROM sqlite_temp_schema WHERE name='private_state'`)
	if e != nil || len(r.Sets) != 1 || len(r.Sets[0].Rows) != 0 {
		t.Fatalf("physical session state leaked into new SQL document: %+v %v", r, e)
	}
}
