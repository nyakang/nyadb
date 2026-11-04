package engines

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestSQLiteOnlineBackupNewRestoreAndDigest(t *testing.T) {
	staging := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, staging)
	}
	s := adapterFixture(t)
	ctx := context.Background()
	if _, e := s.db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE backup_source(id INTEGER PRIMARY KEY,value BLOB); INSERT INTO backup_source VALUES(1,X'00ff')"); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "copy.sqlite")
	r := contract.BackupRequest{Target: contract.SchemaDesignTarget{Object: s.Namespace("main").Object("namespace", ""), Generation: s.Generation()}, Action: "backup", Scope: "database", Path: path}
	p, e := s.Backups().Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	progress := 0
	out := s.Backups().Execute(ctx, p, func(contract.BackupProgress) { progress++ })
	if out.Error != nil || out.State != "completed" || progress == 0 {
		t.Fatal(out)
	}
	r.Action, r.Path, r.Destination = "restore", path, filepath.Join(t.TempDir(), "restored.sqlite")
	p, e = s.Backups().Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	out = s.Backups().Execute(ctx, p, nil)
	if out.Error != nil || out.State != "completed" {
		t.Fatal(out)
	}
	assertNoBackupStaging(t, staging, "nyadb-restore-source-*")
	assertNoBackupStaging(t, filepath.Dir(path), ".nyadb-backup-*")
	assertNoBackupStaging(t, filepath.Dir(r.Destination), ".nyadb-backup-*")
	db, e := sqliteBackupOpen(r.Destination, "ro")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var exact string
	if e = db.QueryRow("SELECT hex(value) FROM backup_source").Scan(&exact); e != nil || exact != "00FF" {
		t.Fatal(exact, e)
	}
	r.Destination = filepath.Join(t.TempDir(), "tampered.sqlite")
	p, e = s.Backups().Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, []byte("damaged"), 0600); e != nil {
		t.Fatal(e)
	}
	out = s.Backups().Execute(ctx, p, nil)
	if out.Error == nil || out.State != "not_executed" {
		t.Fatal("changed archive executed", out)
	}
	assertNoBackupStaging(t, staging, "nyadb-restore-source-*")
}

func assertNoBackupStaging(t *testing.T, dir, pattern string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil || len(paths) != 0 {
		t.Fatalf("backup staging resources remain: %v (%v)", paths, err)
	}
}

func TestPostgreSQL17BackupRestoreFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	ctx := context.Background()
	if dir := os.Getenv("NYADB_POSTGRES_TOOLS"); dir != "" {
		s.backupProfile.PostgreSQLOptions.ToolsDirectory = dir
	}
	if _, e := s.db.Exec(`CREATE SCHEMA nyadb_backup; CREATE TABLE nyadb_backup.items(id bigint PRIMARY KEY, value numeric); INSERT INTO nyadb_backup.items VALUES(1,9007199254740993.123456789)`); e != nil {
		t.Fatal(e)
	}
	defer s.db.Exec("DROP SCHEMA nyadb_backup CASCADE")
	path := filepath.Join(t.TempDir(), "database.dump")
	r := contract.BackupRequest{Target: contract.SchemaDesignTarget{Object: s.Namespace("nyadb_backup").Object("namespace", ""), Generation: s.Generation()}, Action: "backup", Scope: "schema", Path: path}
	p, e := s.Backups().Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	out := s.Backups().Execute(ctx, p, nil)
	if out.Error != nil || out.State != "completed" {
		t.Fatal(out)
	}
	destination := "nyadb_restored_fixture"
	defer s.db.Exec("DROP DATABASE IF EXISTS " + quote(destination) + " WITH (FORCE)")
	r.Action, r.Destination = "restore", destination
	p, e = s.Backups().Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	out = s.Backups().Execute(ctx, p, nil)
	if out.Error != nil || out.State != "completed" {
		t.Fatal(out)
	}
	profile := s.backupProfile
	profile.Database = destination
	db, e := (postgresDialect{}).open(profile, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var exact string
	if e = db.QueryRow("SELECT value::text FROM nyadb_backup.items").Scan(&exact); e != nil || exact != "9007199254740993.123456789" {
		t.Fatal(exact, e)
	}
	if _, e = db.Exec("CREATE TABLE nyadb_backup.outside(id int); INSERT INTO nyadb_backup.outside VALUES(42); DELETE FROM nyadb_backup.items"); e != nil {
		t.Fatal(e)
	}
	r.Overwrite = true
	p, e = s.Backups().Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	for _, line := range p.Objects {
		if schemaTOC.MatchString(line) {
			t.Fatal("existing schema remains in clean list", line)
		}
	}
	out = s.Backups().Execute(ctx, p, nil)
	if out.Error != nil || out.State != "completed" {
		t.Fatal(out)
	}
	var id int
	if e = db.QueryRow("SELECT id FROM nyadb_backup.outside").Scan(&id); e != nil || id != 42 {
		t.Fatal("archive-external object lost", id, e)
	}
	if _, e = db.Exec("CREATE VIEW nyadb_backup.external_view AS SELECT * FROM nyadb_backup.items"); e != nil {
		t.Fatal(e)
	}
	p, e = s.Backups().Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	out = s.Backups().Execute(ctx, p, nil)
	if out.Error == nil || out.State == "completed" {
		t.Fatal("external dependency was silently dropped", out)
	}
	if e = db.QueryRow("SELECT id FROM nyadb_backup.external_view").Scan(&id); e != nil || id != 1 {
		t.Fatal("failed restore changed original data", id, e)
	}
	if strings.Contains(out.Error.Error(), "9007199254740993") {
		t.Fatal("tool error leaked data")
	}
}

func TestPostgreSQL17BackupPrechecksFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	s.backupProfile.PostgreSQLOptions.ToolsDirectory = os.Getenv("NYADB_POSTGRES_TOOLS")
	ctx := context.Background()
	if _, e := s.db.Exec(`CREATE SCHEMA backup_deps; CREATE SCHEMA backup_external;
 CREATE TABLE backup_external.parent(id integer PRIMARY KEY);
 CREATE TYPE backup_external.status AS ENUM ('ready');
 CREATE FUNCTION backup_external.default_id() RETURNS integer LANGUAGE SQL AS 'SELECT 1';
 CREATE TABLE backup_deps.child(id integer PRIMARY KEY DEFAULT backup_external.default_id(), status backup_external.status, parent integer REFERENCES backup_external.parent(id))`); e != nil {
		t.Fatal(e)
	}
	defer s.db.Exec("DROP SCHEMA backup_deps CASCADE; DROP SCHEMA backup_external CASCADE")
	r := contract.BackupRequest{Target: contract.SchemaDesignTarget{Object: s.Namespace("backup_deps").Object("namespace", ""), Generation: s.Generation()}, Action: "backup", Scope: "tables", Tables: []contract.ObjectRef{s.Namespace("backup_deps").Object("table", "child")}, Path: filepath.Join(t.TempDir(), "partial.dump")}
	p, e := s.Backups().Plan(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	dependencies := strings.Join(p.Warnings, "\n")
	for _, name := range []string{"backup_external.parent", "backup_external.status", "backup_external.default_id"} {
		if !strings.Contains(dependencies, name) {
			t.Fatalf("missing external dependency %q: %s", name, dependencies)
		}
	}
	tools := s.backupProfile.PostgreSQLOptions.ToolsDirectory
	s.backupProfile.PostgreSQLOptions.ToolsDirectory = t.TempDir()
	if _, e = s.Backups().Plan(ctx, r); e == nil {
		t.Fatal("missing official tool was accepted")
	}
	s.backupProfile.PostgreSQLOptions.ToolsDirectory = tools
	if wrong := os.Getenv("NYADB_POSTGRES_WRONG_TOOLS"); wrong != "" {
		s.backupProfile.PostgreSQLOptions.ToolsDirectory = wrong
		if _, e = s.Backups().Plan(ctx, r); e == nil {
			t.Fatal("mismatched tool major was accepted")
		}
		s.backupProfile.PostgreSQLOptions.ToolsDirectory = tools
	}
	if e = os.WriteFile(r.Path, []byte("damaged PostgreSQL archive"), 0600); e != nil {
		t.Fatal(e)
	}
	r.Action, r.Destination = "restore", "nyadb_damaged_restore"
	if _, e = s.Backups().Plan(ctx, r); e == nil {
		t.Fatal("damaged archive was accepted")
	}
}
