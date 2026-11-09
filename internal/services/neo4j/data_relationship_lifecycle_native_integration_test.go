package neo4j

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNeo4jNativeDataRelationshipEndpointReadAndLockCancellationOriginalClose(t *testing.T) {
	f := dataFixtureTest(t)
	admin := nativeOpenTest(t, f)
	for _, operation := range []string{"read", "lock"} {
		for _, action := range []string{"cancel", "close", "session_close"} {
			t.Run(operation+"/"+action, func(t *testing.T) {
				label, rel := "NyaEndpointLife_"+f.Nonce+"_"+operation+"_"+action, "NyaEndpointLifeType_"+f.Nonce+"_"+operation+"_"+action
				cleanupLabelTest(t, admin, label)
				seed := nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2}) RETURN a,b", Parameters: map[string]any{"label": label}})
				first, second := seed.Rows[0][0].Node.ElementID, seed.Rows[0][1].Node.ElementID
				s := nativeSessionTest(t, f)
				l := dataLeaseTest(t, s, "relationship", rel)
				needle := relationshipEndpointQuery
				var plan contract.DataMutationPlan
				if operation == "lock" {
					e := relationshipEndpointsTest(t, l, first, second)
					var err error
					plan, err = l.previewEntityEdits(context.Background(), 42, "neo4j", rel, []contract.DataEdit{{Kind: "insert", GraphEndpoints: e}})
					if err != nil {
						t.Fatal(err)
					}
					defer plan.Close()
					blocker := nativeOpenTest(t, f)
					session := blocker.driver.NewSession(context.Background(), nd.SessionConfig{DatabaseName: "neo4j", AccessMode: nd.AccessModeWrite})
					t.Cleanup(func() { _ = session.Close(context.Background()) })
					tx, err := session.BeginTransaction(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = tx.Rollback(context.Background()); _ = tx.Close(context.Background()) })
					id := first
					if strings.Compare(second, first) < 0 {
						id = second
					}
					result, err := tx.Run(context.Background(), "MATCH (n) WHERE elementId(n)=$id SET n.guard=n.guard RETURN n", map[string]any{"id": id})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := result.Consume(context.Background()); err != nil {
						t.Fatal(err)
					}
					needle = "SET entity[$lock_key]=entity[$lock_key]"
				}
				relay := newNativeRelay(t, f, needle)
				relayDataOpenTest(s, f, relay)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type completion struct {
					endpoints *contract.DataGraphEndpoints
					result    *contract.DataEditResult
					err       error
				}
				done := make(chan completion, 1)
				go func() {
					if operation == "read" {
						e, err := l.readGraphEndpoints(ctx, 42, "neo4j", rel, first, second)
						done <- completion{endpoints: e, err: err}
					} else {
						r, err := l.submitEntityPlan(ctx, 42, "neo4j", rel, plan)
						done <- completion{result: r, err: err}
					}
				}()
				select {
				case <-relay.observed:
				case <-time.After(15 * time.Second):
					t.Fatal("original native endpoint operation did not dispatch")
				}
				select {
				case <-done:
					t.Fatal("withheld endpoint read or blocked native lock unexpectedly completed")
				default:
				}
				closed := make(chan struct{})
				switch action {
				case "cancel":
					cancel()
				case "close":
					go func() { _ = l.Close(); close(closed) }()
				case "session_close":
					go func() { _ = s.Close(); close(closed) }()
				}
				var got completion
				select {
				case got = <-done:
				case <-time.After(15 * time.Second):
					t.Fatal("original endpoint executor did not acknowledge cancellation/close")
				}
				if got.err == nil || got.endpoints != nil {
					t.Fatal("interrupted endpoint observation returned reusable authorization")
				}
				if operation == "lock" {
					if got.result == nil || got.result.CommitConfirmed || entityReceiptTest(t, got.result).CommitAttempted {
						t.Fatal("blocked endpoint lock admitted or confirmed COMMIT")
					}
					if _, err := l.submitEntityPlan(context.Background(), 42, "neo4j", rel, plan); !errors.Is(err, contract.ErrRevoked) {
						t.Fatal("canceled original relationship plan replayed", err)
					}
				}
				if action != "cancel" {
					select {
					case <-closed:
					case <-time.After(15 * time.Second):
						t.Fatal("original endpoint close did not wait for worker acknowledgement")
					}
				} else if operation == "read" {
					if _, err := l.count(context.Background(), 42, "neo4j", rel); err != nil {
						t.Fatal("canceled read retired independent document readers", err)
					}
				}
				if relay.dispatches.Load() != 1 {
					t.Fatal("interrupted endpoint request replayed")
				}
				read := nativeRunTest(t, admin, Request{Query: "MATCH ()-[r]->() WHERE type(r)=$rel RETURN count(r)", Parameters: map[string]any{"rel": rel}})
				if read.Rows[0][0].Text != "0" || !admin.Alive() {
					t.Fatal("endpoint interruption created an edge or closed another client")
				}
			})
		}
	}
}
