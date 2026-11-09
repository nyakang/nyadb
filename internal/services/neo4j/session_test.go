package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func fakeSessionTest(t *testing.T) *Session {
	t.Helper()
	c, _ := fakeClient(t, normalResult())
	s := newSession(42, connections.Neo4jConfig{Database: "neo4j", RequestTimeoutSeconds: 40}, "original-secret", c)
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func TestNeo4jSessionLeaseReservationIsPureBoundedAndQualified(t *testing.T) {
	s := fakeSessionTest(t)
	s.openClient = func(context.Context, connections.Neo4jConfig, string) (*Client, error) {
		t.Fatal("reservation did I/O")
		return nil, nil
	}
	if _, e := s.ReserveQuery(0); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	for i := 1; i <= 32; i++ {
		if _, e := s.ReserveQuery(uint64(i)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.ReserveQuery(33); !errors.Is(e, contract.ErrSQLSessionLimit) {
		t.Fatal(e)
	}
	for _, n := range []contract.NamespaceRef{{ProfileID: 99, Catalog: "neo4j"}, {ProfileID: 42, Catalog: "neo4j", Schema: "other"}, {ProfileID: 42}} {
		if e := s.checkNamespace(n); !errors.Is(e, ErrInvalid) {
			t.Fatal("foreign namespace accepted", e)
		}
	}
	if s.Namespace("") != s.Namespace("neo4j") || s.Generation() != 1 || s.Capabilities().EditableTables || s.Capabilities().Transactions {
		t.Fatal("invented SQL capability or database")
	}
	if _, e := s.TableMetadata(context.Background(), s.Namespace("neo4j").Object("label", "Person")); !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal(e)
	}
	_ = s.Close()
	if s.secret != "" || len(s.leases) != 0 || s.Alive() {
		t.Fatal("closed session retained secrets/leases")
	}
	if _, e := s.ReserveQuery(1); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal(e)
	}
}
func TestNeo4jSessionTypedResultAndCompletedSinkFailureRetainReceipt(t *testing.T) {
	s := fakeSessionTest(t)
	c, d := fakeClient(t, normalResult())
	s.openClient = func(context.Context, connections.Neo4jConfig, string) (*Client, error) { return c, nil }
	lease, e := s.ReserveQuery(1)
	if e != nil {
		t.Fatal(e)
	}
	r, e := lease.ExecuteStream(context.Background(), "neo4j", "RETURN 1", func(_ int, _ []contract.Column, row []contract.Cell) error {
		row[0].Text = "changed"
		return errors.New("sink failed")
	})
	if e == nil || r.Graph == nil || !r.Graph.Completed || r.Graph.Uncertain || r.Steps[0].State != "completed" || r.Sets[0].Rows[0][0].Text == "changed" {
		t.Fatal("completion/projection lost", e)
	}
	if d.session.runs.Load() != 1 || !c.Alive() {
		t.Fatal("sink failure retired/replayed a completed query")
	}
	r.Graph.Rows[0][0].Properties[0].Value.Text = "bad"
	if c.LastResponse().Rows[0][0].Properties[0].Value.Text != "AP8=" {
		t.Fatal("contract graph aliases original")
	}
	params := map[string]any{"payload": []byte{0, 255}, "nested": map[string]any{"integer": int64(9223372036854775807)}}
	d.session.run = func(_ context.Context, _ string, p map[string]any, _ ...func(*nd.TransactionConfig)) (nd.Result, error) {
		if !reflect.DeepEqual(p, params) {
			t.Fatal("typed parameters changed")
		}
		return normalResult(), nil
	}
	if _, e = lease.(contract.GraphQueryLease).ExecuteGraph(context.Background(), Request{Query: "RETURN $payload", Parameters: params}); e != nil {
		t.Fatal(e)
	}
}
func TestNeo4jSessionUncertainNativeFailureRequiresExplicitReplacement(t *testing.T) {
	s := fakeSessionTest(t)
	original, d := fakeClient(t, normalResult())
	replacement, rd := fakeClient(t, normalResult())
	clients := []*Client{original, replacement}
	s.openClient = func(context.Context, connections.Neo4jConfig, string) (*Client, error) {
		c := clients[0]
		clients = clients[1:]
		return c, nil
	}
	l, _ := s.ReserveQuery(1)
	d.session.run = func(context.Context, string, map[string]any, ...func(*nd.TransactionConfig)) (nd.Result, error) {
		return nil, &nd.Neo4jError{Code: "Neo.ClientError.Statement.SyntaxError"}
	}
	r, e := l.Execute(context.Background(), "neo4j", "invalid arbitrary procedure")
	if !errors.Is(e, contract.ErrSQLSessionLost) || r.Graph == nil || !r.Graph.Uncertain || r.Steps[0].State != "uncertain" || r.Steps[0].Error == "" || original.Alive() || !s.Alive() {
		t.Fatal("missing completion retained live executor", e)
	}
	if _, e = l.Execute(context.Background(), "neo4j", "RETURN 1"); !errors.Is(e, contract.ErrSQLSessionLost) || d.session.runs.Load() != 1 {
		t.Fatal("old lease replayed", e)
	}
	next, e := l.Replace()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = next.Execute(context.Background(), "neo4j", "RETURN 1"); e != nil || rd.session.runs.Load() != 1 {
		t.Fatal("explicit original replacement failed", e)
	}
	l.Release()
	if !replacement.Alive() {
		t.Fatal("old release closed replacement")
	}
}
func TestNeo4jSessionCloseWaitsLateOriginalOpenAndCancelsWithoutReplacement(t *testing.T) {
	s := fakeSessionTest(t)
	late, d := fakeClient(t, normalResult())
	entered, ack := make(chan struct{}), make(chan struct{})
	s.openClient = func(ctx context.Context, _ connections.Neo4jConfig, _ string) (*Client, error) {
		close(entered)
		<-ctx.Done()
		<-ack
		return late, nil
	}
	l, _ := s.ReserveQuery(1)
	done := make(chan error, 1)
	go func() { done <- l.Preconnect(context.Background()) }()
	<-entered
	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("close returned before captured handshake acknowledged")
	case <-time.After(20 * time.Millisecond):
	}
	close(ack)
	if e := <-done; e == nil {
		t.Fatal("late original open published")
	}
	<-closed
	if late.Alive() || d.closes.Load() != 1 || d.session.runs.Load() != 0 {
		t.Fatal("late captured handle remained alive or executed")
	}
}

func TestNeo4jGraphMetadataRetainsOverloadsAndQualifiedObjects(t *testing.T) {
	n := contract.NamespaceRef{ProfileID: 42, Catalog: "neo4j"}
	snapshot := contract.GraphSchemaSnapshot{Namespace: n,
		Labels: Response{Columns: []string{"label"}, Rows: [][]Value{{{Kind: "string", Text: "same"}}}, Completed: true},
		Functions: Response{Columns: []string{"name", "signature"}, Rows: [][]Value{
			{{Kind: "string", Text: "same"}, {Kind: "string", Text: "same(integer) :: integer"}},
			{{Kind: "string", Text: "same"}, {Kind: "string", Text: "same(string) :: string"}},
		}, Completed: true},
	}
	objects, e := graphObjects(snapshot)
	if e != nil || len(objects) != 2 {
		t.Fatal("overload became duplicate or separate scope", e)
	}
	var function contract.DBObject
	for _, o := range objects {
		if o.Ref != n.Object(o.Kind, o.Name) {
			t.Fatal("lost qualified identity")
		}
		if o.Kind == "function" {
			function = o
		}
	}
	var native Response
	if json.Unmarshal([]byte(function.Definition), &native) != nil || !native.Completed || len(native.Rows) != 2 || native.Rows[1][1].Text != "same(string) :: string" {
		t.Fatal("native overload rows/receipt narrowed")
	}
	snapshot.Indexes = Response{Columns: []string{"name"}, Rows: [][]Value{{{Kind: "string", Text: "duplicate"}}, {{Kind: "string", Text: "duplicate"}}}}
	if _, e = graphObjects(snapshot); !errors.Is(e, ErrProtocol) {
		t.Fatal("duplicate native index identity accepted", e)
	}
}
