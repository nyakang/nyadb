package couchdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const documentTestRevision = "1-0123456789abcdef0123456789abcdef"

func documentWire(id string) string {
	return fmt.Sprintf(`{"_id":%q,"_rev":%q,"n":9007199254740993,"nested":{"null":null,"list":[false,""]}}`, id, documentTestRevision)
}
func documentRowWire(id string) string {
	return fmt.Sprintf(`{"id":%q,"key":%q,"value":{"rev":%q},"doc":%s}`, id, id, documentTestRevision, documentWire(id))
}
func allDocumentWire(ids ...string) string {
	rows := make([]string, len(ids))
	for i, id := range ids {
		rows[i] = documentRowWire(id)
	}
	return `{"total_rows":4,"offset":0,"update_seq":"opaque-native-sequence","rows":[` + strings.Join(rows, ",") + `]}`
}

func TestCouchDBAllDocumentsRetainsNativeJSONAndDeletedBoundarySuccessor(t *testing.T) {
	var calls atomic.Int32
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/proxy/couchdb/owned/_all_docs" || r.URL.Query().Get("limit") != "4" || r.URL.Query().Get("include_docs") != "true" || r.URL.Query().Get("conflicts") != "true" {
			t.Error("native page request changed")
		}
		if r.URL.Query().Get("startkey") == `"b"` {
			fmt.Fprint(w, allDocumentWire("c", "d"))
		} else {
			fmt.Fprint(w, allDocumentWire("a", "b", "c", "d"))
		}
	})
	page, err := c.AllDocuments(context.Background(), "owned", "", 2)
	if err != nil || len(page.Documents) != 2 || page.NextKey != "b" || !page.HasMore || page.TotalRows != 4 || string(page.UpdateSequence) != `"opaque-native-sequence"` || string(page.Documents[0].JSON) != documentWire("a") {
		t.Fatal("native page changed", err)
	}
	next, err := c.AllDocuments(context.Background(), "owned", page.NextKey, 2)
	if err != nil || len(next.Documents) != 2 || next.Documents[0].ID != "c" || next.Documents[1].ID != "d" || next.HasMore {
		t.Fatal("deleted boundary skipped successor", err)
	}
	if calls.Load() != 2 {
		t.Fatal("page request replayed")
	}
}

func TestCouchDBAllDocumentsRejectsInconsistentResponseWithoutPublishingRows(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate":               allDocumentWire("a", "a"),
		"unordered":               allDocumentWire("b", "a"),
		"key_mismatch":            strings.Replace(allDocumentWire("a"), `"key":"a"`, `"key":"other"`, 1),
		"revision_mismatch":       strings.Replace(allDocumentWire("a"), `"value":{"rev":"1-`, `"value":{"rev":"2-`, 1),
		"duplicate_document_name": strings.Replace(allDocumentWire("a"), `"n":9007199254740993`, `"n":1,"n":2`, 1),
		"literal_percent_id":      allDocumentWire("unsupported%id"),
		"invalid_total":           strings.Replace(allDocumentWire("a"), `"total_rows":4`, `"total_rows":1.5`, 1),
		"missing_sequence":        strings.Replace(allDocumentWire("a"), `"update_seq":"opaque-native-sequence",`, "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			page, err := c.AllDocuments(context.Background(), "owned", "", 2)
			if err == nil || page != nil {
				t.Fatal("inconsistent native response published")
			}
		})
	}
}

func TestCouchDBFindPreservesSelectorAndOpaqueBookmarkWithoutFieldProjection(t *testing.T) {
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]json.RawMessage
		if decodeJSON(raw, &body) != nil || r.URL.Path != "/proxy/couchdb/owned/_find" || r.Method != "POST" || string(body["selector"]) != `{"n":{"$gte":9007199254740993}}` || string(body["bookmark"]) != `"opaque/boundary+=="` || body["fields"] != nil || len(body) != 4 {
			t.Error("typed native read changed request")
		}
		fmt.Fprintf(w, `{"docs":[%s,%s],"bookmark":"next-opaque","warning":"native index warning"}`, documentWire("a"), documentWire("b"))
	})
	page, err := c.Find(context.Background(), "owned", FindRequest{Selector: json.RawMessage(`{"n":{"$gte":9007199254740993}}`), Sort: json.RawMessage(`["_id"]`), Bookmark: "opaque/boundary+==", Limit: 2})
	if err != nil || len(page.Documents) != 2 || !page.HasMore || page.Bookmark != "next-opaque" || page.Warning != "native index warning" || string(page.Documents[0].JSON) != documentWire("a") {
		t.Fatal("Mango complete source changed", err)
	}
}

func TestCouchDBFindCancellationCertifiesReadAndNeverReplays(t *testing.T) {
	entered := make(chan struct{})
	var calls atomic.Int32
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		close(entered)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	request := FindRequest{Selector: json.RawMessage(`{}`), Limit: 2}
	go func() { _, err := c.Find(ctx, "owned", request); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original read did not enter")
	}
	cancel()
	select {
	case err := <-done:
		var interrupted *Interrupted
		if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrRetired) {
			t.Fatal("typed read claimed uncertain write", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original read worker did not settle")
	}
	if _, err := c.Find(context.Background(), "owned", request); !errors.Is(err, ErrRetired) || calls.Load() != 1 {
		t.Fatal("retired read replayed", err)
	}
}

func TestCouchDBFindRejectsInvalidOpaqueBookmarkWithoutIO(t *testing.T) {
	var calls atomic.Int32
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, bookmark := range []string{"\xff", strings.Repeat("x", (64<<10)+1)} {
		_, err := c.Find(context.Background(), "owned", FindRequest{Selector: json.RawMessage(`{}`), Limit: 2, Bookmark: bookmark})
		if !errors.Is(err, ErrInvalid) || calls.Load() != 0 {
			t.Fatal("opaque bookmark changed or dispatched", err)
		}
	}
}
