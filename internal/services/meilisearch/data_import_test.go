package meilisearch

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func documentImportEdits(raw ...string) []contract.DataEdit {
	var edits []contract.DataEdit
	for _, doc := range raw {
		edits = append(edits, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: doc}}})
	}
	return edits
}

func TestDocumentImportOriginalReceiptRejectsReplayUnsupportedAndDuplicateIDs(t *testing.T) {
	l, f := mockDataWrite(t)
	ctx := context.Background()
	for _, mode := range []string{"", "append", "replace", "upsert"} {
		if _, err := l.importDocuments(ctx, l.object.ProfileID, "owned", "documents", contract.DataImportRequest{Mode: mode, Edits: documentImportEdits(`{"id":"new"}`)}); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal(err)
		}
	}
	for _, docs := range [][]contract.DataEdit{documentImportEdits(`{"id":"same"}`, `{"id":"same"}`), documentImportEdits(`{"id":9007199254740993}`, `{"id":"9007199254740993"}`), documentImportEdits(`{"id":"new","nested":{"x":1,"x":2}}`)} {
		if _, err := l.importDocuments(ctx, l.object.ProfileID, "owned", "documents", contract.DataImportRequest{Mode: "document_replace", Edits: docs}); err == nil || f.writes.Load() != 0 {
			t.Fatal("invalid admission", err)
		}
	}
	r, err := l.importDocuments(ctx, l.object.ProfileID, "owned", "documents", contract.DataImportRequest{Mode: "document_replace", Edits: documentImportEdits(`{"id":9007199254740993,"new":true}`)})
	if err != nil || r.Committed || r.Saved.Async == nil || r.Saved.Applied != 0 {
		t.Fatal(r, err)
	}
	f.mu.Lock()
	f.status = "succeeded"
	f.mu.Unlock()
	v, err := l.verifyWrite(ctx, l.object.ProfileID, "owned", "documents", r.Saved)
	if err != nil || v.Status != "applied" || v.Recovered.Applied != 1 || !v.Recovered.Verified || len(v.Recovered.Outcomes) != 0 {
		t.Fatal(v, err)
	}
	if f.writes.Load() != 1 {
		t.Fatal("observation wrote")
	}
}
