package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestGraphPropertyNeighborHistoryAndLaterConflictsPermitExplicitContinuedEditing(t *testing.T) {
	s, d, l := propertyDocumentTest(t, func(_ context.Context, request contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
		return propertyReceiptTest(request), nil
	})
	defer s.CloseAll()
	g := d.graphVisualState()
	original, _ := json.Marshal(d.result)
	read := contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"n"}, Rows: [][]contract.GraphValue{{{Kind: "node", Node: func() *contract.GraphNode { n := g.node("n:a").Value.Clone(); return &n }()}}}}
	l.graph = func(context.Context, contract.GraphQueryRequest) (contract.GraphResult, error) {
		return read.Clone(), nil
	}
	s.expandGraph(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	oldNeighbors, _ := json.Marshal(g.observations)
	startPropertyTest(t, s, d, "saved value")
	s.saveGraphProperty(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	read.Rows[0][0] = d.graphPropertyRecords[0].Receipt.Result.Rows[0][3].Clone()
	s.expandGraph(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if g.node("n:a").Conflicting || g.node("n:a").Value.Properties[0].Value.Text != "saved value" {
		t.Fatal("post-save neighbor read treated the original value as a current conflict")
	}
	if !s.pinResult(d) {
		t.Fatal(d.Error())
	}
	sg := d.snapshots[0].Document.graphVisualState()
	read.Rows[0][0].Node.Properties[0].Value.Text = "external later value"
	s.expandGraph(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if !g.node("n:a").Conflicting || s.canEditGraphProperty(d, g, g.properties()[0]) {
		t.Fatal("later external observation conflict was hidden or allowed editing")
	}
	s.verifyGraphProperty(d, 0)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if g.node("n:a").Conflicting || g.node("n:a").Value.Properties[0].Value.Text != "external later value" || !d.graphPropertyRecords[1].Verification || d.graphPropertyRecords[1].Receipt.Applied {
		t.Fatal("independent complete readback failed to establish its own observed baseline")
	}
	startPropertyTest(t, s, d, "continued explicit value")
	if d.graphPropertyDraft.request.Before.Text != "external later value" {
		t.Fatal("continued explicit draft used superseded property value")
	}
	s.saveGraphProperty(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	read.Rows[0][0] = d.graphPropertyRecords[2].Receipt.Result.Rows[0][3].Clone()
	s.expandGraph(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if g.node("n:a").Conflicting || sg.node("n:a").Conflicting || sg.node("n:a").Value.Properties[0].Value.Text != "saved value" || len(sg.observations) != 2 {
		t.Fatal("continued edit revived historical conflict or mutated independent snapshot")
	}
	after, _ := json.Marshal(d.result)
	oldAfter, _ := json.Marshal(g.observations[:1])
	if !bytes.Equal(original, after) || !bytes.Equal(oldNeighbors, oldAfter) || len(g.node("n:a").Cells) == 0 || len(g.node("n:a").Observations) != 4 {
		t.Fatal("observation reconciliation changed immutable source records or their links")
	}
}

func TestGraphPropertyTruncatedVerificationCannotReplaceConflictingBaseline(t *testing.T) {
	s, d, l := propertyDocumentTest(t, func(_ context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
		return propertyReceiptTest(r), nil
	})
	defer s.CloseAll()
	startPropertyTest(t, s, d, "saved value")
	s.saveGraphProperty(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	g := d.graphVisualState()
	read := contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"entity"}, Rows: [][]contract.GraphValue{{d.graphPropertyRecords[0].Receipt.Result.Rows[0][3].Clone()}}}
	read.Rows[0][0].Node.Properties[0].Value.Text = "external later value"
	l.graph = func(context.Context, contract.GraphQueryRequest) (contract.GraphResult, error) {
		return read.Clone(), nil
	}
	s.expandGraph(d)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	read.Truncated = true
	s.verifyGraphProperty(d, 0)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	g.rebuildObservations()
	if !g.node("n:a").Conflicting || g.node("n:a").Value.Properties[0].Value.Text != "saved value" || !d.graphPropertyRecords[1].Receipt.Result.Truncated {
		t.Fatal("truncated independent readback erased a real subsequent conflict")
	}
}
