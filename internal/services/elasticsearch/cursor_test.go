package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestElasticsearchCursorKeepsExactSortAndLatestPITOnIncompletePage(t *testing.T) {
	searches, closed := 0, ""
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_pit") && r.Method == "POST" {
			_, _ = io.WriteString(w, `{"id":"original-pit"}`)
			return
		}
		if r.URL.Path == "/_pit" {
			var body struct {
				ID string `json:"id"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid cleanup body")
			}
			closed = body.ID
			_, _ = io.WriteString(w, `{"succeeded":true,"num_freed":1}`)
			return
		}
		searches++
		var request map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&request) != nil || string(request["_source"]) != "true" || string(request["query"]) != `{"match_all":{}}` {
			t.Error("native request changed query/source semantics")
		}
		if searches == 1 {
			_, _ = io.WriteString(w, `{"pit_id":"fresh-pit","timed_out":false,"_shards":{"total":1,"successful":1,"failed":0},"hits":{"total":{"value":2,"relation":"eq"},"hits":[{"_index":"own","_id":"one","_routing":"tenant","_seq_no":9007199254740993,"_primary_term":1,"_source":{"n":9007199254740993},"sort":[9007199254740993]},{"_index":"own","_id":"two","_routing":"tenant","_seq_no":2,"_primary_term":1,"_source":{"n":2},"sort":[9007199254740994]}]}}`)
		} else {
			if string(request["search_after"]) != `[9007199254740993]` || !strings.Contains(string(request["pit"]), "fresh-pit") {
				t.Error("cursor reused old PIT or rounded sort values")
			}
			_, _ = io.WriteString(w, `{"pit_id":"latest-pit","timed_out":true,"_shards":{"total":1,"successful":1,"failed":0},"hits":{"total":{"value":2,"relation":"eq"},"hits":[]}}`)
		}
	})
	cursor, err := client.OpenCursor(context.Background(), "own", json.RawMessage(`{"match_all":{}}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	page, err := cursor.Next(context.Background())
	if err != nil || !page.HasMore || page.Total != 2 || len(page.Documents) != 1 || page.Documents[0].Identity.Routing != "tenant" || string(page.Documents[0].Source) != `{"n":9007199254740993}` {
		t.Fatal("native first page identity/precision changed", err)
	}
	if _, err = cursor.Next(context.Background()); !errors.Is(err, ErrProtocol) || searches != 2 {
		t.Fatal("incomplete page accepted or retried", err)
	}
	if err = client.Close(); err != nil || closed != "latest-pit" || cursor.query != nil || cursor.after != nil || cursor.client != nil {
		t.Fatal("Close lost latest resource or retained cursor cache", err)
	}
	if _, err = cursor.Next(context.Background()); !errors.Is(err, ErrRetired) {
		t.Fatal("closed cursor revived")
	}
}

func TestElasticsearchIncompleteWriteReceiptsRetireOriginalClient(t *testing.T) {
	for _, raw := range []string{
		`{"_index":"own","_id":"one","_seq_no":1,"_primary_term":1,"result":"created","_shards":{"total":2,"successful":1,"failed":1}}`,
		`{"_index":"own","_id":"one","_seq_no":1,"result":"created","_shards":{"total":1,"successful":1,"failed":0}}`,
		`{"_index":"other","_id":"one","_seq_no":1,"_primary_term":1,"result":"created","_shards":{"total":1,"successful":1,"failed":0}}`,
		`{"_index":"own"`,
	} {
		t.Run(fmt.Sprint(len(raw)), func(t *testing.T) {
			client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, raw) })
			result, err := client.CreateDocument(context.Background(), DocumentID{Index: "own", ID: "one"}, json.RawMessage(`{"n":1}`))
			var interrupted *Interrupted
			if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || string(result.Response.Body) != raw || client.available() != ErrRetired {
				t.Fatal("partial or invalid write receipt became success", err)
			}
		})
	}
}

func TestElasticsearchNativeRequestParserKeepsBodiesAndRejectsScriptReplay(t *testing.T) {
	for _, input := range []string{
		"# native comment\nGET /_search\n{\"query\":{\"term\":{\"n\":9007199254740993}}}",
		"PUT /own/_doc/one?if_seq_no=9007199254740993&if_primary_term=7\n{\"n\":12345678901234567890.123456789}",
		"POST /_bulk\n{\"create\":{\"_index\":\"own\",\"_id\":\"one\"}}\n{\"n\":9007199254740993}\n",
	} {
		request, err := ParseRequest(input)
		if err != nil || !strings.Contains(string(request.Body), "9007199254740993") && !strings.Contains(string(request.Body), "12345678901234567890.123456789") {
			t.Fatal("native body precision changed", err)
		}
	}
	for _, input := range []string{"GET /one\nGET /two", "GET https://other/", "POST /_bulk\n{\"index\":{}}", "PUT /one\n{\"n\":1,\"n\":2}", "# comment only", "GET //other/x", "GET /_search\n{}\nPOST /own/_doc/one\n{}"} {
		if _, err := ParseRequest(input); err == nil {
			t.Fatal("ambiguous/foreign/script request accepted")
		}
	}
}

func TestElasticsearchServerWriteFailureDoesNotCertifyRollback(t *testing.T) {
	calls := 0
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(503)
		_, _ = io.WriteString(w, `{"error":{"type":"unavailable_shards_exception"},"status":503}`)
	})
	result, err := client.CreateDocument(context.Background(), DocumentID{Index: "own", ID: "one"}, json.RawMessage(`{"n":1}`))
	var interrupted *Interrupted
	var native *HTTPError
	if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || !errors.As(err, &native) || native.Status != 503 || result.Response.Status != 503 || calls != 1 {
		t.Fatal("server failure became an acknowledged rollback or replay", err)
	}
	if client.available() != ErrRetired {
		t.Fatal("uncertain write left connection usable")
	}
}
