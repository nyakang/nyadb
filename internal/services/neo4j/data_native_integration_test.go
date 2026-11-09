package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func dataFixtureTest(t *testing.T) nativeFixture {
	t.Helper()
	if os.Getenv("NYADB_NEO4J_DATA_IT") != "1" {
		t.Skip("requires owned original graph data fixture")
	}
	return sessionFixtureTest(t)
}
func cleanupLabelTest(t *testing.T, c *Client, label string) {
	t.Helper()
	t.Cleanup(func() { nativeRunTest(t, c, Request{Query: "MATCH (n:" + graphQuote(label) + ") DETACH DELETE n"}) })
}
func TestNeo4jNativeDataQualifiedNodesRelationshipsAndCursor(t *testing.T) {
	f := dataFixtureTest(t)
	admin := nativeOpenTest(t, f)
	s := nativeSessionTest(t, f)
	ctx := context.Background()
	label := "NyaData_" + f.Nonce + "_猫`"
	rel := "NyaRelData_" + f.Nonce + "_猫`"
	cleanupLabelTest(t, admin, label)
	nativeRunTest(t, admin, Request{Query: "UNWIND range(0,1204) AS i CREATE (n:" + graphQuote(label) + " {i:i,exact:$exact}) SET n.optional=CASE i % 2 WHEN 0 THEN null ELSE '' END RETURN count(n) AS count", Parameters: map[string]any{"exact": int64(math.MaxInt64)}})
	nativeRunTest(t, admin, Request{Query: "MATCH (a:" + graphQuote(label) + " {i:0}),(b:" + graphQuote(label) + " {i:1}) CREATE (a)-[r:" + graphQuote(rel) + " {values:[$exact,1],weight:1.5}]->(b) RETURN r", Parameters: map[string]any{"exact": int64(math.MaxInt64)}})
	l := dataLeaseTest(t, s, "label", label)
	r := l.Runtime()
	count, e := r.Count(ctx, 42, "neo4j", label)
	if e != nil || count != 1205 {
		t.Fatal("original graph count", e)
	}
	first, e := r.AdvancedLoader(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 1000, Object: l.object})
	if e != nil || len(first.Rows) != 1000 || !first.HasMore || !first.LiveRead || first.PagingMode != "cursor" || first.Editable == nil || *first.Editable {
		t.Fatal("first native graph page", e)
	}
	second, e := r.AdvancedLoader(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 1000, Offset: 1000, After: first.NextCursor})
	if e != nil || len(second.Rows) != 205 || second.HasMore {
		t.Fatal("native cursor page", e)
	}
	seen := map[string]bool{}
	for _, p := range []*contract.DataPage{first, second} {
		for _, row := range p.Rows {
			if seen[row[0].Text] {
				t.Fatal("native page duplicated entity")
			}
			seen[row[0].Text] = true
		}
	}
	back, e := r.AdvancedLoader(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 1000, After: first.CurrentCursor})
	if e != nil || back.Rows[0][0].Text != first.Rows[0][0].Text {
		t.Fatal("original back boundary", e)
	}
	other := dataLeaseTest(t, s, "label", label)
	if _, e = other.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 1000, Offset: 1000, After: first.NextCursor}); e == nil {
		t.Fatal("native cursor crossed document")
	}
	forged := cloneDataOptions(contract.DataOptions{Limit: 1000, Offset: 1000, After: first.NextCursor})
	forged.After.Values[0][0] ^= 1
	if _, e = l.page(ctx, 42, "neo4j", label, forged); e == nil {
		t.Fatal("native forged cursor dispatched")
	}
	if _, e = l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 1000}); e != nil {
		t.Fatal(e)
	}
	if _, e = l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 1000, Offset: 1000, After: first.NextCursor}); e == nil {
		t.Fatal("old native epoch reused")
	}
	var jsonl strings.Builder
	if e = r.JSONL(ctx, l.object, &jsonl); e != nil {
		t.Fatal(e)
	}
	lines := strings.Split(strings.TrimSuffix(jsonl.String(), "\n"), "\n")
	if len(lines) != 1205 {
		t.Fatal("native complete enumeration truncated at query/grid limits")
	}
	for _, line := range lines {
		var v Value
		if json.Unmarshal([]byte(line), &v) != nil || v.Node == nil || propertyTest(t, v.Node.Properties, "exact").Text != "9223372036854775807" {
			t.Fatal("native JSONL lost typed integer/entity")
		}
	}
	links := dataLeaseTest(t, s, "relationship", rel)
	p, e := links.page(ctx, 42, "neo4j", rel, contract.DataOptions{Limit: 100})
	if e != nil || len(p.Rows) != 1 || p.Rows[0][1].Text != rel || p.Rows[0][2].Text == "" || p.Rows[0][3].Text == "" {
		t.Fatal("native relationship projection", e)
	}
	row, e := links.row(ctx, 42, "neo4j", rel, *p.RowRefs[0])
	if e != nil {
		t.Fatal(e)
	}
	var v Value
	if json.Unmarshal([]byte(row.Values["relationship"].Text), &v) != nil || v.Relationship.StartElementID != p.Rows[0][2].Text || v.Relationship.EndElementID != p.Rows[0][3].Text || propertyTest(t, v.Relationship.Properties, "values").Values[0].Text != "9223372036854775807" {
		t.Fatal("native relationship identity/types lost")
	}
	if _, e = links.row(ctx, 42, "neo4j", rel, *first.RowRefs[0]); e == nil {
		t.Fatal("node row crossed relationship document")
	}
	// Names containing literal Cypher Unicode escapes must remain parameters,
	// including for count, page and complete-row reads. Creating this fixture
	// uses Neo4j 5.26's native dynamic label/type syntax rather than interpolation.
	literal := "NyaLiteral_" + f.Nonce + "_\\u0060`_猫"
	nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label))-[r:$($type)]->(b:$($label)) RETURN a,r", Parameters: map[string]any{"label": literal, "type": literal}})
	t.Cleanup(func() {
		nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", Parameters: map[string]any{"label": literal}})
	})
	for _, kind := range []string{"label", "relationship"} {
		literalLease := dataLeaseTest(t, s, kind, literal)
		want := int64(2)
		if kind == "relationship" {
			want = 1
		}
		if n, e := literalLease.count(ctx, 42, "neo4j", literal); e != nil || n != want {
			t.Fatal("literal native graph name changed", e)
		}
		p, e := literalLease.page(ctx, 42, "neo4j", literal, contract.DataOptions{Limit: 100})
		if e != nil || int64(len(p.Rows)) != want {
			t.Fatal("literal native graph page", e)
		}
		if _, e = literalLease.row(ctx, 42, "neo4j", literal, *p.RowRefs[0]); e != nil {
			t.Fatal("literal native graph row", e)
		}
	}
}
func TestNeo4jNativeDataOriginalFingerprintsFieldsAndExport(t *testing.T) {
	f := dataFixtureTest(t)
	admin := nativeOpenTest(t, f)
	s := nativeSessionTest(t, f)
	ctx := context.Background()
	label := "NyaDataField_" + f.Nonce
	cleanupLabelTest(t, admin, label)
	zone, e := time.LoadLocation("Europe/Stockholm")
	if e != nil {
		t.Fatal(e)
	}
	params := map[string]any{"exact": int64(math.MaxInt64), "minimum": int64(math.MinInt64), "bytes": []byte{0, 255}, "date": nd.Date(time.Now()), "datetime": time.Date(2026, 7, 3, 1, 2, 3, 123456789, zone), "point": nd.Point3D{X: 113.5, Y: 22.3, Z: 10.5, SpatialRefId: 4979}, "duration": nd.Duration{Months: 2, Days: 3, Seconds: 4, Nanos: 123456789}, "long": strings.Repeat("猫", 3000), "empty": ""}
	nativeRunTest(t, admin, Request{Query: "CREATE (n:" + graphQuote(label) + ") SET n=$properties RETURN n", Parameters: map[string]any{"properties": params}})
	l := dataLeaseTest(t, s, "label", label)
	p, e := l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 100})
	if e != nil || len(p.Rows) != 1 || !p.Rows[0][2].Truncated {
		t.Fatal("native field preview", e)
	}
	row, e := l.row(ctx, 42, "neo4j", label, *p.RowRefs[0])
	if e != nil {
		t.Fatal(e)
	}
	var entity Value
	if json.Unmarshal([]byte(row.Values["node"].Text), &entity) != nil || entity.Node.ElementID != p.Rows[0][0].Text {
		t.Fatal("native complete node")
	}
	for name, kind := range map[string]string{"exact": "integer", "minimum": "integer", "bytes": "bytes", "date": "date", "datetime": "datetime", "point": "point3d", "duration": "duration"} {
		if propertyTest(t, entity.Node.Properties, name).Kind != kind {
			t.Fatal("native property kind lost", name)
		}
	}
	if propertyTest(t, propertyTest(t, entity.Node.Properties, "datetime").Properties, "zone").Text != "Europe/Stockholm" || propertyTest(t, entity.Node.Properties, "empty").Text != "" {
		t.Fatal("native temporal/empty properties lost")
	}
	chunk, e := l.chunk(ctx, 42, "neo4j", label, "properties", *p.RowRefs[0], 20, 1024)
	if e != nil || len([]rune(chunk.Text)) != 1024 || !chunk.HasMore {
		t.Fatal("bounded original native field", e)
	}
	if e = l.exportRows(ctx, 42, "neo4j", label, contract.DataOptions{}, []string{"node"}, func(values []contract.DataValue) error {
		var v Value
		if json.Unmarshal([]byte(values[0].Text), &v) != nil || propertyTest(t, v.Node.Properties, "exact").Text != "9223372036854775807" {
			t.Error("tabular export flattened native value")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e = l.jsonl(ctx, l.object, shortGraphWriter{}); !errors.Is(e, io.ErrShortWrite) {
		t.Fatal("native short writer not rejected", e)
	}
	nativeRunTest(t, admin, Request{Query: "MATCH (n:" + graphQuote(label) + ") SET n.exact=7 RETURN n"})
	if _, e = l.row(ctx, 42, "neo4j", label, *p.RowRefs[0]); !errors.Is(e, contract.ErrDataConflict) {
		t.Fatal("changed native fingerprint replaced original", e)
	}
	nativeRunTest(t, admin, Request{Query: "MATCH (n:" + graphQuote(label) + ") SET n.long=reduce(s='x', i IN range(1,20) | s+s)+'oversize' RETURN elementId(n)"})
	large, e := l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 100})
	if e != nil {
		t.Fatal("native large graph preview", e)
	}
	if _, e = l.row(ctx, 42, "neo4j", label, *large.RowRefs[0]); !errors.Is(e, ErrBounds) {
		t.Fatal("native complete field limit ignored", e)
	}
	if _, e = l.chunk(ctx, 42, "neo4j", label, "properties", *large.RowRefs[0], 0, 1024); e != nil {
		t.Fatal("native explicit field chunk failed", e)
	}
	if e = l.jsonl(ctx, l.object, io.Discard); !errors.Is(e, ErrBounds) {
		t.Fatal("native oversized export accepted", e)
	}
}
func relayDataOpenTest(s *Session, f nativeFixture, relay *nativeRelay) {
	var opens atomic.Int32
	s.openClient = func(ctx context.Context, cfg connections.Neo4jConfig, secret string) (*Client, error) {
		if opens.Add(1) == 1 {
			cfg.URI = "bolt://" + relay.listener.Addr().String()
			cfg.CAFile = ""
			cfg.ClientCertFile = ""
			cfg.ClientKeyFile = ""
		}
		return Open(ctx, cfg, secret)
	}
}
func TestNeo4jNativeDataCancellationOriginalCloseAndNoReplay(t *testing.T) {
	f := dataFixtureTest(t)
	admin := nativeOpenTest(t, f)
	label := "NyaDataCancel_" + f.Nonce
	cleanupLabelTest(t, admin, label)
	nativeRunTest(t, admin, Request{Query: "CREATE (n:" + graphQuote(label) + " {exact:$exact})", Parameters: map[string]any{"exact": int64(math.MaxInt64)}})
	for _, action := range []string{"page_cancel", "page_lost", "count_cancel", "row_cancel", "document_close", "session_close"} {
		t.Run(action, func(t *testing.T) {
			s := nativeSessionTest(t, f)
			l := dataLeaseTest(t, s, "label", label)
			original, e := l.page(context.Background(), 42, "neo4j", label, contract.DataOptions{Limit: 100})
			if e != nil {
				t.Fatal(e)
			}
			relay := newNativeRelay(t, f, label)
			relayDataOpenTest(s, f, relay)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var e error
				switch action {
				case "count_cancel":
					_, e = l.count(ctx, 42, "neo4j", label)
				case "row_cancel":
					_, e = l.row(ctx, 42, "neo4j", label, *original.RowRefs[0])
				default:
					_, e = l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 100})
				}
				done <- e
			}()
			select {
			case <-relay.observed:
			case <-time.After(10 * time.Second):
				t.Fatal("native graph data RUN not observed")
			}
			select {
			case <-done:
				t.Fatal("withheld read acknowledged")
			default:
			}
			// Other original readers are independently connected and remain usable.
			other := dataLeaseTest(t, s, "label", label)
			n, e := other.count(context.Background(), 42, "neo4j", label)
			if e != nil || n != 1 {
				t.Fatal("independent original data reader failed", e)
			}
			closed := make(chan struct{})
			switch action {
			case "document_close":
				go func() { _ = l.Close(); close(closed) }()
			case "session_close":
				go func() { _ = s.Close(); close(closed) }()
			case "page_lost":
				relay.Close()
			default:
				cancel()
			}
			select {
			case e = <-done:
			case <-time.After(15 * time.Second):
				relay.Close()
				t.Fatal("native graph read did not acknowledge cancellation/close")
			}
			if e == nil || relay.dispatches.Load() != 1 {
				t.Fatal("native data request acknowledged or replayed", e)
			}
			if action == "document_close" || action == "session_close" {
				select {
				case <-closed:
				case <-time.After(10 * time.Second):
					t.Fatal("original close did not settle")
				}
			}
			if action == "count_cancel" || action == "row_cancel" {
				if _, e = l.page(context.Background(), 42, "neo4j", label, contract.DataOptions{Limit: 100}); e != nil {
					t.Fatal("auxiliary cancel retired original page range", e)
				}
			} else {
				if _, e = l.page(context.Background(), 42, "neo4j", label, contract.DataOptions{Limit: 100}); e == nil {
					t.Fatal("retired data range automatically recovered")
				}
			}
			freshSession := nativeSessionTest(t, f)
			fresh := dataLeaseTest(t, freshSession, "label", label)
			if n, e = fresh.count(context.Background(), 42, "neo4j", label); e != nil || n != 1 {
				t.Fatal("explicit new session failed", e)
			}
			_ = l.Close()
			if n, e = fresh.count(context.Background(), 42, "neo4j", label); e != nil || n != 1 {
				t.Fatal("old document close reached replacement", e)
			}
		})
	}
}
