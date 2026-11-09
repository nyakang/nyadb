package neo4j

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func schemaObjectTest(s *Session) contract.ObjectRef {
	return s.Namespace("neo4j").Object("graph_schema", "graph_schema")
}
func emptySchemaResultTest(query string) *fakeResult {
	columns := strings.Fields("id name state populationPercent type entityType labelsOrTypes properties indexProvider owningConstraint lastRead readCount trackedSince options failureMessage createStatement")
	if strings.HasPrefix(query, "SHOW CONSTRAINTS") {
		columns = strings.Fields("id name type entityType labelsOrTypes properties ownedIndex propertyType options createStatement")
	}
	r := &fakeResult{keys: columns, summary: fakeSummary{agent: "Neo4j/5.26.21"}}
	if query == databaseProbe("neo4j") {
		r.keys = []string{"name"}
		r.rows = []*nd.Record{{Values: []any{"neo4j"}}}
	}
	return r
}
func schemaClientTest(t *testing.T) (*Client, *fakeDriver) {
	c, d := fakeClient(t, normalResult())
	d.session.run = func(_ context.Context, q string, _ map[string]any, _ ...func(*nd.TransactionConfig)) (nd.Result, error) {
		if q != databaseProbe("neo4j") && q != "SHOW INDEXES YIELD * RETURN *" && q != "SHOW CONSTRAINTS YIELD * RETURN *" {
			t.Error("preparation submitted a native write or concise SHOW", q)
		}
		return emptySchemaResultTest(q), nil
	}
	return c, d
}
func schemaDraftTest() contract.GraphSchemaDraft {
	return contract.GraphSchemaDraft{Action: "create_index", Name: "owned", Type: "RANGE", EntityType: "NODE", LabelsOrTypes: []string{"Person"}, Properties: []string{"name"}}
}

func TestSchemaPreparationOriginalObservationRefreshCopiesAndPureReservation(t *testing.T) {
	s := fakeSessionTest(t)
	c, d := schemaClientTest(t)
	var opens atomic.Int32
	s.openClient = func(context.Context, connections.Neo4jConfig, string) (*Client, error) { opens.Add(1); return c, nil }
	o := schemaObjectTest(s)
	reserved, e := s.ReserveGraphSchemaPreparation(o, 1)
	if e != nil || opens.Load() != 0 {
		t.Fatal("reservation performed I/O", e)
	}
	l := reserved.(*schemaPreparationLease)
	before, e := l.Read(context.Background())
	if e != nil || opens.Load() != 1 || d.session.runs.Load() != 4 {
		t.Fatal("missing complete original reads", e)
	}
	copy := before.Snapshot()
	copy.Indexes.Columns[0] = "changed"
	if before.Snapshot().Indexes.Columns[0] != "id" {
		t.Fatal("mutable baseline")
	}
	draft := schemaDraftTest()
	plan, e := l.Prepare(before, draft)
	if e != nil {
		t.Fatal(e)
	}
	draft.Properties[0] = "other"
	preview := plan.Preview()
	preview.Draft.Properties[0] = "changed"
	if plan.Preview().Draft.Properties[0] != "name" || d.session.runs.Load() != 4 {
		t.Fatal("preparation aliases inputs or performs I/O")
	}
	other, e := s.ReserveGraphSchemaPreparation(o, 2)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = other.Prepare(before, schemaDraftTest()); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("foreign lease observation accepted", e)
	}
	if _, e = l.Prepare(&schemaObservation{owner: l, snapshot: before.Snapshot()}, schemaDraftTest()); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("forged observation accepted", e)
	}
	next, e := l.Read(context.Background())
	if e != nil || next.Snapshot().Sequence != 2 {
		t.Fatal(e)
	}
	if _, e = l.Prepare(before, schemaDraftTest()); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("stale epoch accepted", e)
	}
	if e = l.Close(); e != nil {
		t.Fatal(e)
	}
	if e = l.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = l.Prepare(next, schemaDraftTest()); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal(e)
	}
	if before.Snapshot().Indexes.Columns[0] != "id" || plan.Preview().Draft.Properties[0] != "name" || d.closes.Load() != 1 {
		t.Fatal("close destroyed caller-owned evidence or closed driver twice")
	}
}

func TestSchemaPreparationLimitsUnsupportedVersionsAndFailedReadRevokesBaseline(t *testing.T) {
	s := fakeSessionTest(t)
	c, d := schemaClientTest(t)
	s.openClient = func(context.Context, connections.Neo4jConfig, string) (*Client, error) { return c, nil }
	o := schemaObjectTest(s)
	for _, edit := range []func(*contract.ObjectRef){func(o *contract.ObjectRef) { o.ProfileID++ }, func(o *contract.ObjectRef) { o.Catalog = "system" }, func(o *contract.ObjectRef) { o.Schema = "foreign" }, func(o *contract.ObjectRef) { o.Name = "other" }} {
		bad := o
		edit(&bad)
		if s.SupportsGraphSchemaPreparation(bad) {
			t.Fatal("unqualified scope supported")
		}
	}
	for i := 1; i <= 8; i++ {
		if _, e := s.ReserveGraphSchemaPreparation(o, uint64(i)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.ReserveGraphSchemaPreparation(o, 9); !errors.Is(e, ErrBusy) {
		t.Fatal(e)
	}
	var l *schemaPreparationLease
	for lease := range s.schemaLeases {
		if lease.document == 1 {
			l = lease
		}
	}
	before, e := l.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	d.session.run = func(context.Context, string, map[string]any, ...func(*nd.TransactionConfig)) (nd.Result, error) {
		return nil, &nd.Neo4jError{Code: "Neo.ClientError.Security.Forbidden"}
	}
	if _, e = l.Read(context.Background()); e == nil {
		t.Fatal("failed native SHOW accepted")
	}
	if _, e = l.Prepare(before, schemaDraftTest()); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("failed refresh retained authorization", e)
	}
	if !c.Alive() {
		t.Fatal("definite denied read retired healthy client")
	}
	s.identity.Agent = "Neo4j/4.4.0"
	if s.SupportsGraphSchemaPreparation(o) {
		t.Fatal("unverified version gained DDL capability")
	}
	if e := s.Close(); e != nil || len(s.schemaLeases) != 0 || s.secret != "" {
		t.Fatal("original session retained schema clients/secrets", e)
	}
}

func TestSchemaPreparationCloseCancelsOriginalHandshakeAndClosesOnlyLateCapturedClient(t *testing.T) {
	s := fakeSessionTest(t)
	c, d := schemaClientTest(t)
	started, release := make(chan struct{}), make(chan struct{})
	s.openClient = func(context.Context, connections.Neo4jConfig, string) (*Client, error) {
		close(started)
		<-release
		return c, nil
	}
	l, e := s.ReserveGraphSchemaPreparation(schemaObjectTest(s), 1)
	if e != nil {
		t.Fatal(e)
	}
	read := make(chan error, 1)
	go func() { _, e := l.Read(context.Background()); read <- e }()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- l.Close() }()
	// Close publishes cancellation before waiting for the original opener.
	original := l.(*schemaPreparationLease)
	select {
	case <-original.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("close did not cancel original handshake")
	}
	close(release)
	if e := <-read; e == nil {
		t.Fatal("late canceled handshake published")
	}
	if e := <-closed; e != nil {
		t.Fatal(e)
	}
	if d.session.runs.Load() != 0 || d.closes.Load() != 1 || !s.Alive() {
		t.Fatal("late original client leaked, read or closed parent")
	}
}
