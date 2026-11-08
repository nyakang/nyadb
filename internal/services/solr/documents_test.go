package solr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeRef() NativeDocumentRef {
	return NativeDocumentRef{Member: "owned", UniqueKey: "id", Key: contract.Cell{Kind: contract.Text, Text: "native🐱"}}
}

func documentClient(t *testing.T, cloud bool, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/solr/admin/info/system" {
			identity := testIdentity
			if cloud {
				identity = strings.Replace(identity, `"std"`, `"solrcloud"`, 1)
			}
			_, _ = io.WriteString(w, identity)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	c, err := Open(context.Background(), tlsConfig(t, server), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestSolrNativeDocumentCodecExactAtomicFieldsAndNoCommit(t *testing.T) {
	c := &Client{identity: Identity{Mode: "std"}}
	ref := nativeRef()
	r, err := c.nativeMutationRequest(NativeDocumentMutation{Kind: "update", Ref: ref, Version: "9007199254740993", Fields: map[string]json.RawMessage{"exact_l": []byte(`9007199254740993`), "text_s": []byte(`"Unicode🐱"`), "removed_s": []byte(`null`)}})
	if err != nil || !strings.Contains(string(r.Body), `"set":9007199254740993`) || !strings.Contains(string(r.Body), `"set":null`) || strings.Contains(r.Path, "commit") {
		t.Fatal("native atomic codec", r, err)
	}
	var docs []map[string]json.RawMessage
	if decodeJSON(r.Body, &docs) != nil || len(docs) != 1 || string(docs[0]["id"]) != `"native🐱"` || string(docs[0]["_version_"]) != "9007199254740993" {
		t.Fatal("typed key/version changed")
	}
	for _, m := range []NativeDocumentMutation{
		{Kind: "create", Ref: ref, Version: "0", Fields: map[string]json.RawMessage{"text_s": []byte(`"a"`)}},
		{Kind: "update", Ref: ref, Version: "1", Fields: map[string]json.RawMessage{"text_s": []byte(`"a"`)}},
		{Kind: "update", Ref: ref, Version: "4", Fields: map[string]json.RawMessage{"id": []byte(`"other"`)}},
		{Kind: "update", Ref: ref, Version: "4", Fields: map[string]json.RawMessage{"nested": []byte(`{"set":"x"}`)}},
		{Kind: "update", Ref: ref, Version: "4", Fields: map[string]json.RawMessage{"_root_": []byte(`"other"`)}},
		{Kind: "update", Ref: ref, Version: "4", Fields: map[string]json.RawMessage{"text_s": []byte(`"a" "b"`)}},
		{Kind: "create", Ref: ref, Version: "-1", Fields: map[string]json.RawMessage{"text_s": []byte(`"` + strings.Repeat("a", 1<<20) + `"`)}},
		{Kind: "create", Ref: ref, Version: "-1", Fields: map[string]json.RawMessage{"one_s": []byte(`"` + strings.Repeat("a", 600000) + `"`), "two_s": []byte(`"` + strings.Repeat("a", 600000) + `"`)}},
		{Kind: "delete", Ref: ref, Version: "4", Fields: map[string]json.RawMessage{"text_s": []byte(`"a"`)}},
	} {
		if _, err = c.nativeMutationRequest(m); err == nil {
			t.Fatal("unsafe or unbounded native mutation encoded", m.Kind)
		}
	}
	cloud := &Client{identity: Identity{Mode: "solrcloud"}}
	ref.Router, ref.Shard = "compositeId", "shard2"
	r, err = cloud.nativeMutationRequest(NativeDocumentMutation{Kind: "update", Ref: ref, Version: "4", Fields: map[string]json.RawMessage{"text_s": []byte(`"a"`)}})
	if err != nil || strings.Contains(r.Path, "_route_") || strings.Contains(r.Path, "shards=") {
		t.Fatal("compositeId observation turned into false add routing", err)
	}
	ref.Router = "implicit"
	r, err = cloud.nativeMutationRequest(NativeDocumentMutation{Kind: "delete", Ref: ref, Version: "4"})
	if err != nil || !strings.Contains(r.Path, "_route_=shard2") {
		t.Fatal("implicit original shard not routed", err)
	}
}

func TestSolrNativeDocumentReceiptsRetainExactVersionAndDistributedZero(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		c := documentClient(t, cloud, func(w http.ResponseWriter, r *http.Request) {
			version := "9007199254740993"
			if cloud {
				version = "0"
			}
			_, _ = io.WriteString(w, `{"responseHeader":{"status":0},"adds":["native🐱",`+version+`]}`)
		})
		ref := nativeRef()
		if cloud {
			ref.Router = "compositeId"
		}
		receipt, err := c.MutateNativeDocument(context.Background(), NativeDocumentMutation{Kind: "create", Ref: ref, Version: "-1", Fields: map[string]json.RawMessage{"text_s": []byte(`"value"`)}})
		if err != nil || receipt.State != "acknowledged" || cloud && receipt.Version != "" || !cloud && receipt.Version != "9007199254740993" || len(receipt.Response.Body) == 0 {
			t.Fatal("native receipt", receipt, err)
		}
	}
}

func TestSolrNativeDocumentMalformedAndHTTPReceiptsNeverReplay(t *testing.T) {
	for _, body := range []string{`{"responseHeader":{"status":0}}`, `{"responseHeader":{"status":0},"adds":["other",9]}`, `{"responseHeader":{"status":0},"adds":["native🐱",0]}`, `{"responseHeader":{"status":0},"adds":["native🐱","9"]}`, `{"responseHeader":{"status":0,"partialResults":true},"adds":["native🐱",9]}`, `{"responseHeader":{"status":0},"adds":["native🐱",9],"deletes":["native🐱",-9]}`, `{"error":{"code":409}}`} {
		var writes atomic.Int32
		c := documentClient(t, false, func(w http.ResponseWriter, r *http.Request) {
			writes.Add(1)
			if strings.Contains(body, "409") {
				w.WriteHeader(409)
			}
			_, _ = io.WriteString(w, body)
		})
		m := NativeDocumentMutation{Kind: "update", Ref: nativeRef(), Version: "8", Fields: map[string]json.RawMessage{"text_s": []byte(`"value"`)}}
		receipt, err := c.MutateNativeDocument(context.Background(), m)
		if !errors.Is(err, ErrDocumentMutationUncertain) || receipt == nil || receipt.State != "uncertain" || string(receipt.Response.Body) != body {
			t.Fatal("incomplete receipt lost", receipt, err)
		}
		if _, err = c.MutateNativeDocument(context.Background(), m); !errors.Is(err, ErrRetired) || writes.Load() != 1 {
			t.Fatal("uncertain original client replayed", err)
		}
	}
}

func TestSolrNativeDocumentCloudDeleteEchoIsNotAMutationClock(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		c := documentClient(t, cloud, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"responseHeader":{"status":0},"deletes":["native🐱",9007199254740993]}`)
		})
		ref := nativeRef()
		if cloud {
			ref.Router = "compositeId"
		}
		receipt, err := c.MutateNativeDocument(context.Background(), NativeDocumentMutation{Kind: "delete", Ref: ref, Version: "9007199254740993"})
		if cloud {
			if err != nil || receipt.Version != "" || receipt.ReportedVersion != "9007199254740993" || receipt.State != "acknowledged" {
				t.Fatal("forwarded precondition became a delete clock", receipt, err)
			}
		} else if !errors.Is(err, ErrDocumentMutationUncertain) {
			t.Fatal("standalone invalid positive delete version accepted", err)
		}
	}
}

func TestSolrNativeDocumentRTGExactTypedProjectionAndMissingDocument(t *testing.T) {
	var missing atomic.Bool
	c := documentClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Query().Get("id") != "native🐱" || r.URL.Query().Get("distrib") != "false" || r.URL.Query().Get("omitHeader") != "false" {
			t.Error("RTG original scope", r.URL)
		}
		if missing.Load() {
			_, _ = io.WriteString(w, `{"responseHeader":{"status":0},"doc":null}`)
		} else {
			_, _ = io.WriteString(w, `{"responseHeader":{"status":0},"doc":{"id":"native🐱","_version_":9007199254740993,"exact_l":9007199254740993}}`)
		}
	})
	doc, err := c.ReadNativeDocument(context.Background(), nativeRef())
	if err != nil || doc.Version != "9007199254740993" || !strings.Contains(string(doc.Projection), "9007199254740993") {
		t.Fatal("RTG exact projection", doc, err)
	}
	missing.Store(true)
	if _, err = c.ReadNativeDocument(context.Background(), nativeRef()); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatal("missing document became fabricated source", err)
	}
}

func TestSolrNativeDocumentCancelKeepsCapturedBytesAndAcknowledgesOwnWorker(t *testing.T) {
	entered := make(chan []byte, 1)
	returned := make(chan struct{})
	c := documentClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		entered <- raw
		<-r.Context().Done()
		close(returned)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	value := []byte(`"captured"`)
	done := make(chan error, 1)
	go func() {
		receipt, err := c.MutateNativeDocument(ctx, NativeDocumentMutation{Kind: "create", Ref: nativeRef(), Version: "-1", Fields: map[string]json.RawMessage{"text_s": value}})
		if receipt == nil || receipt.State != "uncertain" {
			err = errors.New("cancel lost original mutation receipt")
		}
		done <- err
	}()
	select {
	case raw := <-entered:
		if !strings.Contains(string(raw), "captured") {
			t.Fatal("original fields changed before dispatch")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native worker did not dispatch")
	}
	copy(value, []byte(`"modified"`))
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrDocumentMutationUncertain) || !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled typed request", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not acknowledge original local executor")
	}
	c.Close()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture server did not observe original cancellation")
	}
}
