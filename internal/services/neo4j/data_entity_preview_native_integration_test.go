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

func TestNeo4jNativeDataEntityPreviewCapturedTypesEdgesAndOneAttempt(t *testing.T) {
	f := dataFixtureTest(t)
	admin, s := nativeOpenTest(t, f), nativeSessionTest(t, f)
	label := "NyaEntityPreview_" + f.Nonce + "_猫`"
	cleanupLabelTest(t, admin, label)
	ctx := context.Background()
	props := map[string]any{"int": int64(-9223372036854775808), "bytes": []byte{0, 255}, "date": mustEntityParameterTest(t, graphDataValueTest(t, extendedPropertyTest(t, "date", "10000-02-29")))}
	seed := nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2}) CREATE (a)-[r:$($label)]->(b),(a)-[p:$($label)]->(b),(b)-[reverse:$($label)]->(a),(a)-[loop:$($label)]->(a) SET r=$props,p=$props,reverse=$props,loop=$props RETURN a,b", Parameters: map[string]any{"label": label, "props": props}})
	l := dataLeaseTest(t, s, "label", label)
	original := entitySnapshotTest(t, l, seed.Rows[0][0].Node.ElementID)
	plain, err := l.previewEntityEdits(ctx, 42, "neo4j", label, []contract.DataEdit{{Kind: "delete", Original: original}})
	if err != nil || len(plain.Snapshot().Items) != 1 || !plain.Snapshot().Items[0].Blocked || plain.Snapshot().Items[0].AffectedRelationships != 4 {
		t.Fatal("plain deletion preview lost connected-node refusal or distinct incident edges", err)
	}
	if result, err := l.submitEntityPlan(ctx, 42, "neo4j", label, plain); !errors.Is(err, contract.ErrDataConflict) || result != nil {
		t.Fatal("blocked plain deletion dispatched a native mutation", err)
	}
	input := []contract.DataEdit{
		{Kind: "insert", Values: map[string]contract.DataValue{"property.bytes": {Kind: contract.Binary, Binary: true, Bytes: []byte{0, 255}}}},
		{Kind: "detach_delete", Original: original}, {Kind: "insert"},
	}
	plan, err := l.previewEntityEdits(ctx, 42, "neo4j", label, input)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	x := plan.Snapshot()
	var evidence struct {
		Observations []entityPreviewObservation
	}
	if json.Unmarshal([]byte(x.NativeJSON), &evidence) != nil || len(evidence.Observations) != 3 || len(evidence.Observations[1].Read.Rows[0][1].Values) != 4 || len(evidence.Observations[1].Parameters) != 4 {
		t.Fatal("preview lost original complete typed edges or read parameter evidence")
	}
	for _, count := range evidence.Observations[1].Read.Summary.Counters {
		if count != 0 {
			t.Fatal("preview dispatched writes")
		}
	}
	want := x.NativeJSON
	input[0].Values["property.bytes"].Bytes[0] = 9
	input[0].Values["property.guard"] = contract.DataValue{Kind: contract.Integer, Text: "999"}
	original.Metadata = "changed caller snapshot"
	x.Edits[0].Kind = "delete"
	x.Edits[0].Values["property.bytes"].Bytes[0] = 8
	x.Items[1].AffectedRelationships = 0
	if plan.Snapshot().NativeJSON != want || plan.Snapshot().Edits[0].Values["property.bytes"].Bytes[0] != 0 || plan.Snapshot().Items[1].AffectedRelationships != 4 {
		t.Fatal("preview accessor or original request mutated executor-owned values")
	}
	result, err := l.submitEntityPlan(ctx, 42, "neo4j", label, plan)
	if err != nil || result == nil || !result.CommitConfirmed || result.Applied != 3 || result.Outcomes[0].Saved == nil || result.Outcomes[2].Saved == nil {
		t.Fatal("previewed creation and exact-edge deletion failed", err)
	}
	receipt := entityReceiptTest(t, result)
	if !receipt.CommitConfirmed || len(receipt.Bookmarks) == 0 || len(receipt.Statements) != 5 || strings.Contains(receipt.Statements[4].Query, "DETACH DELETE") {
		t.Fatal("previewed deletion lost separate locked edge evidence and guarded plain deletion")
	}
	var locked *Response
	for i := range receipt.Statements {
		if len(receipt.Statements[i].Result.Columns) == 2 {
			locked = &receipt.Statements[i].Result
		}
	}
	if locked == nil || len(locked.Rows[0][1].Values) != 4 {
		t.Fatal("native receipt lost complete locked affected-edge observation")
	}
	read := nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) RETURN n.guard,n.bytes ORDER BY elementId(n)", Parameters: map[string]any{"label": label}})
	if len(read.Rows) != 3 {
		t.Fatal("previewed deletion changed unselected endpoints or creation count")
	}
	for _, row := range read.Rows {
		if row[1].Kind == "bytes" && row[1].Text != "AP8=" {
			t.Fatal("native creation followed changed caller bytes")
		}
	}
	if _, err = l.submitEntityPlan(ctx, 42, "neo4j", label, plan); !errors.Is(err, contract.ErrRevoked) || len(plan.Snapshot().Edits) != 0 {
		t.Fatal("one-attempt preview admitted repeated creation/deletion", err)
	}
	// Adjacent selected nodes share incident edges. Earlier provisional deletes
	// must not look like an external change to the later selected node.
	batchLabel := label + "_batch"
	cleanupLabelTest(t, admin, batchLabel)
	batch := nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2}),(c:$($label) {guard:3}) CREATE (a)-[:NYA_PREVIEW_BATCH]->(b),(a)-[:NYA_PREVIEW_BATCH]->(b),(b)-[:NYA_PREVIEW_BATCH]->(a),(a)-[:NYA_PREVIEW_BATCH]->(a),(b)-[:NYA_PREVIEW_BATCH]->(b),(b)-[:NYA_PREVIEW_BATCH]->(c) RETURN a,b,c", Parameters: map[string]any{"label": batchLabel}})
	bound := dataLeaseTest(t, s, "label", batchLabel)
	batchPlan, err := bound.previewEntityEdits(ctx, 42, "neo4j", batchLabel, []contract.DataEdit{
		{Kind: "detach_delete", Original: entitySnapshotTest(t, bound, batch.Rows[0][0].Node.ElementID)},
		{Kind: "detach_delete", Original: entitySnapshotTest(t, bound, batch.Rows[0][1].Node.ElementID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = bound.submitEntityPlan(ctx, 42, "neo4j", batchLabel, batchPlan)
	if err != nil || !result.CommitConfirmed || result.Applied != 2 {
		t.Fatal("batch preview confused its own shared-edge deletions with external changes", err)
	}
	var deleted int64
	for _, statement := range entityReceiptTest(t, result).Statements {
		deleted += statement.Result.Summary.Counters["relationships_deleted"]
	}
	read = nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) RETURN elementId(n),n.guard", Parameters: map[string]any{"label": batchLabel}})
	if deleted != 6 || len(read.Rows) != 1 || read.Rows[0][0].Text != batch.Rows[0][2].Node.ElementID || read.Rows[0][1].Text != "3" {
		t.Fatal("batch shared-edge deletion changed an unselected endpoint or duplicated edge deletion")
	}
	emptyLabel := label + "_empty_batch"
	cleanupLabelTest(t, admin, emptyLabel)
	empty := nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label)),(b:$($label)) CREATE (a)-[:NYA_PREVIEW_EMPTY]->(b) RETURN a,b", Parameters: map[string]any{"label": emptyLabel}})
	emptyLease := dataLeaseTest(t, s, "label", emptyLabel)
	emptyPlan, err := emptyLease.previewEntityEdits(ctx, 42, "neo4j", emptyLabel, []contract.DataEdit{
		{Kind: "detach_delete", Original: entitySnapshotTest(t, emptyLease, empty.Rows[0][0].Node.ElementID)},
		{Kind: "detach_delete", Original: entitySnapshotTest(t, emptyLease, empty.Rows[0][1].Node.ElementID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = emptyLease.submitEntityPlan(ctx, 42, "neo4j", emptyLabel, emptyPlan)
	if err != nil || !result.CommitConfirmed || result.Applied != 2 {
		t.Fatal("shared-edge deletion could not settle the later node's empty adjacency", err)
	}
	count, err := emptyLease.count(ctx, 42, "neo4j", emptyLabel)
	if err != nil || count != 0 {
		t.Fatal("two-node shared-edge deletion retained a selected node", err)
	}
}

func TestNeo4jNativeDataEntityPreviewChangedObservationRollsBackCapturedBatch(t *testing.T) {
	f := dataFixtureTest(t)
	admin, s := nativeOpenTest(t, f), nativeSessionTest(t, f)
	ctx := context.Background()
	for _, change := range []string{"node", "edge", "add", "remove", "type", "endpoint"} {
		t.Run(change, func(t *testing.T) {
			label := "NyaPreviewChange_" + f.Nonce + "_" + change
			cleanupLabelTest(t, admin, label)
			seed := nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2}),(c:$($label) {guard:3}) CREATE (a)-[r:$($label) {guard:1}]->(b) RETURN a,b,c,r", Parameters: map[string]any{"label": label}})
			l := dataLeaseTest(t, s, "label", label)
			original := entitySnapshotTest(t, l, seed.Rows[0][0].Node.ElementID)
			edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"property.guard": {Kind: contract.Integer, Text: "99"}}}, {Kind: "detach_delete", Original: original}}
			old, err := l.previewEntityEdits(ctx, 42, "neo4j", label, edits)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := l.previewEntityEdits(ctx, 42, "neo4j", label, edits)
			if err != nil {
				t.Fatal(err)
			}
			defer plan.Close()
			if _, err = l.submitEntityPlan(ctx, 42, "neo4j", label, old); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("replaced preview admitted submission", err)
			}
			queries := map[string]string{
				"node":     "MATCH (n) WHERE elementId(n)=$a SET n.guard=77",
				"edge":     "MATCH ()-[r]->() WHERE elementId(r)=$edge SET r.guard=77",
				"add":      "MATCH (a),(c) WHERE elementId(a)=$a AND elementId(c)=$c CREATE (a)-[:NYA_PREVIEW_NEW {guard:77}]->(c)",
				"remove":   "MATCH ()-[r]->() WHERE elementId(r)=$edge DELETE r",
				"type":     "MATCH (a)-[r]->(b) WHERE elementId(r)=$edge DELETE r CREATE (a)-[:NYA_PREVIEW_NEW {guard:77}]->(b)",
				"endpoint": "MATCH (a)-[r]->(),(c) WHERE elementId(r)=$edge AND elementId(c)=$c DELETE r CREATE (a)-[:NYA_PREVIEW_NEW {guard:77}]->(c)",
			}
			nativeRunTest(t, admin, Request{Query: queries[change], Parameters: map[string]any{"a": seed.Rows[0][0].Node.ElementID, "c": seed.Rows[0][2].Node.ElementID, "edge": seed.Rows[0][3].Relationship.ElementID}})
			result, err := l.submitEntityPlan(ctx, 42, "neo4j", label, plan)
			if !errors.Is(err, contract.ErrDataConflict) || result == nil || result.CommitConfirmed || result.CommitUncertain || !entityReceiptTest(t, result).RollbackConfirmed {
				t.Fatal("changed preview did not roll back earlier provisional creation", err)
			}
			read := nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) RETURN elementId(n),n.guard ORDER BY elementId(n)", Parameters: map[string]any{"label": label}})
			if len(read.Rows) != 3 {
				t.Fatal("preview conflict changed original identities")
			}
			for _, original := range seed.Rows[0][:3] {
				found := false
				for _, row := range read.Rows {
					found = found || row[0].Text == original.Node.ElementID
				}
				if !found {
					t.Fatal("preview conflict removed or recreated an original identity")
				}
			}
			for _, row := range read.Rows {
				if row[1].Text == "99" {
					t.Fatal("preview conflict retained provisional creation")
				}
			}
			if _, err = l.submitEntityPlan(ctx, 42, "neo4j", label, plan); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("failed original preview admitted replay", err)
			}
		})
	}
}

func TestNeo4jNativeDataEntityPreviewBoundsLocksCancellationAndClose(t *testing.T) {
	f := dataFixtureTest(t)
	admin, s := nativeOpenTest(t, f), nativeSessionTest(t, f)
	ctx := context.Background()
	for _, fixture := range []struct {
		suffix, query string
	}{
		{"wide", "CREATE (a:$($label)),(b:$($label)) WITH a,b UNWIND range(1,2001) AS i CREATE (a)-[:NYA_PREVIEW_WIDE]->(b)"},
		{"large", "CREATE (a:$($label)),(b:$($label)) CREATE (a)-[:NYA_PREVIEW_LARGE {text:left(reduce(s='x',i IN range(1,22)|s+s),2097252)}]->(b)"},
	} {
		label := "NyaPreviewBounds_" + f.Nonce + "_" + fixture.suffix
		cleanupLabelTest(t, admin, label)
		nativeRunTest(t, admin, Request{Query: fixture.query, Parameters: map[string]any{"label": label}})
		l := dataLeaseTest(t, s, "label", label)
		page, err := l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if plan, err := l.previewEntityEdits(ctx, 42, "neo4j", label, []contract.DataEdit{{Kind: "detach_delete", Original: page.RowSnapshots[0]}}); !errors.Is(err, ErrBounds) || plan != nil {
			t.Fatal("preview accepted unbounded or incomplete original relationships", err)
		}
		count, err := l.count(ctx, 42, "neo4j", label)
		if err != nil || count != 2 {
			t.Fatal("bounded read preview modified entities or retired independent reads", err)
		}
	}
	label := "NyaPreviewLocked_" + f.Nonce
	cleanupLabelTest(t, admin, label)
	seed := nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2}) CREATE (a)-[r:$($label) {guard:1}]->(b) RETURN a,r", Parameters: map[string]any{"label": label}})
	l := dataLeaseTest(t, s, "label", label)
	plan, err := l.previewEntityEdits(ctx, 42, "neo4j", label, []contract.DataEdit{{Kind: "detach_delete", Original: entitySnapshotTest(t, l, seed.Rows[0][0].Node.ElementID)}})
	if err != nil {
		t.Fatal(err)
	}
	holder := nativeOpenTest(t, f).driver.NewSession(ctx, nd.SessionConfig{DatabaseName: "neo4j"})
	defer holder.Close(ctx)
	tx, err := holder.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close(ctx)
	r, err := tx.Run(ctx, "MATCH ()-[r]->() WHERE elementId(r)=$id SET r.guard=789 RETURN r", map[string]any{"id": seed.Rows[0][1].Relationship.ElementID})
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
		result, err := l.submitEntityPlan(ctx, 42, "neo4j", label, plan)
		done <- completion{result, err}
	}()
	waitEntityBlockedTest(t, admin, l.lockedPreviewEdgesQuery())
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.result == nil || got.result.CommitConfirmed {
		t.Fatal("changed locked preview admitted deletion", got.err)
	}
	var deadlock *nd.Neo4jError
	if errors.As(got.err, &deadlock) && deadlock.Code == "Neo.TransientError.Transaction.DeadlockDetected" {
		// Native edge writers may acquire endpoint locks while committing. An
		// inverted external lock order can make this transaction the victim;
		// it must retain the exact failure and never acknowledge rollback/retry.
		receipt := entityReceiptTest(t, got.result)
		if !got.result.CommitUncertain || receipt.RollbackConfirmed || receipt.FailureCode != deadlock.Code {
			t.Fatal("native deadlock supplied false rollback acknowledgement")
		}
		if _, err := l.submitEntityPlan(ctx, 42, "neo4j", label, plan); !errors.Is(err, contract.ErrRevoked) {
			t.Fatal("native deadlock admitted original preview replay", err)
		}
	} else if !errors.Is(got.err, contract.ErrDataConflict) || got.result.CommitUncertain || !entityReceiptTest(t, got.result).RollbackConfirmed {
		t.Fatal("preview skipped post-lock comparison or lost original native failure", got.err)
	}
	read := nativeRunTest(t, admin, Request{Query: "MATCH (a)-[r]->(b) WHERE elementId(r)=$id RETURN elementId(a),r.guard,elementId(b)", Parameters: map[string]any{"id": seed.Rows[0][1].Relationship.ElementID}})
	if len(read.Rows) != 1 || read.Rows[0][0].Text != seed.Rows[0][0].Node.ElementID || read.Rows[0][1].Text != "789" {
		t.Fatal("locked preview failure deleted entities or damaged the concurrent edge value")
	}
	for _, fault := range []string{"cancel", "close", "session_close"} {
		t.Run(fault, func(t *testing.T) {
			s := nativeSessionTest(t, f)
			l := dataLeaseTest(t, s, "label", label)
			original := entitySnapshotTest(t, l, seed.Rows[0][0].Node.ElementID)
			relay := newNativeRelay(t, f, l.entityPreviewQuery())
			relayDataOpenTest(s, f, relay)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				p, err := l.previewEntityEdits(ctx, 42, "neo4j", label, []contract.DataEdit{{Kind: "detach_delete", Original: original}})
				if p != nil {
					_ = p.Close()
				}
				done <- err
			}()
			select {
			case <-relay.observed:
			case <-time.After(15 * time.Second):
				t.Fatal("original read preview not forwarded")
			}
			closed := make(chan struct{})
			switch fault {
			case "cancel":
				cancel()
			case "close":
				go func() { _ = l.Close(); close(closed) }()
			case "session_close":
				go func() { _ = s.Close(); close(closed) }()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("interrupted preview admitted a handle")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("preview executor did not acknowledge cancellation/close")
			}
			if fault != "cancel" {
				select {
				case <-closed:
				case <-time.After(15 * time.Second):
					t.Fatal("original preview close did not settle")
				}
			}
			if relay.dispatches.Load() != 1 || l.entityPlan != nil {
				t.Fatal("interrupted original preview replayed or retained a handle")
			}
		})
	}
}
