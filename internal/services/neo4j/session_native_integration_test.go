package neo4j

import (
	"context"
	"errors"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func sessionFixtureTest(t *testing.T) nativeFixture {
	t.Helper()
	if os.Getenv("NYADB_NEO4J_SESSION_IT") != "1" {
		t.Skip("requires owned native session fixture")
	}
	return fixtureTest(t)
}
func nativeSessionTest(t *testing.T, f nativeFixture) *Session {
	t.Helper()
	u, e := url.Parse(f.Config.URI)
	if e != nil {
		t.Fatal(e)
	}
	p, _ := connections.Defaults(connections.Neo4j)
	p.ID, p.Name, p.Host, p.Database, p.Username = 42, "owned original", u.Hostname(), f.Config.Database, f.Config.Username
	n, e := strconv.ParseUint(u.Port(), 10, 16)
	if e != nil {
		t.Fatal(e)
	}
	p.Port = uint16(n)
	p.TLSMode = "disable"
	if strings.HasSuffix(u.Scheme, "+s") {
		p.TLSMode = "verify-full"
	}
	if strings.HasSuffix(u.Scheme, "+ssc") {
		p.TLSMode = "require"
	}
	p.Neo4jOptions.Protocol = strings.Split(u.Scheme, "+")[0]
	p.Neo4jOptions.Auth, p.Neo4jOptions.RoutingContext = f.Config.Auth, u.RawQuery
	p.Neo4jOptions.CAFile, p.Neo4jOptions.ClientCertFile, p.Neo4jOptions.ClientKeyFile = f.Config.CAFile, f.Config.ClientCertFile, f.Config.ClientKeyFile
	p.Neo4jOptions.ConnectTimeoutSeconds, p.Neo4jOptions.RequestTimeoutSeconds = f.Config.ConnectTimeoutSeconds, f.Config.RequestTimeoutSeconds
	raw, e := OpenSession(context.Background(), p, f.Secret)
	if e != nil {
		t.Fatalf("original native session open: %v", e)
	}
	s := raw.(*Session)
	t.Cleanup(func() {
		if e := s.Close(); e != nil {
			t.Error(e)
		}
	})
	return s
}
func nativeGraphLeaseTest(t *testing.T, s *Session, document uint64) contract.GraphQueryLease {
	t.Helper()
	l, e := s.ReserveQuery(document)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(l.Release)
	return l.(contract.GraphQueryLease)
}
func hasNativeNameTest(r Response, column, name string) bool {
	for i := range r.Rows {
		v, e := stringColumn(r, i, column)
		if e == nil && v == name {
			return true
		}
	}
	return false
}
func TestNeo4jNativeSessionQualifiedMetadataAndOriginalDatabase(t *testing.T) {
	f := sessionFixtureTest(t)
	admin := nativeOpenTest(t, f)
	s := nativeSessionTest(t, f)
	label, rel, index, constraint := "NyaLabel_"+f.Nonce, "NyaRel_"+f.Nonce, "NyaIndex_"+f.Nonce, "NyaConstraint_"+f.Nonce
	marker := markerTest(f, "session_metadata")
	t.Cleanup(func() {
		nativeRunTest(t, admin, Request{Query: "DROP INDEX `" + index + "` IF EXISTS"})
		nativeRunTest(t, admin, Request{Query: "DROP CONSTRAINT `" + constraint + "` IF EXISTS"})
		cleanupMarkerTest(t, admin, marker)
	})
	nativeRunTest(t, admin, Request{Query: "CREATE (a:NyaFoundation:`" + label + "` {marker:$marker,key:'first',exact:$exact})-[r:`" + rel + "` {value:$exact}]->(b:NyaFoundation:`" + label + "` {marker:$marker,key:'second'})", Parameters: map[string]any{"marker": marker, "exact": int64(math.MaxInt64)}})
	nativeRunTest(t, admin, Request{Query: "CREATE INDEX `" + index + "` FOR (n:`" + label + "`) ON (n.marker)"})
	nativeRunTest(t, admin, Request{Query: "CREATE CONSTRAINT `" + constraint + "` FOR (n:`" + label + "`) REQUIRE n.key IS UNIQUE"})
	namespaces, e := s.Namespaces(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for _, n := range namespaces {
		if n.Ref.ProfileID != 42 || n.Ref.Schema != "" || n.Ref.Catalog != n.Label {
			t.Fatal("namespace became label")
		}
		seen[n.Ref.Catalog] = true
	}
	if !seen["neo4j"] || !seen["system"] {
		t.Fatal("native databases missing")
	}
	snapshot, e := s.GraphSchema(context.Background(), s.Namespace("neo4j"))
	if e != nil {
		t.Fatal(e)
	}
	if !hasNativeNameTest(snapshot.Labels, "label", label) || !hasNativeNameTest(snapshot.RelationshipTypes, "relationshipType", rel) || !hasNativeNameTest(snapshot.Indexes, "name", index) || !hasNativeNameTest(snapshot.Constraints, "name", constraint) || len(snapshot.Procedures.Rows) == 0 || len(snapshot.Functions.Rows) == 0 {
		t.Fatal("native graph schema not retained")
	}
	properties := false
	for i := range snapshot.NodeProperties.Rows {
		if columnTest(t, snapshot.NodeProperties, i, "propertyName").Text == "exact" {
			for _, v := range columnTest(t, snapshot.NodeProperties, i, "nodeLabels").Values {
				if v.Text == label {
					properties = true
				}
			}
		}
	}
	if !properties {
		t.Fatal("inferred native node property metadata missing")
	}
	functionNames := map[string]int{}
	for i := range snapshot.Functions.Rows {
		name, e := stringColumn(snapshot.Functions, i, "name")
		if e != nil {
			t.Fatal(e)
		}
		functionNames[name]++
	}
	for name, count := range functionNames {
		if count > 1 {
			t.Logf("native overloaded function %s has %d signature rows", name, count)
		}
	}
	objects, e := s.Browse(context.Background(), s.Namespace("neo4j"))
	if e != nil {
		t.Fatal(e)
	}
	matched := map[string]bool{}
	for _, o := range objects {
		if o.Name == label || o.Name == rel || o.Name == index || o.Name == constraint {
			if o.Ref != s.Namespace("neo4j").Object(o.Kind, o.Name) || o.Definition == "" || o.Kind == "table" {
				t.Fatal("graph object identity/definition lost")
			}
			matched[o.Name] = true
		}
	}
	if len(matched) != 4 {
		t.Fatal("native objects omitted")
	}
	if _, e = s.GraphSchema(context.Background(), contract.NamespaceRef{ProfileID: 99, Catalog: "neo4j"}); !errors.Is(e, ErrInvalid) {
		t.Fatal("foreign profile metadata accepted", e)
	}
	if _, e = s.GraphSchema(context.Background(), s.Namespace("system")); !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal("system database treated as graph", e)
	}
	initial := f
	initial.Config.Database = ""
	home := nativeSessionTest(t, initial)
	if home.Namespace("").Catalog != "neo4j" {
		t.Fatal("home database guessed")
	}
	system := f
	system.Config.Database = "system"
	systemSession := nativeSessionTest(t, system)
	l := nativeGraphLeaseTest(t, systemSession, 1)
	r, e := l.Execute(context.Background(), "system", "SHOW DATABASES YIELD name RETURN name")
	if e != nil || r.Graph == nil || r.Graph.Summary.Database != "system" {
		t.Fatal("original system database lost", e)
	}
}
func TestNeo4jNativeSessionTypedQueriesCompletionAndExplicitRecovery(t *testing.T) {
	f := sessionFixtureTest(t)
	s := nativeSessionTest(t, f)
	l := nativeGraphLeaseTest(t, s, 1)
	independent := nativeGraphLeaseTest(t, s, 2)
	if e := l.Preconnect(context.Background()); e != nil {
		t.Fatal(e)
	}
	marker := markerTest(f, "session_typed")
	admin := nativeOpenTest(t, f)
	t.Cleanup(func() { cleanupMarkerTest(t, admin, marker) })
	r, e := l.ExecuteGraph(context.Background(), Request{Query: "CREATE (a:NyaFoundation {marker:$marker,exact:$exact})-[r:NyaSessionRelation]->(b:NyaFoundation {marker:$marker}) RETURN a,r,b,[$exact,null,$bytes] AS nested", Parameters: map[string]any{"marker": marker, "exact": int64(math.MaxInt64), "bytes": []byte{0, 255}}, MaxRows: 3})
	if e != nil || !r.Completed || r.Uncertain || r.Summary.Counters["nodes_created"] != 2 || r.Rows[0][0].Node.ElementID == "" || r.Rows[0][1].Relationship.StartElementID != r.Rows[0][0].Node.ElementID || r.Rows[0][3].Values[0].Text != "9223372036854775807" {
		t.Fatal("typed native lease result narrowed", e)
	}
	full, e := l.Execute(context.Background(), "neo4j", "MATCH p=(a:NyaFoundation)-[:NyaSessionRelation]->(b:NyaFoundation) RETURN p LIMIT 1")
	if e != nil || full.Graph == nil || full.Graph.Rows[0][0].Path == nil || full.Sets[0].Rows[0][0].Kind != contract.JSON || full.AffectedRows != -1 || full.Steps[0].State != "completed" {
		t.Fatal("common projection lost typed path or receipt", e)
	}
	full.Graph.Rows[0][0].Path.Nodes[0].ElementID = "modified"
	if l.(*queryLease).client.LastResponse().Rows[0][0].Path.Nodes[0].ElementID == "modified" {
		t.Fatal("typed response aliases original")
	}
	bounded, e := l.ExecuteGraph(context.Background(), Request{Query: "UNWIND range(1,20) AS i CREATE (n:NyaFoundation {marker:$marker}) RETURN i", Parameters: map[string]any{"marker": marker}, MaxRows: 2, FetchSize: 2})
	if e != nil || !bounded.Completed || !bounded.Truncated || bounded.Uncertain || len(bounded.Rows) != 2 || bounded.Summary.Counters["nodes_created"] != 20 {
		t.Fatal("retention stopped native completion", e)
	}
	invalid, e := l.Execute(context.Background(), "neo4j", "Nya invalid arbitrary Cypher")
	if !errors.Is(e, contract.ErrSQLSessionLost) || invalid.Graph == nil || !invalid.Graph.Uncertain || invalid.Graph.FailureCode != "Neo.ClientError.Statement.SyntaxError" || invalid.Steps[0].State != "uncertain" {
		t.Fatal("native missing completion stayed replayable", e)
	}
	if _, e = l.Execute(context.Background(), "neo4j", "RETURN 1"); !errors.Is(e, contract.ErrSQLSessionLost) {
		t.Fatal("old executor reused", e)
	}
	next, e := l.Replace()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = next.Execute(context.Background(), "neo4j", "RETURN 1"); e != nil {
		t.Fatal("explicit original lease recovery failed", e)
	}
	l.Release()
	if _, e = independent.Execute(context.Background(), "neo4j", "RETURN 1"); e != nil {
		t.Fatal("old executor affected independent lease", e)
	}
}
func TestNeo4jNativeSessionLostCanceledAndCloseNeverReplay(t *testing.T) {
	f := sessionFixtureTest(t)
	admin := nativeOpenTest(t, f)
	for _, action := range []string{"lost", "cancel", "release", "session-close"} {
		t.Run(action, func(t *testing.T) {
			marker := markerTest(f, "session_fault_"+action)
			t.Cleanup(func() { cleanupMarkerTest(t, admin, marker) })
			needle := "NyaSessionFault_" + marker
			relay := newNativeRelay(t, f, needle)
			original := f
			original.Config.URI = "bolt://" + relay.listener.Addr().String()
			original.Config.CAFile = ""
			original.Config.ClientCertFile = ""
			original.Config.ClientKeyFile = ""
			s := nativeSessionTest(t, original)
			l := nativeGraphLeaseTest(t, s, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				r contract.SQLResult
				e error
			}
			done := make(chan completion, 1)
			go func() {
				r, e := l.Execute(ctx, "neo4j", "/*"+needle+"*/ CREATE (:NyaFoundation {marker:'"+marker+"'})")
				done <- completion{r, e}
			}()
			select {
			case <-relay.observed:
			case <-time.After(10 * time.Second):
				t.Fatal("native original lease write not observed")
			}
			committed := false
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				r := nativeRunTest(t, admin, Request{Query: "MATCH (n:NyaFoundation {marker:$marker}) RETURN count(n) AS n", Parameters: map[string]any{"marker": marker}})
				if columnTest(t, r, 0, "n").Text == "1" {
					committed = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !committed {
				cancel()
				relay.Close()
				<-done
				t.Fatal("original native write not independently committed")
			}
			closed := make(chan struct{})
			switch action {
			case "lost":
				relay.Close()
			case "cancel":
				cancel()
			case "release":
				go func() { l.Release(); close(closed) }()
			case "session-close":
				go func() { _ = s.Close(); close(closed) }()
			}
			var result completion
			select {
			case result = <-done:
			case <-time.After(15 * time.Second):
				relay.Close()
				t.Fatal("original native lease did not acknowledge interruption")
			}
			if action == "release" || action == "session-close" {
				select {
				case <-closed:
				case <-time.After(10 * time.Second):
					t.Fatal("close did not wait for executor")
				}
			}
			if !errors.Is(result.e, contract.ErrSQLSessionLost) || result.r.Graph == nil || !result.r.Graph.Uncertain || result.r.Graph.Completed || result.r.Steps[0].State != "uncertain" || relay.dispatches.Load() != 1 {
				t.Fatal("unsafe original lease receipt", result.e)
			}
			if _, e := l.Execute(context.Background(), "neo4j", "RETURN 1"); !errors.Is(e, contract.ErrSQLSessionLost) && !errors.Is(e, contract.ErrRevoked) {
				t.Fatal("old lease replayable", e)
			}
			replacement := nativeSessionTest(t, f)
			next := nativeGraphLeaseTest(t, replacement, 1)
			_ = s.Close()
			l.Release()
			r, e := next.ExecuteGraph(context.Background(), Request{Query: "MATCH (n:NyaFoundation {marker:$marker}) RETURN count(n) AS n", Mode: "read", Parameters: map[string]any{"marker": marker}})
			if e != nil || columnTest(t, r, 0, "n").Text != "1" || !replacement.Alive() {
				t.Fatal("cleanup/replay affected replacement", e)
			}
		})
	}
}
func TestNeo4jNativeSessionBlockedCancellationAcknowledgesOriginalExecutor(t *testing.T) {
	f := sessionFixtureTest(t)
	admin := nativeOpenTest(t, f)
	s := nativeSessionTest(t, f)
	l := nativeGraphLeaseTest(t, s, 1)
	marker := markerTest(f, "session_blocked")
	t.Cleanup(func() { cleanupMarkerTest(t, admin, marker) })
	nativeRunTest(t, admin, Request{Query: "CREATE (:NyaFoundation {marker:$marker,value:0})", Parameters: map[string]any{"marker": marker}})
	holder := nativeOpenTest(t, f)
	held := holder.driver.NewSession(context.Background(), nd.SessionConfig{DatabaseName: "neo4j"})
	defer held.Close(context.Background())
	tx, e := held.BeginTransaction(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Close(context.Background())
	locked, e := tx.Run(context.Background(), "MATCH (n:NyaFoundation {marker:$marker}) SET n.value=1", map[string]any{"marker": marker})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = locked.Consume(context.Background()); e != nil {
		t.Fatal(e)
	}
	query := "/*NyaOriginalSessionBlocked*/ MATCH (n:NyaFoundation {marker:$marker}) SET n.value=2 RETURN n.value"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type completion struct {
		r Response
		e error
	}
	done := make(chan completion, 1)
	go func() {
		r, e := l.ExecuteGraph(ctx, Request{Query: query, Parameters: map[string]any{"marker": marker}, TimeoutSeconds: 10})
		done <- completion{r, e}
	}()
	show := func() Response {
		return nativeRunTest(t, admin, Request{Query: "SHOW TRANSACTIONS YIELD currentQuery,transactionId,status WHERE currentQuery=$query RETURN transactionId,status", Database: "system", Mode: "read", Parameters: map[string]any{"query": query}})
	}
	blocked := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := show()
		if len(r.Rows) > 0 && strings.Contains(columnTest(t, r, 0, "status").Text, "Blocked") {
			blocked = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		cancel()
		<-done
		t.Fatal("original session request not observed blocked")
	}
	cancel()
	var result completion
	select {
	case result = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("blocked original cancellation did not acknowledge")
	}
	if !errors.Is(result.e, contract.ErrSQLSessionLost) || !result.r.Uncertain || result.r.Completed {
		t.Fatal("blocked lease cancellation unsafe", result.e)
	}
	deadline = time.Now().Add(5 * time.Second)
	settled := false
	for time.Now().Before(deadline) {
		if len(show().Rows) == 0 {
			settled = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !settled {
		t.Fatal("server transaction remained after acknowledgement")
	}
	if e = tx.Rollback(context.Background()); e != nil {
		t.Fatal(e)
	}
	next, e := l.Replace()
	if e != nil {
		t.Fatal(e)
	}
	r, e := next.(contract.GraphQueryLease).ExecuteGraph(context.Background(), Request{Query: "MATCH (n:NyaFoundation {marker:$marker}) RETURN n.value AS value", Parameters: map[string]any{"marker": marker}})
	if e != nil || columnTest(t, r, 0, "value").Text != "0" {
		t.Fatal("blocked original write replayed", e)
	}
}
