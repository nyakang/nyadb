package sqlworkspace

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqlresult"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/queries"
)

type graphTestLease struct {
	*testLease
	graph func(context.Context, contract.GraphQueryRequest) (contract.GraphResult, error)
}

func (l *graphTestLease) ExecuteGraph(ctx context.Context, r contract.GraphQueryRequest) (contract.GraphResult, error) {
	return l.graph(ctx, r)
}
func (l *graphTestLease) ExecuteFile(context.Context, string, io.Reader) (contract.SQLResult, error) {
	return contract.SQLResult{}, errors.New("generic file executor called")
}
func (l *graphTestLease) ExecuteStream(context.Context, string, string, sqlresult.Sink) (contract.SQLResult, error) {
	return contract.SQLResult{}, errors.New("generic stream executor called")
}

func TestNeo4jCypherKeyboardFullSourceAndNativePresentation(t *testing.T) {
	s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Connected: func(int64) bool { return true }})
	defer s.CloseAll()
	source := "// original; Cypher\nMATCH (n) WHERE n.text='a;b' RETURN n;\nRETURN 2"
	d := s.Open(1, source, 7, "neo4j", false)
	queries := make(chan string, 3)
	s.Executor = func(_ context.Context, id int64, db, q string) (contract.SQLResult, error) {
		queries <- q
		return contract.SQLResult{Graph: &contract.GraphResult{Attempted: true, Completed: true, Summary: contract.GraphSummary{Database: db}}}, nil
	}
	tt := ui.NewTester(func(c *ui.Context) { s.Consume(); s.View(c, d) }, 1280, 800)
	if e := tt.Click("Cypher 编辑器"); e != nil {
		t.Fatal(e)
	}
	d.Editor.Select(30, 30)
	tt.Key(commandModifier(), ui.KeyEnter)
	waitSQLUI(t, tt, d)
	if q := <-queries; q != source {
		t.Fatal("Cypher was split as SQL", q)
	}
	if tt.HasText("格式化 SQL") || tt.HasText("EXPLAIN") || tt.HasText("影响 -1 行") || !tt.HasText("原生执行已确认") {
		t.Fatal("SQL presentation leaked")
	}
	d.Editor.Select(0, len("// original; Cypher"))
	s.Run(nil, d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if q := <-queries; q != "// original; Cypher" {
		t.Fatal("selection lost")
	}
	s.Format(d)
	if d.Editor.Buffer.String() != source || d.Error() == "" {
		t.Fatal("SQL formatter rewrote Cypher")
	}
	s.Explain(d)
	if s.persistence.explainOpen {
		t.Fatal("SQL explain dialog enabled")
	}
	if d.Editor.Buffer.Language().Name != "Cypher" || d.Editor.Buffer.Language().LineComment != "//" {
		t.Fatal("wrong language")
	}
}

func TestNeo4jCypherParametersCapturedAndUncertainReceiptSurvivesRecovery(t *testing.T) {
	started, cancelled, ack := make(chan struct{}), make(chan struct{}), make(chan struct{})
	original := &graphTestLease{testLease: &testLease{}}
	original.graph = func(ctx context.Context, r contract.GraphQueryRequest) (contract.GraphResult, error) {
		close(started)
		if r.Database != "neo4j" || r.Parameters["exact"] != int64(9223372036854775807) || r.Parameters["nested"].(map[string]any)["value"] != "original" {
			t.Error("parameters lost precision or captured live draft")
		}
		<-ctx.Done()
		close(cancelled)
		<-ack
		return contract.GraphResult{Attempted: true, Uncertain: true}, errors.Join(ctx.Err(), contract.ErrSQLSessionLost)
	}
	fresh := &testLease{}
	original.replace = func() (Lease, error) { return fresh, nil }
	runtime := &testRuntime{reserve: func(int64, uint64) (Lease, error) { return original, nil }}
	s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Connected: func(int64) bool { return true }, Runtime: func() Runtime { return runtime }})
	defer s.CloseAll()
	d := s.Open(1, "CREATE (n {exact:$exact})", 7, "neo4j", false)
	d.graphParameters = `{"exact":9223372036854775807,"nested":{"value":"original"}}`
	s.Prepare(d)
	consumeUntil(t, s, func() bool { return d.prepareResults == nil })
	s.Run(nil, d)
	awaitSignal(t, started)
	d.graphParameters = `{"nested":{"value":"changed"}}`
	s.RequestCancel(d)
	awaitSignal(t, cancelled)
	s.Consume()
	if !d.Loading() {
		t.Fatal("settled before original acknowledgement")
	}
	close(ack)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if !d.NeedsRecovery() || d.Result() == nil || !d.Result().Graph.Uncertain || strings.Contains(d.Error(), "事务") {
		t.Fatal("lost native uncertainty", d.Error())
	}
	s.Run(nil, d)
	if d.Loading() {
		t.Fatal("uncertain query replayed")
	}
	s.Recover(d)
	consumeUntil(t, s, func() bool { return d.prepareResults == nil })
	if d.NeedsRecovery() || fresh.executions.Load() != 0 {
		t.Fatal("explicit recovery replayed")
	}
	s.DisconnectProfile(7)
	if !d.NeedsRecovery() || !strings.Contains(d.Error(), "Cypher") {
		t.Fatal("Cypher disconnect ownership")
	}
}

func TestNeo4jParameterValidationAndSnapshotsRetainExactGraph(t *testing.T) {
	for _, raw := range []string{`[]`, `{"a":1,"a":2}`, `{"n":9223372036854775808}`, `{"n":1e1000}`, `{} {}`, strings.Repeat(" ", 1<<20) + "{}"} {
		if _, e := graphParameters(raw); e == nil {
			t.Fatal("invalid parameters accepted")
		}
	}
	params, e := graphParameters(`{"n":9007199254740993,"nil":null,"list":[true,1.5]}`)
	if e != nil || params["n"] != int64(9007199254740993) {
		t.Fatal("exact parameter loss")
	}
	s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }})
	defer s.CloseAll()
	d := s.Open(1, "RETURN n", 7, "neo4j", false)
	d.setResult(&contract.SQLResult{Graph: &contract.GraphResult{Attempted: true, Completed: true, Rows: [][]contract.GraphValue{{{Kind: "node", Node: &contract.GraphNode{ElementID: "original", Properties: []contract.GraphProperty{{Name: "n", Value: contract.GraphValue{Kind: "integer", Text: "9223372036854775807"}}}}}}}, Summary: contract.GraphSummary{Counters: map[string]int64{"nodes-created": 1}, Bookmarks: []string{"native"}}}})
	if !s.pinResult(d) {
		t.Fatal("graph snapshot rejected")
	}
	snap := d.snapshots[0].Document
	d.result.Graph.Rows[0][0].Node.ElementID = "changed"
	d.result.Graph.Summary.Counters["nodes-created"] = 9
	if snap.result.Graph.Rows[0][0].Node.ElementID != "original" || snap.result.Graph.Summary.Counters["nodes-created"] != 1 || !snap.cypher() {
		t.Fatal("graph snapshot aliased live result")
	}
	restored := s.RestoreDocument(queries.Document{ID: 2, ProfileID: 7, Database: "neo4j", Text: "RETURN $temporary"})
	if !restored.cypher() || restored.graphParameters != "" || !restored.NeedsRecovery() {
		t.Fatal("restore connected or persisted temporary parameters")
	}
}

func TestNeo4jCypherFilesAndExportKeepParametersAndNativeReceipt(t *testing.T) {
	requests := make(chan contract.GraphQueryRequest, 2)
	lease := &graphTestLease{testLease: &testLease{}, graph: func(_ context.Context, r contract.GraphQueryRequest) (contract.GraphResult, error) {
		requests <- r
		return contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"exact"}, Rows: [][]contract.GraphValue{{{Kind: "integer", Text: "9223372036854775807"}}}, Summary: contract.GraphSummary{Database: "neo4j", Counters: map[string]int64{"nodes-created": 1}}}, nil
	}}
	s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Connected: func(int64) bool { return true }})
	defer s.CloseAll()
	var archive bytes.Buffer
	s.SetPersistence(nil, Files{OpenPath: func() (string, error) { return "query.cypher", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("RETURN $exact")), nil }, SavePath: func(string) (string, error) { return "result.zip", nil }, WriteStream: func(_ string, write func(io.Writer) error) error { return write(&archive) }}, nil)
	d := s.Open(1, "RETURN $exact", 7, "neo4j", false)
	d.lease = lease
	d.graphParameters = `{"exact":9223372036854775807}`
	s.RunFile(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if d.Error() != "" {
		t.Fatal(d.Error())
	}
	r := <-requests
	if d.Error() != "" || r.Query != "RETURN $exact" || r.Parameters["exact"] != int64(9223372036854775807) || d.Result().Graph == nil {
		t.Fatal("native file lost parameters or receipt", d.Error())
	}
	s.RunExport(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if d.Error() != "" {
		t.Fatal(d.Error())
	}
	r = <-requests
	if d.Error() != "" || r.Parameters["exact"] != int64(9223372036854775807) {
		t.Fatal("native export used generic executor", d.Error())
	}
	z, e := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for _, file := range z.File {
		seen[file.Name] = true
		if file.Name == "graph.json" {
			reader, e := file.Open()
			if e != nil {
				t.Fatal(e)
			}
			var graph contract.GraphResult
			e = json.NewDecoder(reader).Decode(&graph)
			reader.Close()
			if e != nil || !graph.Completed || graph.Rows[0][0].Text != "9223372036854775807" || graph.Summary.Counters["nodes-created"] != 1 {
				t.Fatal("native archive narrowed graph receipt")
			}
		}
	}
	if !seen["graph.json"] || !seen["manifest.json"] || !seen["result-001.jsonl"] {
		t.Fatal("native archive incomplete")
	}
}

func TestNeo4jCypherCompletionAndCommentsUseNativeGrammar(t *testing.T) {
	s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, GraphCompletions: func(id int64, db string) []GraphCompletion {
		if id != 7 || db != "neo4j" {
			t.Fatal("completion lost original scope")
		}
		return []GraphCompletion{{Name: "123Label", Kind: "label"}}
	}, CompletionCatalog: func(int64, string) sqltext.CompletionCatalog {
		t.Fatal("SQL completion used for Cypher")
		return sqltext.CompletionCatalog{}
	}})
	defer s.CloseAll()
	d := s.Open(1, "MATCH (n:", 7, "neo4j", false)
	tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 1280, 800)
	if e := tt.Click("Cypher 编辑器"); e != nil {
		t.Fatal(e)
	}
	d.Editor.Select(d.Editor.Buffer.Len(), d.Editor.Buffer.Len())
	d.Editor.OnCompletion(editor.CompletionRequest{Version: d.Editor.Buffer.Version(), Offset: d.Editor.Cursor()})
	tt.Frame()
	if !tt.HasText("123Label") || tt.HasText("SELECT") {
		t.Fatal("native label completion missing or SQL leaked")
	}
	tt.Key(0, ui.KeyEnter)
	if d.Editor.Buffer.String() != "MATCH (n:`123Label`" {
		t.Fatal("native numeric label was not quoted", d.Editor.Buffer.String())
	}
	tokens, state := cypherTokens("/* original", 0, nil)
	if state != 'B' || tokens[len(tokens)-1].Kind != editor.TokenComment {
		t.Fatal("block comment state lost")
	}
	tokens, state = cypherTokens("still */ MATCH (n) RETURN $exact", state, nil)
	keyword, parameter := false, false
	for _, token := range tokens {
		keyword = keyword || token.Kind == editor.TokenKeyword
		parameter = parameter || token.Kind == editor.TokenVariable
	}
	if state != 0 || !keyword || !parameter {
		t.Fatal("native tokens missing")
	}
}
