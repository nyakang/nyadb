package sqlworkspace

import (
	"context"
	"errors"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"strings"
	"testing"
)

func TestCassandraCQLKeyboardBatchSelectionScriptAndAcknowledgement(t *testing.T) {
	s := New(Dependencies{QueryDialect: func(int64) string { return "cassandra" }, Connected: func(int64) bool { return true }})
	defer s.CloseAll()
	batch := "BEGIN UNLOGGED BATCH\nINSERT INTO items(id,value) VALUES(1,'one;');\nINSERT INTO items(id,value) VALUES(2,'two');\nAPPLY BATCH;"
	source := "SELECT * FROM items;\n" + batch + "\nSELECT id FROM items;"
	d := s.Open(2, source, 7, "MixedCase", false)
	statements := make(chan string, 4)
	s.Executor = func(_ context.Context, id int64, db, q string) (contract.SQLResult, error) {
		if id != 7 || db != "MixedCase" {
			t.Error("lost original namespace")
		}
		statements <- q
		return contract.SQLResult{AffectedRows: -1, Sets: []contract.ResultSet{{AffectedRows: -1}}}, nil
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(); s.View(c, d); s.Modals(c) }, 1280, 800)
	if err := tt.Click("CQL 编辑器"); err != nil {
		t.Fatal(err)
	}
	at := strings.Index(source, "VALUES(2")
	d.Editor.Select(at, at)
	selection := d.Editor.Selection()
	tt.Key(commandModifier(), ui.KeyEnter)
	waitSQLUI(t, tt, d)
	if q := <-statements; q != strings.TrimSuffix(batch, ";") {
		t.Fatal("caret split native BATCH", q)
	}
	if d.Editor.Selection() != selection || !tt.HasText("原生执行已确认") || tt.HasText("影响 -1 行") || tt.HasText("EXPLAIN") || tt.HasText("格式化 SQL") {
		t.Fatalf("native CQL UI: selection %v vs %v, ack=%v negative=%v explain=%v format=%v summary=%q", d.Editor.Selection(), selection, tt.HasText("原生执行已确认"), tt.HasText("影响 -1 行"), tt.HasText("EXPLAIN"), tt.HasText("格式化 SQL"), sqlResultSummary(d))
	}
	tt.Key(0, ui.KeyF5)
	waitSQLUI(t, tt, d)
	if q := <-statements; q != source {
		t.Fatal("F5 changed native script")
	}
	d.Editor.Select(0, len("SELECT * FROM items;"))
	s.Run(nil, d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if q := <-statements; q != "SELECT * FROM items;" {
		t.Fatal("explicit selection lost")
	}
	before := d.Editor.Buffer.String()
	s.Format(d)
	if d.Error() == "" || d.Editor.Buffer.String() != before {
		t.Fatal("generic SQL formatter rewrote CQL")
	}
	s.Explain(d)
	if s.persistence.explainOpen || d.Error() == "" {
		t.Fatal("CQL SQL EXPLAIN enabled")
	}
	malformed := "BEGIN BATCH INSERT INTO items(id) VALUES(3);"
	d.Editor.Buffer.Set(malformed)
	d.Editor.Select(20, 20)
	if s.statementAtCaret(d) != malformed {
		t.Fatal("malformed BATCH fell back to SQL fragment")
	}
}

func TestCassandraCQLCancellationAcknowledgementAndExplicitRecovery(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	original := &testLease{execute: func(ctx context.Context, db, q string) (contract.SQLResult, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return contract.SQLResult{}, errors.Join(ctx.Err(), contract.ErrSQLSessionLost)
	}}
	fresh := &testLease{}
	original.replace = func() (Lease, error) { return fresh, nil }
	runtime := &testRuntime{reserve: func(int64, uint64) (Lease, error) { return original, nil }}
	s := New(Dependencies{QueryDialect: func(int64) string { return "cassandra" }, Connected: func(int64) bool { return true }, Runtime: func() Runtime { return runtime }})
	defer s.CloseAll()
	source := "UPDATE MixedCase.items SET count=count+1 WHERE id=1;"
	d := s.Open(1, source, 7, "MixedCase", false)
	s.Prepare(d)
	consumeUntil(t, s, func() bool { return d.prepareResults == nil })
	if original.executions.Load() != 0 {
		t.Fatal("preconnect executed CQL")
	}
	s.Run(nil, d)
	awaitSignal(t, started)
	s.RequestCancel(d)
	awaitSignal(t, canceled)
	s.Consume()
	if !d.Loading() {
		t.Fatal("cancellation settled before native acknowledgement")
	}
	close(release)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if !d.NeedsRecovery() || strings.Contains(d.Error(), "事务") || d.Editor.Buffer.String() != source {
		t.Fatal("CQL loss fabricated SQL state or discarded source", d.Error())
	}
	s.Recover(d)
	consumeUntil(t, s, func() bool { return d.prepareResults == nil })
	if d.NeedsRecovery() || fresh.executions.Load() != 0 || d.Editor.Buffer.String() != source {
		t.Fatal("explicit recovery replayed CQL")
	}
	s.DisconnectProfile(7)
	if !d.NeedsRecovery() || strings.Contains(d.Error(), "MySQL") || strings.Contains(d.Error(), "事务") {
		t.Fatal("disconnect has SQL recovery claims")
	}
}

func TestCassandraRestoredNamedAndReboundCQLKeepBatchScope(t *testing.T) {
	s := New(Dependencies{QueryDialect: func(id int64) string {
		if id == 7 {
			return "cassandra"
		}
		return "mysql"
	}})
	defer s.CloseAll()
	source := "BEGIN BATCH INSERT INTO items(id) VALUES(1); INSERT INTO items(id) VALUES(2); APPLY BATCH;"
	d := s.RestoreDocument(queries.Document{ID: 1, ProfileID: 7, Database: "MixedCase", Text: source})
	d.Editor.Select(55, 55)
	if !d.cql() || d.nativeCommands || !d.NeedsRecovery() || d.lease != nil || s.statementAtCaret(d) != strings.TrimSuffix(source, ";") {
		t.Fatal("restored CQL fell back to SQL fragments")
	}
	s.Rebind(d, 8)
	if d.cql() || d.lease != nil {
		t.Fatal("explicit SQL rebind retained CQL dialect or connected")
	}
	s.Rebind(d, 7)
	if !d.cql() || d.lease != nil {
		t.Fatal("CQL rebind lost native dialect or connected")
	}
	named := s.Open(2, "", 0, "", false)
	s.OpenNamed(named, queries.Named{ID: "native", Name: "CQL batch", ProfileID: 7, Database: "MixedCase", Text: source})
	named.Editor.Select(55, 55)
	if !named.cql() || !named.NeedsRecovery() || named.lease != nil || s.statementAtCaret(named) != strings.TrimSuffix(source, ";") {
		t.Fatal("offline named CQL split batch or connected")
	}
}
