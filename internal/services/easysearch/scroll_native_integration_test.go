package easysearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestEasysearchNativeScrollReadsCompleteOriginalSnapshotBeyondPITWindow(t *testing.T) {
	cfg, secret := nativeFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	index := ownedIndex(t, c, ctx)
	const count = 10017
	var batch bytes.Buffer
	for i := 0; i < count; i++ {
		fmt.Fprintf(&batch, "{\"create\":{\"_index\":%q,\"_id\":\"row-%05d\"}}\n{\"name\":\"original\",\"exact\":9007199254740993}\n", index, i)
	}
	reply, err := c.Do(ctx, Request{Method: "POST", Path: "/_bulk?refresh=wait_for", ContentType: "application/x-ndjson", Body: batch.Bytes()})
	var seeded struct {
		Errors *bool             `json:"errors"`
		Items  []json.RawMessage `json:"items"`
	}
	if err != nil || decodeJSON(reply.Body, &seeded) != nil || seeded.Errors == nil || *seeded.Errors || len(seeded.Items) != count {
		t.Fatal("native bulk fixture receipt", err)
	}
	// The bounded PIT must deliver its complete final window page, even
	// though an extra lookahead hit would exceed the native 10000 limit.
	bounded, err := c.OpenCursor(ctx, index, []byte(`{"match_all":{}}`), 500)
	if err != nil {
		t.Fatal(err)
	}
	last, err := bounded.ReadAfter(ctx, []json.RawMessage{json.RawMessage(`9500`)}, 500)
	if err != nil || len(last.Documents) != 500 || last.Total != count || !last.HasMore || len(last.After) != 1 || string(last.After[0]) != "10000" {
		t.Fatal("native PIT lost its final window page", err)
	}
	if _, err = bounded.ReadAfter(ctx, last.After, 500); !errors.Is(err, ErrBounds) {
		t.Fatal("native PIT silently exceeded its window", err)
	}
	if err = bounded.Close(ctx); err != nil {
		t.Fatal(err)
	}
	excluded := make([]string, 0, 17)
	for i := 10000; i < count; i++ {
		excluded = append(excluded, fmt.Sprintf("row-%05d", i))
	}
	query, _ := json.Marshal(map[string]any{"bool": map[string]any{"must_not": map[string]any{"ids": map[string]any{"values": excluded}}}})
	exact, err := c.OpenCursor(ctx, index, query, 500)
	if err != nil {
		t.Fatal(err)
	}
	last, err = exact.ReadAfter(ctx, []json.RawMessage{json.RawMessage(`9500`)}, 500)
	if err != nil || len(last.Documents) != 500 || last.Total != 10000 || last.HasMore {
		t.Fatal("exact native PIT window did not reach its final hit", err)
	}
	if err = exact.Close(ctx); err != nil {
		t.Fatal(err)
	}
	scroll, err := c.OpenScroll(ctx, index, []byte(`{"match_all":{}}`), 500)
	if err != nil {
		t.Fatal("original native scroll", err)
	}
	page, err := scroll.Next(ctx)
	if err != nil || len(page.Documents) != 500 || page.Total != count {
		t.Fatal("native first scroll page", err)
	}
	if _, err = c.CreateDocument(ctx, DocumentID{Index: index, ID: "late"}, []byte(`{"name":"outside original snapshot"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ReplaceDocument(ctx, page.Documents[0], []byte(`{"name":"updated after original snapshot"}`)); err != nil {
		t.Fatal(err)
	}
	seen := map[DocumentID]bool{}
	for {
		for _, document := range page.Documents {
			if seen[document.Identity] || document.Identity.ID == "late" || !bytes.Contains(document.Source, []byte("9007199254740993")) {
				t.Fatal("original native scroll duplicated, changed or truncated a document")
			}
			seen[document.Identity] = true
		}
		if !page.HasMore {
			break
		}
		page, err = scroll.Next(ctx)
		if err != nil || page.Total != count {
			t.Fatal("complete native scroll beyond PIT window", err)
		}
	}
	if len(seen) != count {
		t.Fatalf("native scroll returned %d original documents, want %d", len(seen), count)
	}
	if err = scroll.Close(ctx); err != nil {
		t.Fatal("exact original scroll cleanup", err)
	}
	if err = scroll.Close(ctx); err != nil {
		t.Fatal("idempotent scroll cleanup", err)
	}
	if _, err = scroll.Next(ctx); !errors.Is(err, ErrRetired) {
		t.Fatal("closed original scroll resurrected", err)
	}
	// Terminal client cleanup revokes another independent scroll and PIT.
	terminal, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	other, err := terminal.OpenScroll(ctx, index, []byte(`{"match_all":{}}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	pit, err := terminal.OpenCursor(ctx, index, []byte(`{"match_all":{}}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = terminal.Close(); err != nil {
		t.Fatal("client terminal resource cleanup", err)
	}
	if _, err = other.Next(ctx); !errors.Is(err, ErrRetired) {
		t.Fatal("terminal close left scroll alive", err)
	}
	if _, err = pit.Next(ctx); !errors.Is(err, ErrRetired) {
		t.Fatal("terminal close left PIT alive", err)
	}
}
