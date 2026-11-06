package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	gausspq "gitcode.com/opengauss/openGauss-connector-go-pq"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func openGaussTableFixture(t *testing.T) (*Session, *sql.DB, contract.SchemaDesignTarget) {
	t.Helper()
	s, p := openGaussFixture(t)
	db, err := (openGaussDialect{}).open(p, os.Getenv("NYADB_OPENGAUSS_PASSWORD"), nil)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("nyadb_gauss_table_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+quote(schema)+"; CREATE TABLE "+quote(schema)+".original(id BIGSERIAL PRIMARY KEY,note TEXT,amount NUMERIC(40,20),twice NUMERIC(40,20) GENERATED ALWAYS AS(amount*2) STORED); INSERT INTO "+quote(schema)+".original(note,amount) VALUES('原始',9007199254740993.12345678901234567890); CREATE TABLE "+quote(schema)+".other(id bigint)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DROP SCHEMA "+quote(schema)+" CASCADE"); err != nil {
			t.Error(err)
		}
		_ = db.Close()
	})
	return s, db, contract.SchemaDesignTarget{Object: s.Namespace(schema).Object("table", "original"), Generation: s.Generation()}
}

func openGaussTableOriginal(t *testing.T, s *Session, target contract.SchemaDesignTarget) contract.SchemaDesignSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x, err := s.SchemaDesign().Snapshot(ctx, target)
	if err != nil || !x.Schema.Exists || x.Schema.OpenGauss == nil || x.Schema.Postgres != nil || x.Schema.Engine != "opengauss" || x.Target != target {
		t.Fatal("native original table snapshot", err, x)
	}
	return x
}

func TestOpenGaussTableDropOwnedObjectsAndNativeStorageFixture(t *testing.T) {
	for _, storage := range []string{"row", "unlogged", "column", "partition"} {
		t.Run(storage, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			name := qualified(target.Object)
			q := "COMMENT ON TABLE " + name + " IS 'a quote '' and 中文'; COMMENT ON COLUMN " + name + ".note IS 'column '' comment'; CREATE INDEX note_idx ON " + name + "(note); CREATE RULE keep_updates AS ON UPDATE TO " + name + " DO ALSO NOTHING; CREATE FUNCTION " + quote(target.Object.Schema) + ".keep_note() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$; CREATE TRIGGER keep_note BEFORE INSERT ON " + name + " FOR EACH ROW EXECUTE PROCEDURE " + quote(target.Object.Schema) + ".keep_note()"
			if storage == "unlogged" {
				q = "DROP TABLE " + name + "; CREATE UNLOGGED TABLE " + name + "(id bigint,note text); INSERT INTO " + name + " VALUES(1,'unlogged')"
			} else if storage == "column" {
				q = "DROP TABLE " + name + "; CREATE TABLE " + name + "(id bigint,note text) WITH(orientation=column,compression=middle); INSERT INTO " + name + " VALUES(1,'列存')"
			} else if storage == "partition" {
				q = "DROP TABLE " + name + "; CREATE TABLE " + name + "(id bigint,note text) PARTITION BY RANGE(id)(PARTITION p1 VALUES LESS THAN(10),PARTITION p2 VALUES LESS THAN(MAXVALUE)); INSERT INTO " + name + " VALUES(1,'分区')"
			}
			if _, err := db.ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
			original := openGaussTableOriginal(t, s, target)
			out := s.SchemaDesign().Drop(ctx, original)
			if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil || out.Actual.Schema.Exists || out.Actual.Target != target {
				t.Fatal("native drop", out)
			}
			var other bool
			if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname='other')", target.Object.Schema).Scan(&other); err != nil || !other {
				t.Fatal("native drop removed unrelated table", err)
			}
			if next := s.SchemaDesign().Drop(ctx, original); next.Error == nil || next.Verified || next.State == "committed" {
				t.Fatal("original confirmation replayed", next)
			}
		})
	}
}

func TestOpenGaussTableDropOwnerQuotedIdentityAndGenerationFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	foreign := contract.SchemaDesignTarget{Object: s.Namespace("public").Object("table", "records"), Generation: s.Generation()}
	if _, err := s.SchemaDesign().Snapshot(ctx, foreign); err == nil {
		t.Fatal("granted table permissions accepted as ownership")
	}
	quoted := target
	quoted.Object.Name = `q"; DROP TABLE other; --`
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+qualified(target.Object)+" RENAME TO "+quote(quoted.Object.Name)); err != nil {
		t.Fatal(err)
	}
	original := openGaussTableOriginal(t, s, quoted)
	wrong := original.Clone()
	wrong.Target.Generation++
	if out := s.SchemaDesign().Drop(ctx, wrong); out.State != "not_executed" || !errors.Is(out.Error, contract.ErrRevoked) {
		t.Fatal("obsolete original dispatched DDL", out)
	}
	wrong = original.Clone()
	wrong.Schema.OpenGauss.Entries[0].Data = []byte(`{"tampered":true}`)
	if out := s.SchemaDesign().Drop(ctx, wrong); out.State != "not_executed" || !errors.Is(out.Error, schemaddl.ErrConflict) {
		t.Fatal("mutated native catalog became execution baseline", out)
	}
	out := s.SchemaDesign().Drop(ctx, original)
	if out.State != "committed" || out.Error != nil || !out.Verified {
		t.Fatal("quoted native table drop", out)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+quote(target.Object.Schema)+".other").Scan(&count); err != nil {
		t.Fatal("quoted identity executed SQL text", err)
	}
}

func TestOpenGaussTableDropFreshReadWithoutSchemaCreatePrivilegeFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	original := openGaussTableOriginal(t, s, target)
	if _, err := db.ExecContext(ctx, "REVOKE CREATE ON SCHEMA "+quote(target.Object.Schema)+" FROM nyadb_it"); err != nil {
		t.Fatal(err)
	}
	if out := s.SchemaDesign().Drop(ctx, original); out.State != "rolled_back" || !errors.Is(out.Error, schemaddl.ErrConflict) || out.Verified {
		t.Fatal("changed schema grants ignored", out)
	}
	fresh := openGaussTableOriginal(t, s, target)
	if out := s.SchemaDesign().Drop(ctx, fresh); out.Error != nil || !out.Verified || out.State != "committed" {
		t.Fatal("owner deletion incorrectly requires schema CREATE", out)
	}
}

func TestOpenGaussTableDropRestrictsViewAndForeignKeyDependenciesFixture(t *testing.T) {
	for _, dependency := range []string{"view", "foreign_key"} {
		t.Run(dependency, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			q := "CREATE VIEW " + quote(target.Object.Schema) + ".dependent AS SELECT * FROM " + qualified(target.Object)
			if dependency == "foreign_key" {
				q = "CREATE TABLE " + quote(target.Object.Schema) + ".dependent(id bigint REFERENCES " + qualified(target.Object) + "(id))"
			}
			if _, err := db.ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
			original := openGaussTableOriginal(t, s, target)
			out := s.SchemaDesign().Drop(ctx, original)
			var native *gausspq.Error
			if out.State != "rolled_back" || out.Verified || out.Actual != nil || !errors.As(out.Error, &native) || native.Code != "2BP01" {
				t.Fatal("dependent object cascaded or error lost", out)
			}
			var amount string
			if err := db.QueryRowContext(ctx, "SELECT amount::text FROM "+qualified(target.Object)).Scan(&amount); err != nil || amount != "9007199254740993.12345678901234567890" {
				t.Fatal("failed native drop changed rows", amount, err)
			}
			current := openGaussTableOriginal(t, s, target)
			if current.Schema.Revision != original.Schema.Revision {
				t.Fatal("rolled back drop changed native catalog")
			}
		})
	}
}

func openGaussTableAwait(t *testing.T, ch <-chan contract.SchemaDesignOutcome) contract.SchemaDesignOutcome {
	t.Helper()
	select {
	case out := <-ch:
		return out
	case <-time.After(10 * time.Second):
		t.Fatal("native table did not acknowledge completion")
		return contract.SchemaDesignOutcome{}
	}
}

func openGaussTableLocker(t *testing.T, db *sql.DB, target contract.SchemaDesignTarget) *sql.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rollbackDesign(c); _ = c.Close() })
	if _, err := c.ExecContext(ctx, "BEGIN; LOCK TABLE "+qualified(target.Object)+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOpenGaussTableDropWaitRejectsExternalChangeAndReplacementFixture(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprint(replace), func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			original := openGaussTableOriginal(t, s, target)
			locker := openGaussTableLocker(t, db, target)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			ch := make(chan contract.SchemaDesignOutcome, 1)
			go func() { ch <- s.SchemaDesign().Drop(ctx, original) }()
			openGaussViewWaiting(t, db, target.Object)
			q := "COMMENT ON TABLE " + qualified(target.Object) + " IS 'external change'"
			if replace {
				q = "DROP TABLE " + qualified(target.Object) + "; CREATE TABLE " + qualified(target.Object) + "(id bigint,note text); INSERT INTO " + qualified(target.Object) + " VALUES(2,'replacement')"
			}
			if _, err := locker.ExecContext(ctx, q+"; COMMIT"); err != nil {
				t.Fatal(err)
			}
			out := openGaussTableAwait(t, ch)
			if out.State != "rolled_back" || !errors.Is(out.Error, schemaddl.ErrConflict) || out.Verified || out.Actual != nil {
				t.Fatal("stale drop destroyed external catalog", out)
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+qualified(target.Object)).Scan(&count); err != nil || count != 1 {
				t.Fatal("external table lost", err, count)
			}
		})
	}
}

func TestOpenGaussTableDropCancellationAndRevocationAcknowledgeFixture(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			original := openGaussTableOriginal(t, s, target)
			locker := openGaussTableLocker(t, db, target)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch := make(chan contract.SchemaDesignOutcome, 1)
			go func() { ch <- s.SchemaDesign().Drop(ctx, original) }()
			openGaussViewWaiting(t, db, target.Object)
			if revoke {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			out := openGaussTableAwait(t, ch)
			if out.State != "rolled_back" && out.State != "uncertain" || out.Verified || out.Actual != nil || out.Error == nil || !revoke && !errors.Is(out.Error, context.Canceled) {
				t.Fatal("cancel/revocation permitted replay", out)
			}
			if out.State == "uncertain" && !s.isClosed() {
				t.Fatal("uncertain original remains usable")
			}
			if err := rollbackDesign(locker); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM " + qualified(target.Object)).Scan(&count); err != nil || count != 1 {
				t.Fatal("cancelled native drop executed", err)
			}
		})
	}
}

func TestOpenGaussTableDropLostCommitReceiptClosesOriginalFixture(t *testing.T) {
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
	originalDB := s.db
	t.Cleanup(func() { _ = originalDB.Close() })
	s.db = sql.OpenDB(pgLostCommitConnector{Connector: connector})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out := s.SchemaDesign().Drop(ctx, original)
	if out.State != "uncertain" || out.Verified || out.Actual != nil || !errors.Is(out.Error, io.ErrUnexpectedEOF) || !s.isClosed() {
		t.Fatal("lost commit receipt permitted replay", out)
	}
	var remains bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_class WHERE oid=$1::oid)", original.Schema.OpenGauss.RelationOID).Scan(&remains); err != nil || remains {
		t.Fatal("test did not lose committed acknowledgement", remains, err)
	}
	if next := s.SchemaDesign().Drop(ctx, original); next.State != "not_executed" || !errors.Is(next.Error, contract.ErrRevoked) {
		t.Fatal("lost original replayed drop", next)
	}
}
