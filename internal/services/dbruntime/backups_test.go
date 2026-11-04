package dbruntime_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func TestSQLiteRestoreDrainsSharedFileAndPreservesOperation(t *testing.T) {
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	ctx := context.Background()
	p := profile(1)
	p.Database = filepath.Join(t.TempDir(), "shared.sqlite")
	s, e := m.ConnectSession(ctx, p, "")
	if e != nil {
		t.Fatal(e)
	}
	q, _ := contract.Require[contract.QueryProvider](s)
	l, _ := q.ReserveQuery(1)
	defer l.Release()
	if _, e = l.Execute(ctx, "main", "CREATE TABLE restored(id INTEGER); INSERT INTO restored VALUES(42)"); e != nil {
		t.Fatal(e)
	}
	r := contract.BackupRequest{Target: contract.SchemaDesignTarget{Object: contract.ObjectRef{ProfileID: 1, Catalog: "main", Schema: "main", Kind: "namespace"}, Generation: s.Generation()}, Action: "backup", Scope: "database", Path: filepath.Join(t.TempDir(), "backup.sqlite")}
	ops := m.Backups(1)
	plan, e := ops.Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if out := ops.Execute(ctx, plan, nil); out.Error != nil {
		t.Fatal(out)
	}
	p2 := p
	p2.ID = 2
	if _, e = m.ConnectSession(ctx, p2, ""); e != nil {
		t.Fatal(e)
	}
	r.Action, r.Destination, r.Overwrite = "restore", p.Database, true
	plan, e = ops.Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.Affected) != 2 {
		t.Fatal(plan.Affected)
	}
	if _, e = l.Execute(ctx, "main", "DELETE FROM restored"); e != nil {
		t.Fatal(e)
	}
	blocked := false
	out := ops.Execute(ctx, plan, func(contract.BackupProgress) { _, e := m.ConnectSession(ctx, p2, ""); blocked = e != nil })
	if out.Error != nil || out.State != "completed" || !blocked || len(out.Drained) != 2 || m.IsConnected(1) || m.IsConnected(2) {
		t.Fatal(out, blocked)
	}
	next, e := m.ConnectSession(ctx, p, "")
	if e != nil {
		t.Fatal(e)
	}
	q, _ = contract.Require[contract.QueryProvider](next)
	lease, _ := q.ReserveQuery(2)
	defer lease.Release()
	rows, e := lease.Execute(ctx, "main", "SELECT id FROM restored")
	if e != nil || rows.Rows[0][0] != "42" {
		t.Fatal(rows, e)
	}
}
