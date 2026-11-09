package neo4j

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNeo4jNativePropertyListsStorageTypesAndNativeComparison(t *testing.T) {
	f := fixtureTest(t)
	admin := nativeOpenTest(t, f)
	marker := markerTest(f, "list_type_proof")
	t.Cleanup(func() { cleanupMarkerTest(t, admin, marker) })
	created := nativeRunTest(t, admin, Request{Query: "CREATE (a:NyaFoundation {marker:$marker})-[r:NYA_LIST_PROOF]->(b:NyaFoundation {marker:$marker}) RETURN a,r", Parameters: map[string]any{"marker": marker}})
	for _, target := range []struct{ pattern, id string }{{"(e)", created.Rows[0][0].Node.ElementID}, {"()-[e]->()", created.Rows[0][1].Relationship.ElementID}} {
		for _, sample := range []struct {
			name, storedType string
			value, stored    any
		}{{"empty", "LIST<NOTHING> NOT NULL", []any{}, []any{}}, {"integers", "LIST<INTEGER NOT NULL> NOT NULL", []int64{math.MinInt64, math.MaxInt64}, []int64{math.MinInt64, math.MaxInt64}}, {"floats", "LIST<FLOAT NOT NULL> NOT NULL", []float64{math.Copysign(0, -1), 1e-12}, []float64{math.Copysign(0, -1), 1e-12}}, {"strings", "LIST<STRING NOT NULL> NOT NULL", []string{"", "猫 ' ` \\"}, []string{"", "猫 ' ` \\"}}, {"booleans", "LIST<BOOLEAN NOT NULL> NOT NULL", []bool{false, true}, []bool{false, true}}, {"mixed_numeric", "LIST<FLOAT NOT NULL> NOT NULL", []any{int64(1), float64(2.5)}, []float64{1, 2.5}}} {
			r := nativeRunTest(t, admin, Request{Query: "MATCH " + target.pattern + " WHERE elementId(e)=$id SET e[$key]=$value RETURN valueType($value),valueType(e[$key]),e[$key],e[$key]=$value", Parameters: map[string]any{"id": target.id, "key": sample.name, "value": sample.value}})
			budget := valueBudget{limit: 1 << 20}
			want, _, err := budget.capture(sample.stored, true, 0)
			if err != nil || len(r.Rows) != 1 || r.Rows[0][1].Text != sample.storedType || !reflect.DeepEqual(r.Rows[0][2], want) || r.Rows[0][3].Text != "true" {
				t.Fatal("native list storage/type/comparison proof failed", sample.name, err)
			}
			if sample.name == "mixed_numeric" {
				if r.Rows[0][0].Text == sample.storedType {
					t.Fatal("mixed numeric input coercion was hidden")
				}
			} else if r.Rows[0][0].Text != sample.storedType {
				t.Fatal("homogeneous list input and storage types differed", sample.name)
			}
			raw, _ := json.Marshal(r.Rows[0])
			t.Log(target.pattern, sample.name, string(raw))
		}
		for _, sample := range []struct {
			name  string
			value any
		}{{"heterogeneous", []any{"x", int64(1)}}, {"null_element", []any{"x", nil}}, {"nested", []any{[]any{"x"}}}} {
			client := nativeOpenTest(t, f)
			plan, err := client.Prepare(Request{Query: "MATCH " + target.pattern + " WHERE elementId(e)=$id SET e[$key]=$value RETURN e[$key]", Parameters: map[string]any{"id": target.id, "key": sample.name, "value": sample.value}})
			if err != nil {
				t.Fatal(err)
			}
			r, err := client.Execute(context.Background(), plan)
			if err == nil || r.FailureCode != "Neo.ClientError.Statement.TypeError" {
				t.Fatal("server unexpectedly stored an unsupported property list", sample.name)
			}
			read := nativeRunTest(t, admin, Request{Query: "MATCH " + target.pattern + " WHERE elementId(e)=$id RETURN e[$key]", Mode: "read", Parameters: map[string]any{"id": target.id, "key": sample.name}})
			if read.Rows[0][0].Kind != "null" {
				t.Fatal("native type rejection retained an unsupported property")
			}
			t.Log(target.pattern, sample.name, r.FailureCode)
		}
	}
	r := nativeRunTest(t, admin, Request{Query: "RETURN $integers=$floats,valueType($integers),valueType($floats)", Parameters: map[string]any{"integers": []int64{1}, "floats": []float64{1}}})
	if r.Rows[0][0].Text != "true" || r.Rows[0][1].Text != "LIST<INTEGER NOT NULL> NOT NULL" || r.Rows[0][2].Text != "LIST<FLOAT NOT NULL> NOT NULL" {
		t.Fatal("native list numeric equality was confused with identical types")
	}
	raw, _ := json.Marshal(r.Rows[0])
	t.Log("numeric comparison", string(raw))
}

func TestNeo4jNativePropertyListsTypedLocksReceiptsAndEmptyPresence(t *testing.T) {
	f := sessionFixtureTest(t)
	admin := nativeOpenTest(t, f)
	s := nativeSessionTest(t, f)
	lease := nativeGraphLeaseTest(t, s, 23).(contract.GraphPropertyWriteLease)
	marker := markerTest(f, "list_write")
	t.Cleanup(func() { cleanupMarkerTest(t, admin, marker) })
	created := nativeRunTest(t, admin, Request{Query: "CREATE (a:NyaFoundation {marker:$marker,value:[0],guard:0})-[r:NYA_LIST_WRITE {value:[0],guard:0}]->(b:NyaFoundation {marker:$marker}) RETURN a,r", Parameters: map[string]any{"marker": marker}})
	node, edge := created.Rows[0][0].Node, created.Rows[0][1].Relationship
	targets := []contract.GraphPropertyTarget{{Kind: "node", ElementID: node.ElementID, Labels: node.Labels}, {Kind: "relationship", ElementID: edge.ElementID, SourceID: edge.StartElementID, TargetID: edge.EndElementID, RelationshipType: edge.Type}}
	for _, target := range targets {
		t.Run(target.Kind, func(t *testing.T) {
			pattern := "(e)"
			if target.Kind == "relationship" {
				pattern = "()-[e]->()"
			}
			r := contract.GraphPropertyWriteRequest{Namespace: s.Namespace("neo4j"), Target: target, Property: "value", Before: propertyListTest(t, "integer", "[0]"), After: propertyListTest(t, "integer", "[2]")}
			holder := nativeOpenTest(t, f).driver.NewSession(context.Background(), nd.SessionConfig{DatabaseName: "neo4j"})
			defer holder.Close(context.Background())
			tx, err := holder.BeginTransaction(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Close(context.Background())
			locked, err := tx.Run(context.Background(), "MATCH "+pattern+" WHERE elementId(e)=$id SET e.value=[1],e.guard=1", map[string]any{"id": target.ElementID})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = locked.Consume(context.Background()); err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				receipt contract.GraphPropertyWriteReceipt
				err     error
			}
			done := make(chan outcome, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			go func() { receipt, err := lease.WriteGraphProperty(ctx, r); done <- outcome{receipt, err} }()
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
				t.Fatal("typed list comparison did not wait for the original entity lock")
			}
			if err = tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			result := <-done
			if result.err != nil || !result.receipt.Conflict || result.receipt.Applied || !result.receipt.Result.Completed {
				t.Fatal("a stale list comparison overwrote a concurrent list", result.err)
			}
			r.Before = propertyListTest(t, "integer", "[1]")
			receipt, err := lease.WriteGraphProperty(context.Background(), r)
			if err != nil || !receipt.Applied || !reflect.DeepEqual(receipt.Result.Rows[0][2], r.After) {
				t.Fatal("fresh native typed list comparison failed", err)
			}
			r.Before, r.After = propertyListTest(t, "float", "[2]"), propertyListTest(t, "float", "[3]")
			receipt, err = lease.WriteGraphProperty(context.Background(), r)
			if err != nil || !receipt.Conflict || receipt.Applied {
				t.Fatal("equal float/integer lists bypassed the native type comparison", err)
			}
			for _, sample := range []struct{ kind, text string }{{"integer", "[-9223372036854775808,9223372036854775807]"}, {"float", "[-0,1e-12]"}, {"boolean", "[false,true]"}, {"string", `["","猫 ' ` + "`" + ` \\"]`}} {
				r.Property = "list ` 猫 " + sample.kind
				r.Before, r.After = contract.GraphValue{Kind: "null"}, propertyListTest(t, sample.kind, sample.text)
				receipt, err = lease.WriteGraphProperty(context.Background(), r)
				if err != nil || !receipt.Applied || !reflect.DeepEqual(receipt.Result.Rows[0][2], r.After) || !propertyReceiptEntity(receipt.Result.Rows[0][3], target, r.Property, receipt.Result.Rows[0][2]) {
					t.Fatal("native typed list addition lost values/type/full entity", sample.kind, err)
				}
				duplicate, err := lease.WriteGraphProperty(context.Background(), r)
				if err != nil || !duplicate.Conflict || duplicate.Applied {
					t.Fatal("list addition overwrote an existing property", err)
				}
				r.Before, r.After = receipt.Result.Rows[0][2].Clone(), contract.GraphValue{Kind: "list"}
				receipt, err = lease.WriteGraphProperty(context.Background(), r)
				if err != nil || !receipt.Applied || receipt.Result.Rows[0][1].Text != "LIST<NOTHING> NOT NULL" || receipt.Result.Rows[0][2].Kind != "list" || len(receipt.Result.Rows[0][2].Values) != 0 {
					t.Fatal("list clearing fabricated absence instead of a present empty list", err)
				}
				r.Before, r.After = receipt.Result.Rows[0][2].Clone(), contract.GraphValue{Kind: "null"}
				receipt, err = lease.WriteGraphProperty(context.Background(), r)
				if err != nil || !receipt.Applied || !propertyReceiptEntity(receipt.Result.Rows[0][3], target, r.Property, r.After) {
					t.Fatal("empty list removal lacked native absence confirmation", err)
				}
			}
			r.Property, r.Before, r.After = "empty ` 猫", contract.GraphValue{Kind: "null"}, contract.GraphValue{Kind: "list"}
			receipt, err = lease.WriteGraphProperty(context.Background(), r)
			if err != nil || !receipt.Applied || receipt.Result.Rows[0][2].Kind != "list" {
				t.Fatal("empty list addition confused absence", err)
			}
			r.Before, r.After = receipt.Result.Rows[0][2].Clone(), propertyListTest(t, "boolean", "[false]")
			receipt, err = lease.WriteGraphProperty(context.Background(), r)
			if err != nil || !receipt.Applied || receipt.Result.Rows[0][2].Values[0].Kind != "boolean" {
				t.Fatal("empty list could not receive its explicitly selected element kind", err)
			}
			read := nativeRunTest(t, admin, Request{Query: "MATCH " + pattern + " WHERE elementId(e)=$id RETURN e.value,e.guard", Mode: "read", Parameters: map[string]any{"id": target.ElementID}})
			if read.Rows[0][0].Values[0].Text != "2" || read.Rows[0][1].Text != "1" {
				t.Fatal("list writes changed unrelated properties or replayed a stale comparison")
			}
		})
	}
}
