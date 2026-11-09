package neo4j

import (
	"context"
	"strings"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNeo4jNativePropertyWritesTypedLocksConflictsAndNativeReceipts(t *testing.T) {
	f := sessionFixtureTest(t)
	admin := nativeOpenTest(t, f)
	s := nativeSessionTest(t, f)
	l := nativeGraphLeaseTest(t, s, 7).(contract.GraphPropertyWriteLease)
	marker := markerTest(f, "property_write")
	t.Cleanup(func() { cleanupMarkerTest(t, admin, marker) })
	created := nativeRunTest(t, admin, Request{Query: "CREATE (a:NyaFoundation {marker:$marker,value:0,exact:$exact,name:'old',flag:true,weight:1.5})-[r:NYA_PROPERTY {value:0}]->(b:NyaFoundation {marker:$marker}) RETURN a,r,b", Parameters: map[string]any{"marker": marker, "exact": int64(9223372036854775807)}})
	node := columnTest(t, created, 0, "a").Node
	rel := columnTest(t, created, 0, "r").Relationship
	targets := []contract.GraphPropertyTarget{{Kind: "node", ElementID: node.ElementID, Labels: node.Labels}, {Kind: "relationship", ElementID: rel.ElementID, SourceID: rel.StartElementID, TargetID: rel.EndElementID, RelationshipType: rel.Type}}
	for _, target := range targets {
		t.Run(target.Kind, func(t *testing.T) {
			r := contract.GraphPropertyWriteRequest{Namespace: s.Namespace("neo4j"), Target: target, Property: "value", Before: contract.GraphValue{Kind: "integer", Text: "0"}, After: contract.GraphValue{Kind: "integer", Text: "2"}}
			holder := nativeOpenTest(t, f)
			held := holder.driver.NewSession(context.Background(), nd.SessionConfig{DatabaseName: "neo4j"})
			defer held.Close(context.Background())
			tx, e := held.BeginTransaction(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Close(context.Background())
			pattern := "(e)"
			if target.Kind == "relationship" {
				pattern = "()-[e]->()"
			}
			lock, e := tx.Run(context.Background(), "MATCH "+pattern+" WHERE elementId(e)=$id SET e.value=1", map[string]any{"id": target.ElementID})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = lock.Consume(context.Background()); e != nil {
				t.Fatal(e)
			}
			type outcome struct {
				r contract.GraphPropertyWriteReceipt
				e error
			}
			done := make(chan outcome, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			go func() { receipt, e := l.WriteGraphProperty(ctx, r); done <- outcome{receipt, e} }()
			query, _ := propertyWriteQuery(r)
			blocked := false
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				show := nativeRunTest(t, admin, Request{Query: "SHOW TRANSACTIONS YIELD currentQuery,status WHERE currentQuery=$query RETURN status", Database: "system", Mode: "read", Parameters: map[string]any{"query": query.Query}})
				if len(show.Rows) > 0 && strings.Contains(show.Rows[0][0].Text, "Blocked") {
					blocked = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !blocked {
				cancel()
				<-done
				t.Fatal("native conditional write did not lock before comparison")
			}
			if e = tx.Commit(context.Background()); e != nil {
				t.Fatal(e)
			}
			out := <-done
			if out.e != nil || !out.r.Conflict || out.r.Applied || !out.r.Result.Completed || out.r.Result.Uncertain || len(out.r.Result.Rows) != 0 {
				t.Fatal("concurrent committed change overwritten or missing native conflict receipt", out.e, out.r.Result.String())
			}
			r.Before.Text = "1"
			receipt, e := l.WriteGraphProperty(context.Background(), r)
			if e != nil || !receipt.Applied || receipt.Conflict || !receipt.Result.Completed || receipt.Result.Rows[0][2].Text != "2" {
				t.Fatal("explicit fresh typed comparison failed", e)
			}
			r.Before = contract.GraphValue{Kind: "float", Text: "2"}
			r.After = contract.GraphValue{Kind: "float", Text: "3"}
			receipt, e = l.WriteGraphProperty(context.Background(), r)
			if e != nil || !receipt.Conflict || receipt.Applied {
				t.Fatal("equal float/integer types admitted", e)
			}
		})
	}
	for _, property := range []struct{ name, kind, before, after string }{{"exact", "integer", "9223372036854775807", "-9223372036854775808"}, {"name", "string", "old", "new 猫 ' \\ `"}, {"flag", "boolean", "true", "false"}, {"weight", "float", "1.5", "2.75"}} {
		r := contract.GraphPropertyWriteRequest{Namespace: s.Namespace("neo4j"), Target: targets[0], Property: property.name, Before: contract.GraphValue{Kind: property.kind, Text: property.before}, After: contract.GraphValue{Kind: property.kind, Text: property.after}}
		receipt, e := l.WriteGraphProperty(context.Background(), r)
		if e != nil || !receipt.Applied || receipt.Result.Rows[0][2].Text != property.after {
			t.Fatal("typed scalar write lost value", property.name, e)
		}
	}
	r := contract.GraphPropertyWriteRequest{Namespace: s.Namespace("neo4j"), Target: targets[0], Property: "value", Before: contract.GraphValue{Kind: "integer", Text: "2"}, After: contract.GraphValue{Kind: "integer", Text: "5"}}
	r.Target.Labels = append(r.Target.Labels, "MissingLabel")
	if receipt, e := l.WriteGraphProperty(context.Background(), r); e != nil || !receipt.Conflict || receipt.Applied {
		t.Fatal("changed labels admitted", e)
	}
	r.Target = targets[1]
	r.Target.SourceID = targets[1].TargetID
	if receipt, e := l.WriteGraphProperty(context.Background(), r); e != nil || !receipt.Conflict || receipt.Applied {
		t.Fatal("foreign directed endpoint admitted", e)
	}
	read := nativeRunTest(t, admin, Request{Query: "MATCH (a:NyaFoundation {marker:$marker})-[r:NYA_PROPERTY]->() RETURN a,r", Parameters: map[string]any{"marker": marker}, Mode: "read"})
	if propertyTest(t, read.Rows[0][0].Node.Properties, "value").Text != "2" || propertyTest(t, read.Rows[0][1].Relationship.Properties, "value").Text != "2" {
		t.Fatal("conflict changed selected values")
	}
}
