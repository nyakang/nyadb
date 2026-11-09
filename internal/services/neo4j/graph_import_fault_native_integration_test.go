package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
)

func TestNeo4jNativeGraphImportActualCommitReplyLostCancelAndOriginalCloseNeverReplay(t *testing.T) {
	f := dataFixtureTest(t)
	admin := nativeOpenTest(t, f)
	for _, fault := range []string{"lost", "cancel", "close", "session_close"} {
		t.Run(fault, func(t *testing.T) {
			label := "NyaGraphCommit_" + f.Nonce + "_" + fault
			cleanupLabelTest(t, admin, label)
			s := nativeSessionTest(t, f)
			l := dataLeaseTest(t, s, "label", label)
			baseline := nativeRunTest(t, admin, Request{Query: "CREATE (n:$($label) {guard:10}) RETURN n", Parameters: map[string]any{"label": label}}).Rows[0][0].Node.ElementID
			original := entitySnapshotTest(t, l, baseline)
			bundle := contract.GraphImportBundle{Format: graphimport.Format, Nodes: []Node{{ElementID: "new", Labels: []string{label}, Properties: []Property{{Name: "guard", Value: Value{Kind: "integer", Text: "1"}}}}}, Relationships: []Relationship{{ElementID: "edge", StartElementID: "new", EndElementID: "new", Type: label}}}
			p := graphImportPreviewTest(t, l, contract.GraphImportRequest{Bundle: bundle})
			relay := newNativeRelay(t, f, string([]byte{0, 2, 0xb0, 0x12, 0, 0}))
			relayDataOpenTest(s, f, relay)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				result *contract.GraphImportResult
				err    error
			}
			done := make(chan completion, 1)
			go func() { r, err := l.submitGraphImport(ctx, 42, "neo4j", label, p); done <- completion{r, err} }()
			select {
			case <-relay.observed:
			case <-time.After(15 * time.Second):
				t.Fatal("original graph COMMIT not forwarded")
			}
			committed := false
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				r := nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) AND n.guard=1 MATCH (n)-[r]->(n) WHERE type(r)=$label RETURN count(r)", Parameters: map[string]any{"label": label}, Mode: "read"})
				if r.Rows[0][0].Text == "1" {
					committed = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !committed {
				cancel()
				relay.Close()
				<-done
				t.Fatal("graph transaction not independently committed before response fault")
			}
			nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) SET n.guard=-789", Parameters: map[string]any{"label": label}})
			queued := make(chan error, 1)
			go func() {
				_, err := l.save(context.Background(), 42, "neo4j", label, []contract.DataEdit{{Kind: "update", Original: original, Values: map[string]contract.DataValue{"property.guard": {Kind: contract.Integer, Text: "2"}}}})
				queued <- err
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
				t.Fatal("graph executor did not acknowledge fault")
			}
			if got.err == nil || got.result == nil || got.result.CommitConfirmed || !got.result.OutcomeUnknown || !got.result.CommitAttempted || got.result.RollbackAttempted || len(got.result.Identities) != 0 || got.result.NodesCreated != 0 || got.result.RelationshipsCreated != 0 {
				t.Fatal("lost graph COMMIT admitted generated IDs or success", got.err, got.result)
			}
			var receipt graphImportReceipt
			if json.Unmarshal([]byte(got.result.NativeJSON), &receipt) != nil || !receipt.OutcomeUnknown || !receipt.BeginConfirmed || len(receipt.Identities) != 2 || len(receipt.Statements) != 2 {
				t.Fatal("unknown graph receipt lost provisional complete batch")
			}
			select {
			case err := <-queued:
				if !errors.Is(err, contract.ErrRevoked) && !errors.Is(err, context.Canceled) {
					t.Fatal("uncertain graph did not poison queued original writes", err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("queued original write did not settle")
			}
			if fault == "close" || fault == "session_close" {
				select {
				case <-closed:
				case <-time.After(15 * time.Second):
					t.Fatal("original close did not await graph executor")
				}
			}
			if _, err := l.submitGraphImport(context.Background(), 42, "neo4j", label, p); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("unknown graph plan replayed", err)
			}
			check := nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) RETURN count(n),collect(n.guard)", Parameters: map[string]any{"label": label}, Mode: "read"})
			if check.Rows[0][0].Text != "2" {
				t.Fatal("unknown original graph replayed nodes")
			}
			for _, v := range check.Rows[0][1].Values {
				if v.Text != "-789" {
					t.Fatal("queued write changed distinctive postcommit state")
				}
			}
		})
	}
}
