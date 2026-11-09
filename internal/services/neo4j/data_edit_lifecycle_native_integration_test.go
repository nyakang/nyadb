package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNeo4jNativeDataWriteBlockedOriginalComparisonCancellationAndErrors(t *testing.T) {
	f := fixtureTest(t)
	admin := nativeOpenTest(t, f)
	s := nativeSessionTest(t, f)
	label, rel := "NyaSaveLock_"+f.Nonce, "NyaSaveLockRel_"+f.Nonce
	nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label) {guard:1,optional:'exists'}),(b:$($label) {guard:1}) CREATE (a)-[r:$($rel) {guard:1}]->(b)", Parameters: map[string]any{"label": label, "rel": rel}})
	t.Cleanup(func() {
		nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", Parameters: map[string]any{"label": label}})
	})
	ctx := context.Background()
	for _, kind := range []string{"label", "relationship"} {
		for _, fault := range []string{"concurrent", "cancel", "close"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				name, pattern := label, "(entity)"
				if kind == "relationship" {
					name, pattern = rel, "()-[entity]->()"
				}
				l := dataLeaseTest(t, s, kind, name)
				page, err := l.page(ctx, s.ProfileID(), "neo4j", name, contract.DataOptions{Limit: 10})
				if err != nil {
					t.Fatal(err)
				}
				// Choose a row without optional, while its sibling inventories that key.
				row := len(page.Rows) - 1
				fields := map[string]contract.DataValue{"property.guard": {Kind: contract.Integer, Text: "7"}}
				if kind == "label" {
					fields = map[string]contract.DataValue{"property.optional": {Kind: contract.Text, Text: "new absent value"}}
				}
				id := page.RowRefs[row].TypedValues[0].Text
				holder := nativeOpenTest(t, f).driver.NewSession(ctx, nd.SessionConfig{DatabaseName: "neo4j"})
				defer holder.Close(ctx)
				tx, err := holder.BeginTransaction(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Close(ctx)
				r, err := tx.Run(ctx, "MATCH "+pattern+" WHERE elementId(entity)=$id SET entity.guard=entity.guard+1 RETURN entity", map[string]any{"id": id})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = r.Consume(ctx); err != nil {
					t.Fatal(err)
				}
				child, cancel := context.WithCancel(ctx)
				defer cancel()
				type completion struct {
					out *contract.DataEditResult
					err error
				}
				done := make(chan completion, 1)
				go func() {
					out, e := l.Runtime().Saver(child, s.ProfileID(), "neo4j", name, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[row], Values: fields}})
					done <- completion{out, e}
				}()
				blocked := false
				deadline := time.Now().Add(10 * time.Second)
				query := l.match() + " AND elementId(entity)=$element_id SET entity[$lock_key]=entity[$lock_key] RETURN entity LIMIT 2"
				for time.Now().Before(deadline) {
					show := nativeRunTest(t, admin, Request{Query: "SHOW TRANSACTIONS YIELD currentQuery,status WHERE currentQuery=$query RETURN status", Database: "system", Mode: "read", Parameters: map[string]any{"query": query}})
					if len(show.Rows) > 0 && strings.Contains(show.Rows[0][0].Text, "Blocked") {
						blocked = true
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !blocked {
					cancel()
					<-done
					t.Fatal("actual saver did not acquire the original entity lock")
				}
				switch fault {
				case "concurrent":
					if err = tx.Commit(ctx); err != nil {
						t.Fatal(err)
					}
				case "cancel":
					cancel()
				case "close":
					if err = l.Close(); err != nil {
						t.Fatal(err)
					}
				}
				var got completion
				select {
				case got = <-done:
				case <-time.After(15 * time.Second):
					t.Fatal("original save did not acknowledge cancellation/close")
				}
				if got.err == nil || got.out == nil || got.out.CommitConfirmed {
					t.Fatal("blocked stale/canceled save admitted completion", got.err)
				}
				var receipt dataWriteReceipt
				if json.Unmarshal([]byte(got.out.NativeJSON), &receipt) != nil || receipt.CommitAttempted {
					t.Fatal("blocked save lost original native receipt")
				}
				if fault == "concurrent" {
					if !errors.Is(got.err, contract.ErrDataConflict) || !receipt.RollbackConfirmed || got.out.CommitUncertain {
						t.Fatal("post-lock full entity comparison failed", got.err)
					}
				} else {
					if !got.out.CommitUncertain || receipt.RollbackConfirmed {
						t.Fatal("driver no-op rollback treated as server acknowledgement")
					}
					if fault == "cancel" {
						if _, err = l.save(ctx, s.ProfileID(), "neo4j", name, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[row], Values: fields}}); !errors.Is(err, contract.ErrRevoked) {
							t.Fatal("uncertain writer accepted replay", err)
						}
					}
					if err = tx.Rollback(ctx); err != nil {
						t.Fatal(err)
					}
				}
				read := nativeRunTest(t, admin, Request{Query: "MATCH " + pattern + " WHERE elementId(entity)=$id RETURN entity.guard,entity.optional", Mode: "read", Parameters: map[string]any{"id": id}})
				if read.Rows[0][1].Kind != "null" {
					t.Fatal("blocked writer introduced absent property")
				}
				if fault == "concurrent" && read.Rows[0][0].Text == "7" {
					t.Fatal("conflicting write replaced concurrent value")
				}
			})
		}
	}

	errorLabel, constraint := label+"Error", "nya_grid_unique_"+f.Nonce
	nativeRunTest(t, admin, Request{Query: "CREATE (a:" + graphQuote(errorLabel) + " {guard:1}),(b:" + graphQuote(errorLabel) + " {guard:2})"})
	nativeRunTest(t, admin, Request{Query: "CREATE CONSTRAINT " + graphQuote(constraint) + " FOR (n:" + graphQuote(errorLabel) + ") REQUIRE n.guard IS UNIQUE"})
	t.Cleanup(func() {
		nativeRunTest(t, admin, Request{Query: "DROP CONSTRAINT " + graphQuote(constraint)})
		nativeRunTest(t, admin, Request{Query: "MATCH (n:" + graphQuote(errorLabel) + ") DETACH DELETE n"})
	})
	failedLease := dataLeaseTest(t, s, "label", errorLabel)
	page, e := failedLease.page(ctx, s.ProfileID(), "neo4j", errorLabel, contract.DataOptions{Limit: 10})
	if e != nil {
		t.Fatal(e)
	}
	row := 0
	for i, cells := range page.Rows {
		for _, cell := range cells {
			if cell.Value != nil && cell.Value.Kind == contract.Integer && cell.Value.Text == "1" {
				row = i
			}
		}
	}
	result, e := failedLease.save(ctx, s.ProfileID(), "neo4j", errorLabel, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[row], Values: map[string]contract.DataValue{"property.guard": {Kind: contract.Integer, Text: "2"}}}})
	var native *nd.Neo4jError
	if !errors.As(e, &native) || result == nil || result.CommitConfirmed || !result.CommitUncertain {
		t.Fatal("native constraint failure supplied a false commit/rollback acknowledgement", e)
	}
	var receipt dataWriteReceipt
	if json.Unmarshal([]byte(result.NativeJSON), &receipt) != nil || receipt.FailureCode == "" || receipt.RollbackConfirmed || receipt.CommitAttempted {
		t.Fatal("native server error lost its original receipt")
	}
	read := nativeRunTest(t, admin, Request{Query: "MATCH (n:" + graphQuote(errorLabel) + ") RETURN n.guard ORDER BY n.guard", Mode: "read"})
	if len(read.Rows) != 2 || read.Rows[0][0].Text != "1" || read.Rows[1][0].Text != "2" {
		t.Fatal("failed constraint write changed native values")
	}
	t.Log("Actual public saver node/relationship locks, absent lock key, original comparison, canceled/closed executor acknowledgement and no replay confirmed")
}
