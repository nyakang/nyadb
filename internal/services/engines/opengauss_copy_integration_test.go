package engines

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gausspq "gitcode.com/opengauss/openGauss-connector-go-pq"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func gaussCopy(t *testing.T, s *Session, target contract.SchemaDesignTarget, name string, data bool) contract.SchemaDesignOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.SchemaDesign().Copy(ctx, contract.SchemaCopyRequest{Original: openGaussTableOriginal(t, s, target), Destination: name, WithData: data, RebindSelfReferences: true})
}

func TestOpenGaussTableCopyNativeStructureAndCompleteValuesFixture(t *testing.T) {
	for _, storage := range []string{"row", "unlogged", "column", "partition"} {
		t.Run(storage, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			name := qualified(target.Object)
			ns := quote(target.Object.Schema)
			q := "ALTER TABLE " + name + " ADD COLUMN payload bytea; UPDATE " + name + " SET payload=decode('00ff01','hex'); COMMENT ON TABLE " + name + " IS 'table 中文 quote '''; COMMENT ON COLUMN " + name + ".note IS 'column comment'; ALTER TABLE " + name + " ALTER COLUMN note SET STATISTICS 77; ALTER SEQUENCE " + ns + ".original_id_seq INCREMENT BY 3 CACHE 5; SELECT setval('" + target.Object.Schema + ".original_id_seq',987,true); COMMENT ON SEQUENCE " + ns + ".original_id_seq IS 'independent sequence'; CREATE INDEX note_idx ON " + name + "(note); COMMENT ON INDEX " + ns + ".note_idx IS 'index comment'"
			if storage == "unlogged" {
				q = "DROP TABLE " + name + "; CREATE UNLOGGED TABLE " + name + "(id bigint,note text); INSERT INTO " + name + " VALUES(1,'unlogged'),(1,'unlogged'),(2,NULL)"
			}
			if storage == "column" {
				q = "DROP TABLE " + name + "; CREATE TABLE " + name + "(id bigint,note text,n numeric(40,20)) WITH(orientation=column,compression=middle); INSERT INTO " + name + " VALUES(1,'column',9007199254740993.12345678901234567890),(2,NULL,NULL)"
			}
			if storage == "partition" {
				q = "DROP TABLE " + name + "; CREATE TABLE " + name + "(id bigint,note text) PARTITION BY RANGE(id)(PARTITION p1 VALUES LESS THAN(10),PARTITION p2 VALUES LESS THAN(MAXVALUE)); INSERT INTO " + name + " VALUES(1,'first'),(11,'second')"
			}
			if _, err := db.ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
			original := openGaussTableOriginal(t, s, target)
			for _, data := range []bool{true, false} {
				destination := fmt.Sprintf("copy_%t", data)
				out := gaussCopy(t, s, target, destination, data)
				if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil || out.Actual.Target.Object.Name != destination {
					t.Fatalf("native %s copy: state=%s err=%v", storage, out.State, out.Error)
				}
				var count int64
				if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+ns+"."+quote(destination)).Scan(&count); err != nil || data && count == 0 || !data && count != 0 {
					t.Fatal("copied rows", count, err)
				}
			}
			current := openGaussTableOriginal(t, s, target)
			if current.Schema.Revision != original.Schema.Revision {
				t.Fatal("copy changed source catalog")
			}
			if storage == "row" {
				var next, empty int64
				if err := db.QueryRowContext(ctx, "INSERT INTO "+ns+".copy_true(note,amount) VALUES('new',3) RETURNING id").Scan(&next); err != nil || next != 990 {
					t.Fatal("copied high water", next, err)
				}
				if err := db.QueryRowContext(ctx, "INSERT INTO "+ns+".copy_false(note,amount) VALUES('first',3) RETURNING id").Scan(&empty); err != nil || empty != 1 {
					t.Fatal("structure-only reset", empty, err)
				}
			}
		})
	}
}

func TestOpenGaussTableCopyForeignKeysTriggersRulesAndQuotedNamesFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := qualified(target.Object)
	ns := quote(target.Object.Schema)
	q := "ALTER TABLE " + name + " ADD COLUMN parent_id bigint REFERENCES " + name + "(id); UPDATE " + name + " SET parent_id=id; CREATE TABLE " + ns + ".audit(id bigint); CREATE FUNCTION " + ns + ".audit_insert() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN INSERT INTO " + ns + ".audit VALUES(NEW.id); RETURN NEW; END$$; CREATE TRIGGER audit_insert BEFORE INSERT ON " + name + " FOR EACH ROW EXECUTE PROCEDURE " + ns + ".audit_insert(); CREATE RULE retain_updates AS ON UPDATE TO " + name + " DO ALSO NOTHING; COMMENT ON TRIGGER audit_insert ON " + name + " IS 'program comment'"
	if _, err := db.ExecContext(ctx, q); err != nil {
		t.Fatal(err)
	}
	destination := `q"; DROP TABLE other; --`
	out := gaussCopy(t, s, target, destination, true)
	if out.Error != nil || !out.Verified {
		t.Fatal("quoted programs/self-reference copy", out.State, out.Error)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+ns+".audit").Scan(&count); err != nil || count != 0 {
		t.Fatal("copied trigger ran during insertion", count, err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO "+ns+"."+quote(destination)+"(note,amount) VALUES('next',3)"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+ns+".audit").Scan(&count); err != nil || count != 1 {
		t.Fatal("copied trigger missing", count, err)
	}
	if _, err := db.ExecContext(ctx, "SELECT * FROM "+ns+".other"); err != nil {
		t.Fatal("quoted destination executed SQL", err)
	}
	ref := contract.SchemaCopyRequest{Original: openGaussTableOriginal(t, s, target), Destination: "retained_reference", WithData: true}
	out = s.SchemaDesign().Copy(ctx, ref)
	if out.Error != nil || !out.Verified {
		t.Fatal("retained self reference", out.State, out.Error)
	}
}

func TestOpenGaussTableCopyStaleDestinationPrivilegesAndExecutableProgramsFixture(t *testing.T) {
	for _, change := range []string{"destination", "stale", "grants", "program", "generated", "rowid", "generation"} {
		t.Run(change, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			original := openGaussTableOriginal(t, s, target)
			name := qualified(target.Object)
			ns := quote(target.Object.Schema)
			q := ""
			switch change {
			case "destination":
				q = "CREATE TABLE " + ns + ".copied(id bigint); INSERT INTO " + ns + ".copied VALUES(99)"
			case "stale":
				q = "COMMENT ON TABLE " + name + " IS 'external'"
			case "grants":
				q = "GRANT SELECT ON " + name + " TO PUBLIC"
			case "program":
				q = "CREATE FUNCTION " + ns + ".effect(n bigint) RETURNS boolean IMMUTABLE LANGUAGE plpgsql AS $$BEGIN RETURN n>0; END$$; ALTER TABLE " + name + " ADD CONSTRAINT effects CHECK(" + ns + ".effect(id))"
			case "generated":
				q = "CREATE FUNCTION " + ns + ".effect(n bigint) RETURNS bigint IMMUTABLE LANGUAGE plpgsql AS $$BEGIN RETURN n; END$$; ALTER TABLE " + name + " ADD COLUMN effect bigint GENERATED ALWAYS AS(" + ns + ".effect(id)) STORED"
			}
			if q != "" {
				if _, err := db.ExecContext(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			if change == "grants" || change == "program" || change == "generated" {
				original = openGaussTableOriginal(t, s, target)
			}
			r := contract.SchemaCopyRequest{Original: original, Destination: "copied", WithData: true, RebindSelfReferences: true}
			if change == "rowid" {
				r.PreserveRowID = true
			}
			if change == "generation" {
				r.Original.Target.Generation++
			}
			var effectsBefore int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+ns+".other").Scan(&effectsBefore); err != nil {
				t.Fatal(err)
			}
			out := s.SchemaDesign().Copy(ctx, r)
			if out.Error == nil || out.Verified || out.State == "committed" {
				t.Fatal("unsafe/stale copy accepted", change, out)
			}
			var effectsAfter int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+ns+".other").Scan(&effectsAfter); err != nil || effectsAfter != effectsBefore {
				t.Fatal("copy validation executed user code", effectsBefore, effectsAfter, err)
			}
			var exists bool
			if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname='copied')", target.Object.Schema).Scan(&exists); err != nil || exists != (change == "destination") {
				t.Fatal("failed copy left/overwrote destination", exists, err)
			}
		})
	}
}

func TestOpenGaussTableCopyWaitCancellationAndRevocationFixture(t *testing.T) {
	for _, mode := range []string{"change", "replace", "cancel", "revoke"} {
		t.Run(mode, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			original := openGaussTableOriginal(t, s, target)
			locker := openGaussTableLocker(t, db, target)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch := make(chan contract.SchemaDesignOutcome, 1)
			go func() {
				ch <- s.SchemaDesign().Copy(ctx, contract.SchemaCopyRequest{Original: original, Destination: "copied", WithData: true})
			}()
			openGaussViewWaiting(t, db, target.Object)
			switch mode {
			case "cancel":
				cancel()
			case "revoke":
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			case "change", "replace":
				q := "COMMENT ON TABLE " + qualified(target.Object) + " IS 'external'"
				if mode == "replace" {
					q = "DROP TABLE " + qualified(target.Object) + "; CREATE TABLE " + qualified(target.Object) + "(id bigint)"
				}
				if _, err := locker.ExecContext(ctx, q+"; COMMIT"); err != nil {
					t.Fatal(err)
				}
			}
			out := openGaussTableAwait(t, ch)
			if out.Error == nil || out.Verified || out.State != "rolled_back" && out.State != "uncertain" {
				t.Fatal("stale/cancelled copy", out)
			}
			if mode == "change" || mode == "replace" {
				if !errors.Is(out.Error, schemaddl.ErrConflict) {
					t.Fatal(out.Error)
				}
			}
		})
	}
}

func TestOpenGaussTableCopyLostCommitAcknowledgementFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	original := openGaussTableOriginal(t, s, target)
	cfg, err := openGaussConfig(s.backupProfile, os.Getenv("NYADB_OPENGAUSS_PASSWORD"), nil)
	if err != nil {
		t.Fatal(err)
	}
	connector, err := gausspq.NewConnectorConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	old := s.db
	t.Cleanup(func() { _ = old.Close() })
	s.db = sql.OpenDB(pgLostCommitConnector{Connector: connector})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r := contract.SchemaCopyRequest{Original: original, Destination: "copied", WithData: true}
	out := s.SchemaDesign().Copy(ctx, r)
	if out.State != "uncertain" || out.Verified || out.Actual != nil || !errors.Is(out.Error, io.ErrUnexpectedEOF) || !s.isClosed() {
		t.Fatal("lost copy receipt", out.State, out.Error)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+quote(target.Object.Schema)+".copied").Scan(&count); err != nil || count != 1 {
		t.Fatal("fixture did not drop committed receipt", count, err)
	}
	if next := s.SchemaDesign().Copy(ctx, r); next.State != "not_executed" || !errors.Is(next.Error, contract.ErrRevoked) {
		t.Fatal("ambiguous copy replayed", next)
	}
}

func TestOpenGaussTableCopyStreamsBeyondPreviewAndKeepsArrayEnumValuesFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ns := quote(target.Object.Schema)
	name := qualified(target.Object)
	q := "DROP TABLE " + name + "; CREATE TYPE " + ns + ".mood AS ENUM('开心','sleepy'); CREATE TABLE " + name + "(id bigint,note text,payload bytea,values numeric(40,20)[],matrix integer[][],mood " + ns + ".mood); INSERT INTO " + name + " SELECT g,repeat('长',2048),decode('00ff01','hex'),ARRAY[9007199254740993.12345678901234567890,NULL],ARRAY[[g,NULL],[g+1,g+2]], '开心'::" + ns + ".mood FROM generate_series(1,1201) g"
	// 'values' is reserved in the native parser, so quote its field identity.
	q = strings.ReplaceAll(q, "values numeric", `"values" numeric`)
	if _, err := db.ExecContext(ctx, q); err != nil {
		t.Fatal(err)
	}
	out := gaussCopy(t, s, target, "copied", true)
	if out.Error != nil || !out.Verified || out.Rows != 1201 {
		t.Fatal("full typed copy", out.State, out.Rows, out.Error)
	}
	var count, length int
	if err := db.QueryRowContext(ctx, "SELECT count(*),min(length(note)) FROM "+ns+".copied").Scan(&count, &length); err != nil || count != 1201 || length != 2048 {
		t.Fatal("copy truncated to preview", count, length, err)
	}
}

func TestOpenGaussTableCopyLargeOwnedSequenceKeeps128BitStateFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ns := quote(target.Object.Schema)
	name := qualified(target.Object)
	q := "DROP TABLE " + name + "; CREATE LARGE SEQUENCE " + ns + ".large_seq START 100000000000000000000 INCREMENT 7 MAXVALUE 170141183460469231731687303715884105727 CACHE 1; CREATE TABLE " + name + "(id NUMERIC(40,0) PRIMARY KEY DEFAULT nextval('" + target.Object.Schema + ".large_seq'::regclass),note text); ALTER LARGE SEQUENCE " + ns + ".large_seq OWNED BY " + name + ".id; COMMENT ON LARGE SEQUENCE " + ns + ".large_seq IS 'large sequence comment'; INSERT INTO " + name + "(note) VALUES('128-bit')"
	if _, err := db.ExecContext(ctx, q); err != nil {
		t.Fatal(err)
	}
	out := gaussCopy(t, s, target, "copied", true)
	if out.Error != nil || !out.Verified {
		t.Fatal("native large sequence copy", out.State, out.Error)
	}
	var id string
	if err := db.QueryRowContext(ctx, "INSERT INTO "+ns+".copied(note) VALUES('next') RETURNING id::text").Scan(&id); err != nil || id != "100000000000000000007" {
		t.Fatal("128-bit sequence lost precision", id, err)
	}
}

type gaussCorruptCopyConnector struct {
	driver.Connector
	destination string
	changed     *atomic.Bool
}

func (c gaussCorruptCopyConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &gaussCorruptCopyConnection{Conn: conn, connector: c}, nil
}

type gaussCorruptCopyConnection struct {
	driver.Conn
	connector gaussCorruptCopyConnector
}

func (c *gaussCorruptCopyConnection) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func (c *gaussCorruptCopyConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, "record_send(ROW(") && strings.Contains(q, "FROM "+c.connector.destination) && c.connector.changed.CompareAndSwap(false, true) {
		if _, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, "UPDATE "+c.connector.destination+" SET note='corrupted destination'", nil); err != nil {
			return nil, err
		}
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func TestOpenGaussTableCopyRejectsCorruptedValuesAndRollsBackWholeDestinationFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	original := openGaussTableOriginal(t, s, target)
	cfg, err := openGaussConfig(s.backupProfile, os.Getenv("NYADB_OPENGAUSS_PASSWORD"), nil)
	if err != nil {
		t.Fatal(err)
	}
	connector, err := gausspq.NewConnectorConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	old := s.db
	t.Cleanup(func() { _ = old.Close() })
	changed := &atomic.Bool{}
	s.db = sql.OpenDB(gaussCorruptCopyConnector{Connector: connector, destination: quote(target.Object.Schema) + `."copied"`, changed: changed})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out := s.SchemaDesign().Copy(ctx, contract.SchemaCopyRequest{Original: original, Destination: "copied", WithData: true})
	if !changed.Load() || out.State != "rolled_back" || out.Verified || out.Actual != nil || out.Error == nil || !strings.Contains(out.Error.Error(), "values failed verification") {
		t.Fatal("corrupted destination committed", out.State, out.Error)
	}
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname='copied')", target.Object.Schema).Scan(&exists); err != nil || exists {
		t.Fatal("failed verification left a destination", exists, err)
	}
	var note string
	if err := db.QueryRowContext(ctx, "SELECT note FROM "+qualified(target.Object)).Scan(&note); err != nil || note != "原始" {
		t.Fatal("verification rollback changed the source", note, err)
	}
}
