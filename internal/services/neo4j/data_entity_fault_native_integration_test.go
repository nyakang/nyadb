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

func TestNeo4jNativeDataEntityLostCanceledOriginalCloseNeverReplay(t *testing.T) {
	f := dataFixtureTest(t)
	admin := nativeOpenTest(t, f)
	for _, kind := range []string{"insert", "delete", "detach_delete", "relationship_delete"} {
		for _, fault := range []string{"lost", "cancel", "close", "session_close"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				label := "NyaEntityFault_" + f.Nonce + "_" + kind + "_" + fault
				cleanupLabelTest(t, admin, label)
				s := nativeSessionTest(t, f)
				objectKind, objectName := "label", label
				edit := contract.DataEdit{Kind: kind}
				identity := ""
				if kind == "insert" {
					edit.Values = map[string]contract.DataValue{"property.guard": {Kind: contract.Integer, Text: "1"}}
				} else {
					seed := nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2}) RETURN a,b", Parameters: map[string]any{"label": label}})
					identity = seed.Rows[0][0].Node.ElementID
					if kind == "detach_delete" || kind == "relationship_delete" {
						edge := nativeRunTest(t, admin, Request{Query: "MATCH (a),(b) WHERE elementId(a)=$a AND elementId(b)=$b CREATE (a)-[r:$($label) {guard:1}]->(b) RETURN r", Parameters: map[string]any{"a": identity, "b": seed.Rows[0][1].Node.ElementID, "label": label}})
						if kind == "relationship_delete" {
							objectKind, identity, edit.Kind = "relationship", edge.Rows[0][0].Relationship.ElementID, "delete"
						}
					}
				}
				l := dataLeaseTest(t, s, objectKind, objectName)
				if kind != "insert" {
					edit.Original = entitySnapshotTest(t, l, identity)
				}
				// An exact Bolt COMMIT frame, with no binary input containing it.
				relay := newNativeRelay(t, f, string([]byte{0, 2, 0xb0, 0x12, 0, 0}))
				relayDataOpenTest(s, f, relay)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type completion struct {
					result *contract.DataEditResult
					err    error
				}
				done := make(chan completion, 1)
				go func() {
					r, e := l.saveEntityEdits(ctx, 42, "neo4j", objectName, []contract.DataEdit{edit})
					done <- completion{r, e}
				}()
				select {
				case <-relay.observed:
				case <-time.After(15 * time.Second):
					t.Fatal("original native entity COMMIT not forwarded")
				}
				committed := false
				deadline := time.Now().Add(15 * time.Second)
				for time.Now().Before(deadline) {
					query := "MATCH (n) WHERE $label IN labels(n) AND n.guard=1 RETURN count(n)"
					if kind == "relationship_delete" {
						query = "MATCH ()-[n]->() WHERE elementId(n)=$identity RETURN count(n)"
					} else if kind != "insert" {
						query = "MATCH (n) WHERE elementId(n)=$identity RETURN count(n)"
					}
					read := nativeRunTest(t, admin, Request{Query: query, Parameters: map[string]any{"label": label, "identity": identity}, Mode: "read"})
					wanted := "0"
					if kind == "insert" {
						wanted = "1"
					}
					if read.Rows[0][0].Text == wanted {
						committed = true
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !committed {
					cancel()
					relay.Close()
					<-done
					t.Fatal("entity COMMIT not independently confirmed before response fault")
				}
				// A distinctive later write makes accidental replays observable.
				nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) SET n.guard=-789", Parameters: map[string]any{"label": label}})
				queued := make(chan error, 1)
				go func() {
					_, e := l.saveEntityEdits(context.Background(), 42, "neo4j", objectName, []contract.DataEdit{edit})
					queued <- e
				}()
				closed := make(chan struct{})
				switch fault {
				case "lost":
					relay.Close()
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
					relay.Close()
					t.Fatal("original entity executor did not acknowledge fault")
				}
				if got.err == nil || got.result == nil || got.result.CommitConfirmed || !got.result.CommitUncertain {
					t.Fatal("missing entity COMMIT response admitted success", got.err)
				}
				r := entityReceiptTest(t, got.result)
				if !r.CommitAttempted || r.CommitConfirmed || !r.OutcomeUnknown || r.RollbackAttempted {
					t.Fatal("uncertain original entity receipt lost native boundaries")
				}
				if fault == "close" || fault == "session_close" {
					select {
					case <-closed:
					case <-time.After(15 * time.Second):
						t.Fatal("original entity close did not acknowledge executor")
					}
				}
				select {
				case err := <-queued:
					if !errors.Is(err, contract.ErrRevoked) && !errors.Is(err, context.Canceled) {
						t.Fatal("queued uncertain entity mutation admitted dispatch", err)
					}
				case <-time.After(15 * time.Second):
					t.Fatal("queued entity executor did not settle")
				}
				if _, err := l.saveEntityEdits(context.Background(), 42, "neo4j", objectName, []contract.DataEdit{edit}); !errors.Is(err, contract.ErrRevoked) {
					t.Fatal("uncertain entity mutation admitted explicit replay", err)
				}
				if fault == "lost" || fault == "cancel" {
					count, err := l.count(context.Background(), 42, "neo4j", objectName)
					if err != nil || count < 0 {
						t.Fatal("unknown entity write revoked independent original read scope", err)
					}
				}
				replacement := nativeOpenTest(t, f)
				_ = l.Close()
				read := nativeRunTest(t, replacement, Request{Query: "MATCH (n) WHERE $label IN labels(n) RETURN n.guard", Parameters: map[string]any{"label": label}, Mode: "read"})
				for _, row := range read.Rows {
					if row[0].Text != "-789" {
						t.Fatal("entity mutation replayed over distinctive subsequent value")
					}
				}
				if len(read.Rows) != 1 && kind != "relationship_delete" || kind == "relationship_delete" && len(read.Rows) != 2 || relay.dispatches.Load() != 1 || !replacement.Alive() {
					t.Fatal("original entity cleanup/replay damaged replacement or changed node count")
				}
			})
		}
	}
}

func TestNeo4jNativeDataEntityLocksAndConcurrentChanges(t *testing.T) {
	f := dataFixtureTest(t)
	admin, s := nativeOpenTest(t, f), nativeSessionTest(t, f)
	label := "NyaEntityLock_" + f.Nonce
	cleanupLabelTest(t, admin, label)
	ctx := context.Background()
	seed := nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2}) CREATE (a)-[r:$($label) {guard:1}]->(b) RETURN a,b,r", Parameters: map[string]any{"label": label}})
	for _, test := range []struct{ kind, edit, id, pattern string }{
		{"label", "delete", seed.Rows[0][1].Node.ElementID, "(entity)"},
		{"label", "detach_delete", seed.Rows[0][0].Node.ElementID, "(entity)"},
		{"relationship", "delete", seed.Rows[0][2].Relationship.ElementID, "()-[entity]->()"},
	} {
		t.Run(test.kind+"/"+test.edit, func(t *testing.T) {
			l := dataLeaseTest(t, s, test.kind, label)
			original := entitySnapshotTest(t, l, test.id)
			holder := nativeOpenTest(t, f).driver.NewSession(ctx, nd.SessionConfig{DatabaseName: "neo4j"})
			defer holder.Close(ctx)
			tx, err := holder.BeginTransaction(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Close(ctx)
			r, err := tx.Run(ctx, "MATCH "+test.pattern+" WHERE elementId(entity)=$id SET entity.guard=789 RETURN entity", map[string]any{"id": test.id})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.Consume(ctx); err != nil {
				t.Fatal(err)
			}
			type completion struct {
				result *contract.DataEditResult
				err    error
			}
			done := make(chan completion, 1)
			go func() {
				r, e := l.saveEntityEdits(ctx, 42, "neo4j", label, []contract.DataEdit{{Kind: test.edit, Original: original}})
				done <- completion{r, e}
			}()
			query := l.match() + " AND elementId(entity)=$element_id SET entity[$lock_key]=entity[$lock_key] RETURN entity LIMIT 2"
			waitEntityBlockedTest(t, admin, query)
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			got := <-done
			if !errors.Is(got.err, contract.ErrDataConflict) || got.result.CommitConfirmed || got.result.CommitUncertain || !entityReceiptTest(t, got.result).RollbackConfirmed {
				t.Fatal("entity deletion skipped post-lock complete fingerprint comparison", got.err)
			}
			read := nativeRunTest(t, admin, Request{Query: "MATCH " + test.pattern + " WHERE elementId(entity)=$id RETURN entity.guard", Parameters: map[string]any{"id": test.id}})
			if len(read.Rows) != 1 || read.Rows[0][0].Text != "789" {
				t.Fatal("entity conflict changed concurrent entity or adjacency")
			}
		})
	}
	// A relationship's properties can change without changing its node's
	// fingerprint. The detached deletion must wait for that original edge lock
	// and capture the committed typed value before deleting it.
	nodeID, edgeID := seed.Rows[0][0].Node.ElementID, seed.Rows[0][2].Relationship.ElementID
	l := dataLeaseTest(t, s, "label", label)
	original := entitySnapshotTest(t, l, nodeID)
	holder := nativeOpenTest(t, f).driver.NewSession(ctx, nd.SessionConfig{DatabaseName: "neo4j"})
	defer holder.Close(ctx)
	tx, err := holder.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close(ctx)
	r, err := tx.Run(ctx, "MATCH ()-[r]->() WHERE elementId(r)=$id SET r.guard=$guard RETURN r", map[string]any{"id": edgeID, "guard": int64(-9223372036854775808)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Consume(ctx); err != nil {
		t.Fatal(err)
	}
	type completion struct {
		result *contract.DataEditResult
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		result, e := l.saveEntityEdits(ctx, 42, "neo4j", label, []contract.DataEdit{{Kind: "detach_delete", Original: original}})
		done <- completion{result, e}
	}()
	waitEntityBlockedTest(t, admin, l.detachDeleteQuery())
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || !got.result.CommitConfirmed {
		t.Fatal("detached entity edge-lock save failed", got.err)
	}
	edges := entityReceiptTest(t, got.result).Statements[1].Result.Rows[0][1]
	if len(edges.Values) != 1 || propertyTest(t, edges.Values[0].Properties, "element_id").Text != edgeID || propertyTest(t, propertyTest(t, edges.Values[0].Properties, "properties").Properties, "guard").Text != "-9223372036854775808" {
		t.Fatal("detached receipt captured edge properties before original lock acknowledgement")
	}
}

func waitEntityBlockedTest(t *testing.T, admin *Client, query string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r := nativeRunTest(t, admin, Request{Query: "SHOW TRANSACTIONS YIELD currentQuery,status WHERE currentQuery=$query RETURN status", Database: "system", Parameters: map[string]any{"query": query}})
		if len(r.Rows) > 0 && strings.Contains(r.Rows[0][0].Text, "Blocked") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("actual entity mutation was not observed waiting for original lock")
}
