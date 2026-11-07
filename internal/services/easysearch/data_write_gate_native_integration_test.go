package easysearch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEasysearchNativeWriteParameterGates(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	c := sessionNativeOpen(t, ctx)
	index := ownedIndex(t, c, ctx)
	alias := "NyaDB_" + index
	body, _ := json.Marshal(map[string]any{"actions": []any{map[string]any{"add": map[string]any{"index": index, "alias": alias, "is_write_index": true, "index_routing": "original"}}}})
	sessionNativeRun(t, c, ctx, "POST", "/_aliases", body)
	report := map[string]any{"version": c.Identity().Version.Number, "cluster_uuid": c.Identity().ClusterUUID}
	r, e := c.Do(ctx, Request{Method: "PUT", Path: "/" + index + "/_create/rejected?require_alias=true", Body: []byte(`{"name":"original"}`)})
	var native *HTTPError
	if !errors.As(e, &native) || (native.Status != 400 && native.Status != 404) || !strings.Contains(string(r.Body), "require_alias") || strings.Contains(string(r.Body), "unrecognized parameter") {
		t.Fatal("native require_alias did not protect concrete-index writes", e)
	}
	if _, e = c.GetDocument(ctx, DocumentID{Index: index, ID: "rejected"}); !errors.As(e, &native) || native.Status != 404 {
		t.Fatal("rejected index write had effects", e)
	}
	r = sessionNativeRun(t, c, ctx, "PUT", "/"+alias+"/_create/alias_gate?require_alias=true&refresh=wait_for", []byte(`{"name":"original"}`))
	var receipt struct {
		Index  string `json:"_index"`
		Result string `json:"result"`
	}
	if decodeJSON(r.Body, &receipt) != nil || receipt.Index != index || receipt.Result != "created" {
		t.Fatal("native alias gate did not resolve original concrete index")
	}
	if _, e = c.GetDocument(ctx, DocumentID{Index: index, ID: "alias_gate", Routing: "original"}); e != nil {
		t.Fatal("native alias write routing changed", e)
	}
	report["require_alias"] = "verified_native_rejection_and_case_sensitive_alias_create"
	report["require_alias_concrete_status"] = 404
	stream, template := index+"_gate_stream", index+"_gate_template"
	sessionNativeRun(t, c, ctx, "PUT", "/_index_template/"+template, []byte(`{"index_patterns":["`+stream+`"],"data_stream":{},"template":{"settings":{"number_of_shards":1,"number_of_replicas":0}}}`))
	sessionNativeRun(t, c, ctx, "PUT", "/_data_stream/"+stream, nil)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = c.Do(cleanup, Request{Method: "DELETE", Path: "/_data_stream/" + stream})
		_, _ = c.Do(cleanup, Request{Method: "DELETE", Path: "/_index_template/" + template})
	})
	source := []byte(`{"@timestamp":"2026-11-08T00:00:00Z","name":"original"}`)
	r, e = c.Do(ctx, Request{Method: "PUT", Path: "/" + stream + "/_create/stream_gate?require_data_stream=true&refresh=wait_for", Body: source})
	if e == nil {
		if decodeJSON(r.Body, &receipt) != nil || !strings.HasPrefix(receipt.Index, ".ds-"+stream) || receipt.Result != "created" {
			t.Fatal("native stream gate receipt incomplete")
		}
		report["require_data_stream"] = "verified_native_stream_create"
	} else {
		if !errors.As(e, &native) || (native.Status != 400 && native.Status != 404) || !strings.Contains(string(r.Body), "unrecognized parameter") || !strings.Contains(string(r.Body), "require_data_stream") {
			t.Fatal("stream flag failed without proven unsupported parameter", e)
		}
		report["require_data_stream"] = "native_parameter_unsupported"
		report["require_data_stream_rejection"] = json.RawMessage(r.Body)
		r = sessionNativeRun(t, c, ctx, "PUT", "/"+stream+"/_create/stream_gate?refresh=wait_for", source)
		if decodeJSON(r.Body, &receipt) != nil || !strings.HasPrefix(receipt.Index, ".ds-"+stream) || receipt.Result != "created" {
			t.Fatal("native stream create did not resolve original backing index")
		}
	}
	report["native_stream_create"] = "verified_original_backing_receipt"
	t.Log("Native write gates:", report["require_alias"], report["require_data_stream"])
	if path := os.Getenv("NYADB_EASYSEARCH_WRITE_GATE_REPORT"); path != "" {
		raw, _ := json.MarshalIndent(report, "", "  ")
		if e = os.WriteFile(path, append(raw, '\n'), 0o600); e != nil {
			t.Fatal(e)
		}
	}
}
