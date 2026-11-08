package sqlworkspace

import (
	"context"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"strings"
	"testing"
)

func TestCouchDBRESTKeyboardDispatchAndSavedOriginalDialect(t *testing.T) {
	deps := Dependencies{QueryDialect: func(int64) string { return "couchdb" }, Connected: func(int64) bool { return true }, Namespace: func(id int64, db string) contract.NamespaceRef {
		return contract.NamespaceRef{ProfileID: id, Catalog: db}
	}}
	s := New(deps)
	defer s.CloseAll()
	source := "PUT /owned/document\n{\"n\":9007199254740993,\"text\":\"原始;请求🐱\",\"map\":\"function(doc) { emit(doc._id, doc.n); }\"}"
	d := s.Open(1, source, 7, "owned", false)
	calls := make(chan string, 2)
	s.Executor = func(_ context.Context, id int64, db, q string) (contract.SQLResult, error) {
		if id != 7 || db != "owned" {
			t.Error("native dispatch lost source scope")
		}
		calls <- q
		return contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "response"}}, Rows: [][]contract.Cell{{{Kind: contract.JSON, Text: `{"ok":true}`}}}}}}, nil
	}
	tt := ui.NewTester(func(c *ui.Context) { s.Consume(); s.View(c, d) }, 1280, 800)
	if err := tt.Click("CouchDB REST 编辑器"); err != nil {
		t.Fatal(err)
	}
	at := strings.Index(source, "emit(")
	d.Editor.Select(at, at)
	tt.Key(commandModifier(), ui.KeyEnter)
	waitSQLUI(t, tt, d)
	if actual := <-calls; actual != source {
		t.Fatal("SQL caret splitter changed native REST body")
	}
	if tt.HasText("EXPLAIN") || tt.HasText("格式化 SQL") || !d.restRequests() {
		t.Fatal("native REST inherited SQL tools")
	}
	snapshot := s.Snapshot(d.id, []uint64{d.id})
	restored := New(deps)
	defer restored.CloseAll()
	if len(snapshot.Documents) != 1 {
		t.Fatal("native request missing from snapshot")
	}
	r := restored.RestoreDocument(snapshot.Documents[0])
	if !r.NeedsRecovery() || !r.restRequests() || r.Namespace().Catalog != "owned" || r.Namespace().Schema != "" || r.Editor.Buffer.String() != source {
		t.Fatal("restored native source/dialect lost")
	}
	select {
	case <-calls:
		t.Fatal("restore replayed request")
	default:
	}
}
