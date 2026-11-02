package engines

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestSchemaGateWaitsCancelWithoutRevokingExistingReaders(t *testing.T) {
	var g schemaGate
	reader, err := g.acquire(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = g.acquire(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	second, err := g.acquire(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	second()
	reader()
	writer, err := g.acquire(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err = g.acquire(wait, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	writer()
	reader, err = g.acquire(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	reader()
}

func TestSQLiteDesignSeesTemporaryTriggersOnAnotherOwnedSQLConnection(t *testing.T) {
	s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT)")
	s.db.SetMaxOpenConns(2)
	original := designSnapshot(t, s, "main", "t")
	l, err := s.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err = l.Execute(context.Background(), "CREATE TEMP TRIGGER sql_session_trigger AFTER UPDATE ON main.t BEGIN SELECT 1; END"); err != nil {
		t.Fatal(err)
	}
	target := contract.SchemaDesignTarget{Object: s.Namespace("main").Object("table", "t"), Generation: s.Generation()}
	if _, err = s.SchemaDesign().Snapshot(context.Background(), target); err == nil || !strings.Contains(err.Error(), "temporary triggers") {
		t.Fatal(err)
	}
	out := s.SchemaDesign().Apply(context.Background(), addSQLiteColumn(t, original, "added", "TEXT"))
	if out.State != "not_executed" || out.Error == nil || !strings.Contains(out.Error.Error(), "temporary triggers") {
		t.Fatalf("missed leased trigger: %+v", out)
	}
	if _, err = l.Execute(context.Background(), "DROP TRIGGER temp.sql_session_trigger"); err != nil {
		t.Fatal(err)
	}
	out = s.SchemaDesign().Apply(context.Background(), addSQLiteColumn(t, original, "added", "TEXT"))
	requireDesignCommit(t, out)
	if _, err = l.Execute(context.Background(), "SELECT added FROM t"); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteDesignRejectsManualSQLTransactionWithoutBlockingItsCommit(t *testing.T) {
	for _, foreignKeys := range []string{"ON", "OFF"} {
		t.Run(foreignKeys, func(t *testing.T) {
			s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT)")
			s.db.SetMaxOpenConns(2)
			original := designSnapshot(t, s, "main", "t")
			l, err := s.Reserve(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			if _, err = l.Execute(context.Background(), "PRAGMA foreign_keys="+foreignKeys+"; BEGIN IMMEDIATE; INSERT INTO t VALUES(1,'owned transaction')"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			out := s.SchemaDesign().Apply(ctx, addSQLiteColumn(t, original, "added", "TEXT"))
			if out.State != "not_executed" || out.Error == nil || !strings.Contains(out.Error.Error(), "open transaction") {
				t.Fatalf("manual transaction: %+v", out)
			}
			if _, err = l.Execute(ctx, "COMMIT"); err != nil {
				t.Fatalf("writer stranded manual COMMIT: %v", err)
			}
			result, err := l.Execute(ctx, "PRAGMA foreign_keys; SELECT value FROM t")
			want := "1"
			if foreignKeys == "OFF" {
				want = "0"
			}
			if err != nil || len(result.Sets) != 2 || result.Sets[0].Rows[0][0].Text != want || result.Sets[1].Rows[0][0].Text != "owned transaction" {
				t.Fatalf("SQL state changed: %+v %v", result, err)
			}
			out = s.SchemaDesign().Apply(ctx, addSQLiteColumn(t, original, "added", "TEXT"))
			requireDesignCommit(t, out)
		})
	}
}

func TestSQLiteQueryAndSnapshotWaitingForSchemaWriterCanCancel(t *testing.T) {
	s := sqliteDesignFixture(t, "CREATE TABLE t(id INTEGER PRIMARY KEY,value TEXT)")
	s.db.SetMaxOpenConns(2)
	l, err := s.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	writer, err := s.schema.acquire(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err = l.Execute(ctx, "SELECT 1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	target := contract.SchemaDesignTarget{Object: s.Namespace("main").Object("table", "t"), Generation: s.Generation()}
	if _, err = s.SchemaDesign().Snapshot(ctx, target); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	writer()
	if _, err = l.Execute(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("canceled wait damaged original lease: %v", err)
	}
}
