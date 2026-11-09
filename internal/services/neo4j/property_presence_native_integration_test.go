package neo4j

import (
	"context"
	"strings"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNeo4jNativePropertyPresenceWritesAbsentLocksTypedAdditionAndRemoval(t *testing.T) {
	f := sessionFixtureTest(t)
	admin := nativeOpenTest(t, f)
	s := nativeSessionTest(t, f)
	lease := nativeGraphLeaseTest(t, s, 17).(contract.GraphPropertyWriteLease)
	marker := markerTest(f, "property_presence")
	t.Cleanup(func() { cleanupMarkerTest(t, admin, marker) })
	created := nativeRunTest(t, admin, Request{Query: "CREATE (a:NyaFoundation {marker:$marker,guard:0})-[r:NYA_PRESENCE {guard:0}]->(b:NyaFoundation {marker:$marker}) RETURN a,r", Parameters: map[string]any{"marker": marker}})
	node, edge := created.Rows[0][0].Node, created.Rows[0][1].Relationship
	targets := []contract.GraphPropertyTarget{{Kind: "node", ElementID: node.ElementID, Labels: node.Labels}, {Kind: "relationship", ElementID: edge.ElementID, SourceID: edge.StartElementID, TargetID: edge.EndElementID, RelationshipType: edge.Type}}
	null := contract.GraphValue{Kind: "null"}
	for _, target := range targets {
		t.Run(target.Kind, func(t *testing.T) {
			pattern := "(e)"
			if target.Kind == "relationship" {
				pattern = "()-[e]->()"
			}
			request := contract.GraphPropertyWriteRequest{Namespace: s.Namespace("neo4j"), Target: target, Property: "missing ` 猫", Before: null, After: contract.GraphValue{Kind: "string", Text: "Requested addition"}}
			// Resolve the global property-key token before the two transactions.
			// Concurrent token creation while holding an entity lock can deadlock
			// at the token lock, which does not test the absence predicate.
			nativeRunTest(t, admin, Request{Query: "MATCH " + pattern + " WHERE elementId(e)=$id SET e[$key]=1 WITH e SET e[$key]=null", Parameters: map[string]any{"id": target.ElementID, "key": request.Property}})
			// The last committed entity has no selected key. Hold an uncommitted
			// creation until the requesting query is blocked, then commit it. An
			// absence check against the old committed state must not overwrite it.
			holdingClient := nativeOpenTest(t, f)
			holder := holdingClient.driver.NewSession(context.Background(), nd.SessionConfig{DatabaseName: "neo4j"})
			defer holder.Close(context.Background())
			tx, err := holder.BeginTransaction(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Close(context.Background())
			locked, err := tx.Run(context.Background(), "MATCH "+pattern+" WHERE elementId(e)=$id SET e.guard=e.guard+1,e[$key]=$value", map[string]any{"id": target.ElementID, "key": request.Property, "value": "Concurrent creation"})
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
			go func() { r, e := lease.WriteGraphProperty(ctx, request); done <- outcome{r, e} }()
			query, err := propertyWriteQuery(request)
			if err != nil {
				t.Fatal(err)
			}
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
				t.Fatal("absent-property request did not wait for the original entity lock")
			}
			if err = tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			result := <-done
			if result.err != nil || !result.receipt.Conflict || result.receipt.Applied || !result.receipt.Result.Completed {
				t.Fatal("an absence check before locking overwrote concurrent creation", result.err, result.receipt.Result.String())
			}
			request.Before, request.After = contract.GraphValue{Kind: "string", Text: "Concurrent creation"}, null
			removed, err := lease.WriteGraphProperty(context.Background(), request)
			if err != nil || !removed.Applied || !removed.Result.Completed || removed.Result.Rows[0][2].Kind != "null" || removed.Result.Rows[0][1].Text != "NULL" || !propertyReceiptEntity(removed.Result.Rows[0][3], target, request.Property, null) {
				t.Fatal("native property removal did not return absence and the complete entity", err)
			}
			stale, err := lease.WriteGraphProperty(context.Background(), request)
			if err != nil || !stale.Conflict || stale.Applied {
				t.Fatal("removal from an already absent property was falsely confirmed", err)
			}
			for _, value := range []contract.GraphValue{{Kind: "string", Text: ""}, {Kind: "boolean", Text: "false"}, {Kind: "integer", Text: "-9223372036854775808"}, {Kind: "float", Text: "1e-12"}} {
				request.Before, request.After = null, value
				added, err := lease.WriteGraphProperty(context.Background(), request)
				if err != nil || !added.Applied || added.Result.Rows[0][2].Kind != value.Kind || !propertyReceiptEntity(added.Result.Rows[0][3], target, request.Property, added.Result.Rows[0][2]) {
					t.Fatal("typed creation confused absence with a present scalar", value.Kind, err)
				}
				// A present empty string/false/zero-like number cannot be added over.
				conflict, err := lease.WriteGraphProperty(context.Background(), request)
				if err != nil || !conflict.Conflict || conflict.Applied {
					t.Fatal("addition overwrote an already present scalar", value.Kind, err)
				}
				request.Before, request.After = added.Result.Rows[0][2].Clone(), null
				removed, err = lease.WriteGraphProperty(context.Background(), request)
				if err != nil || !removed.Applied || !propertyReceiptEntity(removed.Result.Rows[0][3], target, request.Property, null) {
					t.Fatal("typed removal failed", value.Kind, err)
				}
			}
			read := nativeRunTest(t, admin, Request{Query: "MATCH " + pattern + " WHERE elementId(e)=$id RETURN e[$key],e.guard", Mode: "read", Parameters: map[string]any{"id": target.ElementID, "key": request.Property}})
			if read.Rows[0][0].Kind != "null" || read.Rows[0][1].Text != "1" {
				t.Fatal("presence mutations changed another property or retained the removed key")
			}
			// Confirm an unseeded key with no competing token transaction as well.
			request.Property = marker + " fresh " + target.Kind
			request.Before, request.After = null, contract.GraphValue{Kind: "boolean", Text: "false"}
			added, err := lease.WriteGraphProperty(context.Background(), request)
			if err != nil || !added.Applied {
				t.Fatal("fresh native key-token creation failed", err)
			}
			request.Before, request.After = request.After, null
			removed, err = lease.WriteGraphProperty(context.Background(), request)
			if err != nil || !removed.Applied {
				t.Fatal("fresh native key removal failed", err)
			}
		})
	}
}
