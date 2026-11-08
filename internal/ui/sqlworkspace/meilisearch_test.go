package sqlworkspace

import (
	"context"
	"errors"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"strings"
	"testing"
)

func TestMeilisearchRESTWholeCaretSelectionAdmissionAndOriginalRestore(t *testing.T) {
	status := ""
	deps := Dependencies{QueryDialect: func(int64) string { return "meilisearch" }, Connected: func(int64) bool { return true }, Status: func(v string) { status = v }, Namespace: func(id int64, index string) contract.NamespaceRef {
		return contract.NamespaceRef{ProfileID: id, Catalog: index}
	}}
	s := New(deps)
	defer s.CloseAll()
	source := "POST /indexes/owned/documents\n" + `[{"id":"one","text":"原生;请求🐱","n":9007199254740993}]`
	d := s.Open(1, source, 7, "owned", false)
	calls := make(chan string, 3)
	var sinkFailure bool
	s.Executor = func(_ context.Context, id int64, index, q string) (contract.SQLResult, error) {
		if id != 7 || index != "owned" {
			t.Error("original request scope changed")
		}
		calls <- q
		result := contract.SQLResult{Steps: []contract.StatementOutcome{{SQL: "POST /indexes/owned/documents", State: contract.StatementEnqueued}}, Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "response"}}, Rows: [][]contract.Cell{{{Kind: contract.JSON, Text: `{"taskUid":0,"status":"enqueued"}`}}}}}}
		if sinkFailure {
			return result, errors.New("result display closed")
		}
		return result, nil
	}
	tt := ui.NewTester(func(c *ui.Context) { s.Consume(); s.View(c, d) }, 980, 700)
	if err := tt.Click("Meilisearch REST 编辑器"); err != nil {
		t.Fatal(err)
	}
	at := strings.Index(source, "请求")
	d.Editor.Select(at, at)
	tt.Key(commandModifier(), ui.KeyEnter)
	waitSQLUI(t, tt, d)
	if actual := <-calls; actual != source {
		t.Fatal("caret split native request")
	}
	if !strings.Contains(status, "任务已接收，尚未确认完成") || !tt.HasText("任务已接收，尚未确认完成") || tt.HasText("EXPLAIN") || tt.HasText("格式化 SQL") {
		t.Fatal(status)
	}
	d.Editor.Select(0, len(source))
	sinkFailure = true
	tt.Key(commandModifier(), ui.KeyEnter)
	waitSQLUI(t, tt, d)
	if actual := <-calls; actual != source || !strings.Contains(status, "任务已接收") || d.result == nil || d.result.Steps[0].State != contract.StatementEnqueued {
		t.Fatal(status)
	}
	snapshot := s.Snapshot(d.id, []uint64{d.id})
	restored := New(deps)
	defer restored.CloseAll()
	r := restored.RestoreDocument(snapshot.Documents[0])
	if !r.NeedsRecovery() || !r.restRequests() || r.Namespace().Catalog != "owned" || r.Editor.Buffer.String() != source {
		t.Fatal("original REST snapshot changed")
	}
	select {
	case <-calls:
		t.Fatal("restore replayed request")
	default:
	}
}
