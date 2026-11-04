package engines

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSQLiteAttachedBackupAndLockedRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	aux := filepath.Join(root, "aux.sqlite")
	db, e := sqliteBackupOpen(aux, "rwc")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("CREATE TABLE attachment(id INTEGER); INSERT INTO attachment VALUES(77)"); e != nil {
		t.Fatal(e)
	}
	db.Close()
	opts := connections.DefaultSQLiteOptions()
	opts.RequireExisting = false
	opts.Attachments = []connections.SQLiteAttachment{{Name: "aux", File: aux}}
	manager := NewManager()
	defer manager.Close()
	p := connections.Profile{ID: 50, Kind: connections.SQLite, Database: filepath.Join(root, "main.sqlite"), SQLiteOptions: opts}
	if e = manager.Connect(ctx, p, ""); e != nil {
		t.Fatal(e)
	}
	s, _ := manager.Session(p.ID)
	r := contract.BackupRequest{Target: contract.SchemaDesignTarget{Object: s.Namespace("aux").Object("namespace", ""), Generation: s.Generation()}, Action: "backup", Path: filepath.Join(root, "backup.sqlite"), Scope: "database"}
	plan, e := s.Backups().Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if plan.Source != aux {
		t.Fatal("attachment mapped to main", plan.Source)
	}
	if out := s.Backups().Execute(ctx, plan, nil); out.Error != nil {
		t.Fatal(out)
	}
	probe, e := sqliteBackupOpen(r.Path, "ro")
	if e != nil {
		t.Fatal(e)
	}
	var id int
	if e = probe.QueryRow("SELECT id FROM attachment").Scan(&id); e != nil || id != 77 {
		t.Fatal(id, e)
	}
	probe.Close()
	r.Action, r.Destination, r.Overwrite = "restore", aux, true
	plan, e = s.Backups().Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	task, e := s.PrepareBackup(ctx, plan)
	if e != nil {
		t.Fatal(e)
	}
	defer task.Close()
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	locked, e := sqliteBackupOpen(aux, "rw")
	if e != nil {
		t.Fatal(e)
	}
	defer locked.Close()
	c, e := locked.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if _, e = c.ExecContext(ctx, "BEGIN EXCLUSIVE; UPDATE attachment SET id=88"); e != nil {
		t.Fatal(e)
	}
	out := task.Execute(nil)
	if out.Error == nil || out.State == "completed" {
		t.Fatal("external lock was ignored", out)
	}
	if _, e = c.ExecContext(ctx, "COMMIT"); e != nil {
		t.Fatal(e)
	}
	if e = c.QueryRowContext(ctx, "SELECT id FROM attachment").Scan(&id); e != nil || id != 88 {
		t.Fatal("locked data modified", id, e)
	}
}
func TestSQLiteBackupCancelAndPlanMutation(t *testing.T) {
	s := adapterFixture(t)
	ctx := context.Background()
	if _, e := s.db.Exec("CREATE TABLE large_backup(id INTEGER,data BLOB); INSERT INTO large_backup SELECT 1,zeroblob(4194304)"); e != nil {
		t.Fatal(e)
	}
	r := contract.BackupRequest{Target: contract.SchemaDesignTarget{Object: s.Namespace("main").Object("namespace", ""), Generation: s.Generation()}, Action: "backup", Path: filepath.Join(t.TempDir(), "canceled.sqlite"), Scope: "database"}
	plan, e := s.Backups().Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	wrong := plan.Clone()
	wrong.Request.Path = filepath.Join(t.TempDir(), "wrong.sqlite")
	if out := s.Backups().Execute(ctx, wrong, nil); out.Error == nil || out.State != "not_executed" {
		t.Fatal("mutated plan executed", out)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := s.Backups().Execute(ctx, plan, func(contract.BackupProgress) { cancel() })
	if out.Error == nil || out.State == "completed" {
		t.Fatal("cancel published a backup", out)
	}
	if _, e = os.Stat(r.Path); !os.IsNotExist(e) {
		t.Fatal("partial output published", e)
	}
}
