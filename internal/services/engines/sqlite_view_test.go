package engines

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func viewSnapshot(t *testing.T, s *Session, schema, name string) contract.ViewDesignSnapshot {
	t.Helper()
	out, err := s.ViewDesign().Snapshot(context.Background(), contract.SchemaDesignTarget{Object: s.Namespace(schema).Object("view", name), Generation: s.Generation()})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func requireViewCommit(t *testing.T, out contract.ViewDesignOutcome) {
	t.Helper()
	if out.State != "committed" || !out.Verified || out.Actual == nil || out.Error != nil {
		t.Fatalf("view outcome: %+v", out)
	}
}
func TestSQLiteViewCreatesEditsRenamesAndDropsCompleteDefinition(t *testing.T) {
	s := sqliteDesignFixture(t, `CREATE TABLE source(id INTEGER PRIMARY KEY,value TEXT,raw BLOB);INSERT INTO source VALUES(1,'saved',x'00ff'),(2,NULL,NULL);CREATE VIEW unrelated AS SELECT * FROM source`)
	original := viewSnapshot(t, s, "main", "v")
	desired, err := schemaddl.ParseSQLiteView(`CREATE VIEW main.v(id,value,raw) AS SELECT id,value,raw FROM source`)
	if err != nil {
		t.Fatal(err)
	}
	out := s.ViewDesign().Apply(context.Background(), contract.ViewDesignRequest{Original: original, Action: "create", Desired: desired})
	requireViewCommit(t, out)
	if _, err = s.db.Exec(`CREATE TRIGGER v_update INSTEAD OF UPDATE OF value ON v BEGIN UPDATE source SET value=new.value WHERE id=new.id; END`); err != nil {
		t.Fatal(err)
	}
	original = viewSnapshot(t, s, "main", "v")
	desired = original.Schema.View.Clone()
	desired.SelectSQL = `SELECT id,upper(value),raw FROM source`
	out = s.ViewDesign().Apply(context.Background(), contract.ViewDesignRequest{Original: original, Action: "alter", Desired: desired})
	requireViewCommit(t, out)
	var value string
	var raw []byte
	if err = s.db.QueryRow("SELECT value,raw FROM v WHERE id=1").Scan(&value, &raw); err != nil || value != "SAVED" || len(raw) != 2 || raw[1] != 255 {
		t.Fatal(value, raw, err)
	}
	if _, err = s.db.Exec("UPDATE v SET value='through preserved trigger' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	original = viewSnapshot(t, s, "main", "v")
	desired = original.Schema.View.Clone()
	desired.Name = "renamed"
	out = s.ViewDesign().Apply(context.Background(), contract.ViewDesignRequest{Original: original, Action: "rename", Desired: desired})
	requireViewCommit(t, out)
	if out.Actual.Target.Object.Name != "renamed" || len(out.Actual.Schema.View.Triggers) != 1 {
		t.Fatal("rename receipt lost original triggers")
	}
	if _, err = s.db.Exec("UPDATE renamed SET value='after rename' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	original = viewSnapshot(t, s, "main", "renamed")
	out = s.ViewDesign().Apply(context.Background(), contract.ViewDesignRequest{Original: original, Action: "drop"})
	requireViewCommit(t, out)
	if out.Actual.Schema.Exists {
		t.Fatal("drop retained view")
	}
	if err = s.db.QueryRow("SELECT value FROM source WHERE id=1").Scan(&value); err != nil || value != "after rename" {
		t.Fatal("view drop changed source", value, err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name IN ('v','renamed','v_update')").Scan(&count); err != nil || count != 0 {
		t.Fatal("dropped view definitions remain", count, err)
	}
}

func TestSQLiteViewRejectsDependentRenameDropAndBrokenReplacementAtomically(t *testing.T) {
	for _, action := range []string{"rename", "drop", "alter removed column", "alter trigger column", "alter missing source", "multi statement"} {
		t.Run(action, func(t *testing.T) {
			s := sqliteDesignFixture(t, `CREATE TABLE source(id INTEGER PRIMARY KEY,value TEXT);INSERT INTO source VALUES(1,'saved');CREATE VIEW v AS SELECT * FROM source;CREATE VIEW dependent AS SELECT value FROM v;CREATE TRIGGER own INSTEAD OF UPDATE OF value ON v BEGIN UPDATE source SET value=new.value WHERE id=new.id;END`)
			original := viewSnapshot(t, s, "main", "v")
			r := contract.ViewDesignRequest{Original: original, Action: action, Desired: original.Schema.View.Clone()}
			switch action {
			case "rename":
				r.Desired.Name = "renamed"
			case "alter removed column":
				r.Action = "alter"
				r.Desired.SelectSQL = "SELECT id FROM source"
			case "alter trigger column":
				r.Action = "alter"
				r.Desired.SelectSQL = "SELECT id,1 AS value_replaced FROM source"
			case "alter missing source":
				r.Action = "alter"
				r.Desired.SelectSQL = "SELECT * FROM missing"
			case "multi statement":
				r.Action = "alter"
				r.Desired.SelectSQL = "SELECT 1;DELETE FROM source"
			}
			out := s.ViewDesign().Apply(context.Background(), r)
			if out.Error == nil || out.Verified || out.State == "committed" {
				t.Fatalf("unsafe view operation: %+v", out)
			}
			actual := viewSnapshot(t, s, "main", "v")
			if schemaddl.Signature(actual.Schema) != schemaddl.Signature(original.Schema) {
				t.Fatal("failed view operation changed original catalog")
			}
			var value string
			if err := s.db.QueryRow("SELECT value FROM dependent").Scan(&value); err != nil || value != "saved" {
				t.Fatal("failure changed source/dependent", value, err)
			}
		})
	}
}

func TestSQLiteViewRejectsConflictsForgedSnapshotAndOccupiedTargets(t *testing.T) {
	for _, change := range []string{"ddl", "forged", "occupied", "wrong schema", "wrong profile", "generation", "hidden trigger"} {
		t.Run(change, func(t *testing.T) {
			s := sqliteDesignFixture(t, "CREATE TABLE source(id);CREATE VIEW v AS SELECT id FROM source")
			r := contract.ViewDesignRequest{Original: viewSnapshot(t, s, "main", "v"), Action: "rename"}
			r.Desired = r.Original.Schema.View.Clone()
			r.Desired.Name = "renamed"
			switch change {
			case "ddl":
				_, _ = s.db.Exec("CREATE TABLE concurrent(id)")
			case "forged":
				r.Original.Schema.View.SelectSQL = "SELECT 2"
				r.Desired.SelectSQL = "SELECT 2"
			case "occupied":
				_, _ = s.db.Exec("CREATE TABLE renamed(id)")
				r.Original = viewSnapshot(t, s, "main", "v")
			case "wrong schema":
				r.Original.Target.Object.Schema = "temp"
			case "wrong profile":
				r.Original.Target.Object.ProfileID++
			case "generation":
				r.Original.Target.Generation++
			case "hidden trigger":
				r.Desired.Triggers = []schemaddl.Definition{{Kind: "trigger", Name: "hidden", SQL: "CREATE TRIGGER hidden INSTEAD OF DELETE ON v BEGIN DELETE FROM source;END"}}
			}
			out := s.ViewDesign().Apply(context.Background(), r)
			if out.Error == nil || out.Verified || out.State == "committed" {
				t.Fatalf("conflict accepted: %+v", out)
			}
			if change == "ddl" || change == "forged" {
				if !errors.Is(out.Error, schemaddl.ErrConflict) {
					t.Fatal(out.Error)
				}
			}
			var count int
			if err := s.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='view' AND name='v'").Scan(&count); err != nil || count != 1 {
				t.Fatal("original view lost", count, err)
			}
		})
	}
}

func TestSQLiteViewCancellationDuringRealLockWaitKeepsCatalog(t *testing.T) {
	s := sqliteDesignFixture(t, "CREATE TABLE source(id);CREATE VIEW v AS SELECT id FROM source")
	original := viewSnapshot(t, s, "main", "v")
	blocker, err := sql.Open("sqlite", sqliteFileURI(filepath.ToSlash(s.catalog)))
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if _, err = blocker.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer blocker.Exec("ROLLBACK")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	out := s.ViewDesign().Apply(ctx, contract.ViewDesignRequest{Original: original, Action: "drop"})
	if out.Error == nil || out.Verified || out.State == "committed" || ctx.Err() == nil {
		t.Fatal("lock wait not cancelled", out)
	}
	if _, err = blocker.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if actual := viewSnapshot(t, s, "main", "v"); schemaddl.Signature(actual.Schema) != schemaddl.Signature(original.Schema) {
		t.Fatal("cancel changed catalog")
	}
}

func TestSQLiteViewReadOnlyAndAttachmentIsolation(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "read-only"}[readOnly], func(t *testing.T) {
			dir := t.TempDir()
			main, attached := filepath.Join(dir, "main.sqlite"), filepath.Join(dir, "attached.sqlite")
			createSQLiteAttachmentFixture(t, main, "main")
			createSQLiteAttachmentFixture(t, attached, "attached")
			for _, path := range []string{main, attached} {
				db, err := sql.Open("sqlite", sqliteFileURI(filepath.ToSlash(path)))
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("CREATE VIEW v AS SELECT value FROM shared")
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			m := NewManager()
			defer m.Close()
			opts := connections.DefaultSQLiteOptions()
			opts.Attachments = []connections.SQLiteAttachment{{Name: "aux", File: attached, ReadOnly: readOnly}}
			if err := m.Connect(context.Background(), connections.Profile{ID: 83, Kind: connections.SQLite, Database: main, SQLiteOptions: opts}, ""); err != nil {
				t.Fatal(err)
			}
			s, _ := m.Session(83)
			original := viewSnapshot(t, s, "aux", "v")
			if s.ViewDesign().CanApply(original.Target) == readOnly {
				t.Fatal("wrong read-only availability")
			}
			out := s.ViewDesign().Apply(context.Background(), contract.ViewDesignRequest{Original: original, Action: "drop"})
			if readOnly {
				if out.Error == nil || out.State != "not_executed" {
					t.Fatal(out)
				}
			} else {
				requireViewCommit(t, out)
			}
			var value string
			if err := s.db.QueryRow("SELECT value FROM main.v").Scan(&value); err != nil || value != "main" {
				t.Fatal("main view changed", value, err)
			}
		})
	}
}
