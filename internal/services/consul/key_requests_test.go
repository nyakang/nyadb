package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
)

func TestNoncanonicalKeysKeepOriginalTransactionIdentityAndNativeRejection(t *testing.T) {
	for _, key := range []string{"original//key", "original/", "/original", "original/../other", "original/./other"} {
		t.Run(key, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/txn" {
					t.Error("noncanonical key reached a cleaning HTTP path")
					return
				}
				var ops []TxnOperation
				if json.NewDecoder(r.Body).Decode(&ops) != nil || len(ops) != 1 || ops[0].KV.Key != key || !bytes.Equal(*ops[0].KV.Value, []byte{0, 255}) {
					t.Error("original JSON key or bytes changed")
				}
				w.WriteHeader(400)
				w.Write([]byte("original key rejected"))
			})
			if _, err := KeyPath(key); !errors.Is(err, ErrUnsupported) {
				t.Fatal("cleaning path accepted")
			}
			r, err := KeyPutRequest(key, []byte{0, 255}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.Do(context.Background(), r)
			if err == nil || !result.Acknowledged || result.Succeeded || result.WriteOutcomeUnknown || !bytes.Equal(result.Body, []byte("original key rejected")) {
				t.Fatal("original explicit rejection was lost")
			}
		})
	}
}

func TestExactKeysAndPrefixesUseReadOnlyTransactionsAndPreserveOperations(t *testing.T) {
	key := "雪//key/"
	read, err := KeyReadRequest(key)
	if err != nil {
		t.Fatal(err)
	}
	list, err := KeyListRequest(key, true)
	if err != nil {
		t.Fatal(err)
	}
	put, err := KeyPutRequest(key, nil, u64(0), u64(0))
	if err != nil {
		t.Fatal(err)
	}
	del, err := KeyDeleteRequest(key, u64(0), false)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := KeyDeleteRequest(key, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("preparation sent HTTP") })
	for i, r := range []Request{read, list, put, del, tree} {
		p, err := c.Prepare(r)
		if err != nil || p.Writes() != (i >= 2) {
			t.Fatal("native read/write semantics changed", err)
		}
		var ops []TxnOperation
		if json.Unmarshal(p.Snapshot().Body, &ops) != nil || len(ops) != 1 || ops[0].KV.Key != key || ops[0].KV.Verb != []string{"get", "get-tree", "cas", "delete-cas", "delete-tree"}[i] {
			t.Fatal("operation changed")
		}
		if i == 2 && (ops[0].KV.Value == nil || len(*ops[0].KV.Value) != 0 || ops[0].KV.Flags == nil || *ops[0].KV.Flags != 0) {
			t.Fatal("explicit empty bytes or zero flags became absent")
		}
	}
}

func TestSessionInputsAndScopeOverridesFailBeforeHTTP(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid request sent HTTP") })
	for _, body := range []string{"null", `{"Name":null}`, `{"TTL":"9s"}`, `{"LockDelay":"61s"}`, `{"NodeChecks":null}`, `{"TTL":"10s","TTL":"20s"}`, `{"Behavior":"foreign"}`, `{"Secret":"private"}`} {
		if _, err := c.Prepare(Request{Method: "PUT", Path: "/v1/session/create", Body: []byte(body)}); err == nil {
			t.Fatal("invalid session plan prepared")
		}
	}
	for _, field := range []string{"token", "dc", "ns", "partition"} {
		if _, err := c.Prepare(Request{Method: "GET", Path: "/v1/catalog/services", Query: url.Values{field: {"foreign"}}}); err == nil {
			t.Fatal("request changed original scope")
		}
	}
}

func TestAmbiguousHTTPTimeoutKeepsOriginalWriteBarrier(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
		w.Write([]byte("original request timed out after dispatch"))
	})
	r, err := c.Do(context.Background(), putRequest("key", []byte{0, 255}))
	if err == nil || !r.Captured || r.Acknowledged || !r.WriteOutcomeUnknown {
		t.Fatal("timeout became a known rejection")
	}
	if _, err := c.Prepare(putRequest("key", nil)); !errors.Is(err, ErrWriteUnknown) {
		t.Fatal("ambiguous timeout permitted replay")
	}
}
