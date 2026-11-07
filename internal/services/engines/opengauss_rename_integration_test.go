package engines

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussRenameNativeCatalogStorageNamesAndReferencesFixture(t *testing.T) {
	for _, mode := range []string{"row", "unlogged", "column", "partition", "array_collision", "unicode", "programs", "references"} {
		t.Run(mode, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			name, schema := qualified(target.Object), quote(target.Object.Schema)
			if mode == "unlogged" || mode == "column" || mode == "partition" {
				prefix, suffix := "CREATE TABLE ", ""
				if mode == "unlogged" {
					prefix = "CREATE UNLOGGED TABLE "
				}
				if mode == "column" {
					suffix = " WITH(orientation=column,compression=middle)"
				}
				if mode == "partition" {
					suffix = " PARTITION BY RANGE(id)(PARTITION p1 VALUES LESS THAN(10),PARTITION p2 VALUES LESS THAN(MAXVALUE))"
				}
				if _, err := db.ExecContext(ctx, "DROP TABLE "+name+"; "+prefix+name+"(id bigint,note text,amount numeric(40,20))"+suffix+"; INSERT INTO "+name+" VALUES(1,'原始',9007199254740993.12345678901234567890)"); err != nil {
					t.Fatal(err)
				}
			}
			destination := `renamed"; other --`
			if mode == "unicode" {
				destination = strings.Repeat("表", 21)
			}
			if mode == "array_collision" {
				destination = "renamed"
				if _, err := db.ExecContext(ctx, "CREATE TYPE "+schema+"._renamed AS ENUM('a'); CREATE TYPE "+schema+"._renamed_1 AS ENUM('b')"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "programs" {
				if _, err := db.ExecContext(ctx, "CREATE SEQUENCE "+schema+".effects START 919; CREATE FUNCTION "+schema+".effect() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('"+schema+".effects'::regclass); RETURN NEW; END $$; CREATE TRIGGER keep_trigger BEFORE UPDATE ON "+name+" FOR EACH ROW EXECUTE PROCEDURE "+schema+".effect(); CREATE RULE keep_rule AS ON UPDATE TO "+name+" DO ALSO NOTHING; COMMENT ON TRIGGER keep_trigger ON "+name+" IS '中文'; COMMENT ON TABLE "+name+" IS '完整注释'"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, "ALTER TABLE "+name+" ALTER COLUMN id SET DEFAULT nextval('"+schema+".effects'::regclass)"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "references" {
				if _, err := db.ExecContext(ctx, "CREATE TABLE "+schema+".child(parent_id bigint REFERENCES "+name+"(id)); INSERT INTO "+schema+".child VALUES(1); ALTER TABLE "+name+" ADD COLUMN self_id bigint REFERENCES "+name+"(id); CREATE VIEW "+schema+".keep_view AS SELECT id,note FROM "+name); err != nil {
					t.Fatal(err)
				}
			}
			ops := s.SchemaDesign()
			if ops.CanRename == nil || !ops.CanRename(target) || ops.Rename == nil || ops.Apply != nil {
				t.Fatal("independent native rename binding")
			}
			original, err := ops.Snapshot(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			r := contract.SchemaRenameRequest{Original: original, Destination: destination}
			signature := schemaddl.Signature(r)
			out := ops.Rename(ctx, r)
			expected := target
			expected.Object.Name = destination
			if out.State != "committed" || out.Error != nil || !out.Verified || out.Actual == nil || out.Actual.Target != expected || out.Actual.Schema.OpenGauss.RelationOID != original.Schema.OpenGauss.RelationOID || schemaddl.Signature(r) != signature {
				t.Fatal("native verified rename", out)
			}
			var note, amount string
			if err := db.QueryRowContext(ctx, "SELECT note,amount::text FROM "+qualified(expected.Object)).Scan(&note, &amount); err != nil || note != "原始" || amount != "9007199254740993.12345678901234567890" {
				t.Fatal("original complete values", note, amount, err)
			}
			if replay := ops.Rename(ctx, r); replay.Error == nil || replay.Verified {
				t.Fatal("old name replay", replay)
			}
			if mode == "array_collision" {
				var array string
				if err := db.QueryRowContext(ctx, "SELECT a.typname FROM pg_type t JOIN pg_type a ON a.oid=t.typarray WHERE t.typrelid=$1", original.Schema.OpenGauss.RelationOID).Scan(&array); err != nil || array != "_renamed_2" {
					t.Fatal("native array collision", array, err)
				}
			}
			if mode == "programs" {
				var last int
				var called bool
				if err := db.QueryRowContext(ctx, "SELECT last_value,is_called FROM "+schema+".effects").Scan(&last, &called); err != nil || last != 919 || called {
					t.Fatal("rename invoked row program", last, called, err)
				}
			}
			if mode == "references" {
				var parent int
				if err := db.QueryRowContext(ctx, "SELECT id FROM "+schema+".keep_view").Scan(&parent); err != nil || parent != 1 {
					t.Fatal("dependent view", parent, err)
				}
				if _, err := db.ExecContext(ctx, "INSERT INTO "+schema+".child VALUES(999)"); err == nil {
					t.Fatal("incoming foreign key disappeared")
				}
			}
			if mode == "row" {
				for _, entry := range out.Actual.Schema.OpenGauss.Entries {
					if entry.Kind == "copy_definition" || entry.Kind == "design_definition" {
						continue
					}
					corrupt := out.Actual.Clone()
					for i, e := range corrupt.Schema.OpenGauss.Entries {
						if e.Kind == entry.Kind && e.OID == entry.OID && e.SubID == entry.SubID {
							var row map[string]json.RawMessage
							if json.Unmarshal(e.Data, &row) != nil {
								t.Fatal("native record")
							}
							row["unknown_rename_property"] = json.RawMessage(`90071992547409931234567890`)
							corrupt.Schema.OpenGauss.Entries[i].Data, _ = json.Marshal(row)
						}
					}
					corrupt.Schema.Revision, _ = corrupt.Schema.OpenGauss.Fingerprint()
					arrayName, _ := schemaddl.OpenGaussRenameArrayName(destination, nil)
					if err := schemaddl.VerifyOpenGaussRename(original.Schema, destination, arrayName, corrupt.Schema); err == nil {
						t.Fatal("unknown native property accepted", entry.Kind, entry.OID)
					}
				}
			}
		})
	}
}

func TestOpenGaussRenameChangedOriginalAndOccupiedDestinationFixture(t *testing.T) {
	for _, mode := range []string{"schema_changed", "table_collision", "type_collision", "wrong_generation", "wrong_scope"} {
		t.Run(mode, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			original := openGaussTableOriginal(t, s, target)
			destination := "renamed"
			switch mode {
			case "schema_changed":
				if _, err := db.ExecContext(ctx, "COMMENT ON TABLE "+qualified(target.Object)+" IS 'changed after preview'"); err != nil {
					t.Fatal(err)
				}
			case "table_collision":
				destination = "other"
			case "type_collision":
				if _, err := db.ExecContext(ctx, "CREATE TYPE "+quote(target.Object.Schema)+".renamed AS ENUM('occupied')"); err != nil {
					t.Fatal(err)
				}
			case "wrong_generation":
				original.Target.Generation++
			case "wrong_scope":
				original.Target.Object.Schema = "public"
			}
			out := s.SchemaDesign().Rename(ctx, contract.SchemaRenameRequest{Original: original, Destination: destination})
			if out.Error == nil || out.Verified || out.Actual != nil || out.State == "committed" || out.State == "uncertain" {
				t.Fatal("conflicting rename executed", out)
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+qualified(target.Object)).Scan(&count); err != nil || count != 1 {
				t.Fatal("original changed on rejected rename", count, err)
			}
		})
	}
}

func TestOpenGaussRenameCancellationAndLostAcknowledgementFixture(t *testing.T) {
	for _, sample := range []struct{ name, query string }{
		{"cancel_rename", "ALTER TABLE %s RENAME TO \"renamed\""},
		{"lost_commit", "COMMIT"},
		{"lost_rollback", "ROLLBACK"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			original, err := s.SchemaDesign().Snapshot(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			query := sample.query
			if sample.name == "cancel_rename" {
				query = "ALTER TABLE " + qualified(target.Object) + " RENAME TO \"renamed\""
			}
			fired := false
			gaussDesignInstallExecutionGate(t, s, query, func(context.Context) error {
				fired = true
				if sample.name == "cancel_rename" {
					cancel()
					return nil
				}
				return io.ErrUnexpectedEOF
			})
			destination := "renamed"
			if sample.name == "lost_rollback" {
				destination = "other"
			}
			out := s.SchemaDesign().Rename(ctx, contract.SchemaRenameRequest{Original: original, Destination: destination})
			if !fired || out.Error == nil || out.Verified || out.Actual != nil {
				t.Fatal("unacknowledged rename", fired, out)
			}
			if sample.name == "cancel_rename" && !errors.Is(out.Error, context.Canceled) {
				t.Fatal(out)
			}
			if strings.HasPrefix(sample.name, "lost_") && (out.State != "uncertain" || !errors.Is(out.Error, ErrUncertain) || !s.isClosed()) {
				t.Fatal("owning session remained replayable", out)
			}
			var actual string
			if err := db.QueryRowContext(context.Background(), "SELECT relname FROM pg_class WHERE oid=$1", original.Schema.OpenGauss.RelationOID).Scan(&actual); err != nil || actual != map[bool]string{true: "renamed", false: "original"}[sample.name == "lost_commit"] {
				t.Fatal("original native receipt", actual, err)
			}
		})
	}
}
