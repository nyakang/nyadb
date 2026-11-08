package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativePointBatch(t *testing.T, collection string, writes ...PointWrite) PointBatch {
	t.Helper()
	b, err := PreparePointBatch(collection, writes)
	if err != nil {
		t.Fatal("pure native batch preparation", err)
	}
	return b
}
func nativeBatchComplete(t *testing.T, c *Client, b PointBatch) PointBatchReceipt {
	t.Helper()
	r, response, err := c.SubmitPointBatch(context.Background(), b)
	if err != nil || response.Status != 200 || !r.Completed || len(r.Operations) != b.OperationCount() || !bytes.Equal(r.Raw, response.Body) {
		t.Fatal("native batch completion", err, string(response.Body))
	}
	for _, operation := range r.Operations {
		if operation.Collection != b.Collection() || operation.Status != "completed" {
			t.Fatal("original native operation receipt lost")
		}
	}
	return r
}

func TestQdrantNativePointBatchExactModesNativeFiltersAndMixedOperations(t *testing.T) {
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	points := []json.RawMessage{
		[]byte(`{"id":0,"vector":[1,0],"payload":{"n":9007199254740993,"group":"original"}}`),
		[]byte(`{"id":9007199254740993,"vector":[0,1],"payload":{"n":9007199254740993,"text":"猫🐱"}}`),
		[]byte(`{"id":18446744073709551615,"vector":[1,1],"payload":{"n":9007199254740993,"nested":{"null":null}}}`),
		[]byte(`{"id":"550e8400-e29b-41d4-a716-446655440000","vector":[0.25,0.75],"payload":null}`),
	}
	nativeBatchComplete(t, c, nativePointBatch(t, name, PointWrite{Action: "upsert", Mode: "insert_only", Points: points}))
	page, _, err := c.Scroll(context.Background(), name, nil, 10, nil)
	if err != nil || len(page.Points) != 4 {
		t.Fatal("mixed exact native IDs lost", err)
	}
	for _, p := range page.Points {
		// The UUID point intentionally has no payload.
		if string(p.ID) != `"550e8400-e29b-41d4-a716-446655440000"` && !bytes.Contains(p.Payload, []byte("9007199254740993")) {
			t.Fatal("native batch rounded payload")
		}
	}
	replacement := json.RawMessage(`{"id":0,"vector":[0,1],"payload":{"n":9007199254740995,"group":"updated"}}`)
	nativeBatchComplete(t, c, nativePointBatch(t, name, PointWrite{Action: "upsert", Mode: "insert_only", Points: []json.RawMessage{replacement}}))
	original, _, err := c.ReadPoint(context.Background(), name, []byte("0"))
	if err != nil || !bytes.Contains(original.Payload, []byte("9007199254740993")) {
		t.Fatal("insert_only overwrote existing point", err)
	}
	nativeBatchComplete(t, c, nativePointBatch(t, name, PointWrite{Action: "upsert", Mode: "update_only", Points: []json.RawMessage{[]byte(`{"id":99999,"vector":[1,0]}`)}}))
	if _, response, err := c.ReadPoint(context.Background(), name, []byte("99999")); err == nil || response.Status != 404 {
		t.Fatal("update_only resurrected missing point", err)
	}
	miss := json.RawMessage(`{"must":[{"key":"n","match":{"value":9007199254740995}}]}`)
	nativeBatchComplete(t, c, nativePointBatch(t, name, PointWrite{Action: "upsert", Mode: "update_only", Points: []json.RawMessage{replacement}, Filter: miss}))
	original, _, err = c.ReadPoint(context.Background(), name, []byte("0"))
	if err != nil || !bytes.Contains(original.Payload, []byte("9007199254740993")) {
		t.Fatal("nonmatching native update_filter applied", err)
	}
	hit := json.RawMessage(`{"must":[{"key":"n","match":{"value":9007199254740993}}]}`)
	nativeBatchComplete(t, c, nativePointBatch(t, name, PointWrite{Action: "upsert", Mode: "update_only", Points: []json.RawMessage{replacement}, Filter: hit}))
	changed, _, err := c.ReadPoint(context.Background(), name, []byte("0"))
	if err != nil || !bytes.Contains(changed.Payload, []byte("9007199254740995")) {
		t.Fatal("matching native filter did not update", err)
	}
	b := nativePointBatch(t, name,
		PointWrite{Action: "upsert", Mode: "insert_only", Points: []json.RawMessage{[]byte(`{"id":77,"vector":[1,0],"payload":{"group":"delete-this"}}`)}},
		PointWrite{Action: "upsert", Mode: "update_only", Points: []json.RawMessage{[]byte(`{"id":18446744073709551615,"vector":[1,0],"payload":{"text":"mixed"}}`)}},
		PointWrite{Action: "delete", IDs: []json.RawMessage{[]byte(`"550e8400-e29b-41d4-a716-446655440000"`)}},
	)
	nativeBatchComplete(t, c, b)
	if _, response, err := c.ReadPoint(context.Background(), name, []byte(`"550e8400-e29b-41d4-a716-446655440000"`)); err == nil || response.Status != 404 {
		t.Fatal("exact UUID deletion failed", err)
	}
	changed, _, err = c.ReadPoint(context.Background(), name, []byte("18446744073709551615"))
	if err != nil || !bytes.Contains(changed.Payload, []byte(`"mixed"`)) {
		t.Fatal("mixed update lost MaxUint64", err)
	}
	nativeBatchComplete(t, c, nativePointBatch(t, name, PointWrite{Action: "delete", Filter: []byte(`{"must":[{"key":"group","match":{"value":"delete-this"}}]}`)}))
	if count, _, err := c.Count(context.Background(), name, nil); err != nil || count != 3 {
		t.Fatal("native filtered delete scope", count, err)
	}
}

func TestQdrantNativePointBatchNormalizationAndNativePartialFailure(t *testing.T) {
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"dense":{"size":2,"distance":"Cosine"},"multi":{"size":2,"distance":"Dot","multivector_config":{"comparator":"max_sim"}}},"sparse_vectors":{"sparse":{}}}`)
	point := json.RawMessage(`{"id":1,"vector":{"dense":[3,4],"multi":[[0.75,0.25],[0.25,0.75]],"sparse":{"indices":[2,8],"values":[0.25,0.5]}},"payload":{"n":9007199254740993,"text":"native"}}`)
	b := nativePointBatch(t, name, PointWrite{Action: "upsert", Mode: "upsert", Points: []json.RawMessage{point}})
	nativeBatchComplete(t, c, b)
	saved, _, err := c.ReadPoint(context.Background(), name, []byte("1"))
	if err != nil || !bytes.Contains(saved.Vector, []byte(`"dense":[0.6,0.8]`)) || !bytes.Contains(saved.Vector, []byte(`"multi"`)) || !bytes.Contains(saved.Vector, []byte(`"sparse"`)) || !bytes.Contains(saved.Payload, []byte("9007199254740993")) || !bytes.Contains(b.Preview().Body, point) {
		t.Fatal("native normalization or original point spelling lost", err, string(saved.JSON))
	}
	// Native batch operations apply one by one. A later schema failure is not
	// proof of rollback of the earlier completed operation.
	plain := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	partial := nativePointBatch(t, plain,
		PointWrite{Action: "upsert", Mode: "insert_only", Points: []json.RawMessage{[]byte(`{"id":3,"vector":[1,0],"payload":{"before-error":true}}`)}},
		PointWrite{Action: "upsert", Mode: "insert_only", Points: []json.RawMessage{[]byte(`{"id":4,"vector":[1,0,0]}`)}},
	)
	receipt, response, err := c.SubmitPointBatch(context.Background(), partial)
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || response.Status != 400 || receipt.Completed || !bytes.Equal(receipt.Raw, response.Body) {
		t.Fatal("native partial batch failure claimed rollback", err, string(response.Body))
	}
	reader := nativeOpen(t)
	saved, _, err = reader.ReadPoint(context.Background(), plain, []byte("3"))
	if err != nil || !bytes.Contains(saved.Payload, []byte("before-error")) {
		t.Fatal("first applied operation missing after native HTTP failure", err)
	}
	if _, _, err = c.SubmitPointBatch(context.Background(), partial); !errors.Is(err, ErrRetired) {
		t.Fatal("partial batch allowed replay on retired original client", err)
	}
	if _, response, err = reader.ReadPoint(context.Background(), plain, []byte("4")); err == nil || response.Status != 404 {
		t.Fatal("invalid-dimension operation unexpectedly applied", err)
	}
}

func TestQdrantNativePointBatchReadOnlyDenial(t *testing.T) {
	admin := nativeOpen(t)
	name := nativeCollection(t, admin, `{"vectors":{"size":2,"distance":"Dot"}}`)
	cfg, _, readonly := nativeFixture(t)
	b := nativePointBatch(t, name, PointWrite{Action: "upsert", Mode: "insert_only", Points: []json.RawMessage{[]byte(`{"id":1,"vector":[1,0]}`)}})
	if cfg.Auth != "api_key" {
		nativeBatchComplete(t, admin, b)
		return
	}
	reader, err := Open(context.Background(), cfg, readonly)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	receipt, response, err := reader.SubmitPointBatch(context.Background(), b)
	if err == nil || response.Status != 403 || receipt.Completed || len(response.Body) == 0 || !bytes.Equal(receipt.Raw, response.Body) {
		t.Fatal("native readonly batch denial lost", err)
	}
	if n, _, err := admin.Count(context.Background(), name, nil); err != nil || n != 0 {
		t.Fatal("denied batch changed original collection", n, err)
	}
}

func TestQdrantNativePointBatchLostCanceledAndOriginalCloseNeverReplays(t *testing.T) {
	admin := nativeOpen(t)
	for _, fault := range []string{"lost", "canceled", "close"} {
		t.Run(fault, func(t *testing.T) {
			name := nativeCollection(t, admin, `{"vectors":{"size":2,"distance":"Dot"}}`)
			var dispatched atomic.Int32
			observed, acked := make(chan struct{}), make(chan struct{})
			relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
				response, err := admin.Do(r.Context(), Request{Method: r.Method, Path: strings.TrimPrefix(r.URL.RequestURI(), "/gateway/qdrant"), Body: raw})
				if err != nil && response.Status == 0 {
					w.WriteHeader(502)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/points/batch") {
					dispatched.Add(1)
					close(observed)
					if fault == "lost" {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
						} else {
							conn.Close()
						}
					} else {
						<-r.Context().Done()
					}
					close(acked)
					return
				}
				w.Header().Set("Content-Type", response.ContentType)
				w.WriteHeader(response.Status)
				w.Write(response.Body)
			}))
			defer relay.Close()
			ca := filepath.Join(t.TempDir(), "relay.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Open(context.Background(), connections.QdrantConfig{Endpoint: relay.URL + "/gateway/qdrant", CAFile: ca, ServerName: "example.com"}, "")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			b := nativePointBatch(t, name, PointWrite{Action: "upsert", Mode: "insert_only", Points: []json.RawMessage{[]byte(`{"id":9007199254740993,"vector":[1,0],"payload":{"n":9007199254740993}}`)}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				r, _, err := c.SubmitPointBatch(ctx, b)
				if r.Completed {
					t.Error("lost response claimed completed batch")
				}
				result <- err
			}()
			waitSessionSignal(t, observed)
			if fault == "canceled" {
				cancel()
			}
			if fault == "close" {
				c.Close()
			}
			select {
			case err := <-result:
				var interrupted *Interrupted
				if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown {
					t.Fatal("lost native write became a safe failure", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("captured original batch executor did not settle")
			}
			waitSessionSignal(t, acked)
			if _, _, err = c.SubmitPointBatch(context.Background(), b); !errors.Is(err, ErrRetired) || dispatched.Load() != 1 {
				t.Fatal("interrupted native batch replayed", err)
			}
			p, _, err := admin.ReadPoint(context.Background(), name, []byte("9007199254740993"))
			if err != nil || !bytes.Contains(p.Payload, []byte("9007199254740993")) {
				t.Fatal("independent original read did not prove the applied write", err)
			}
		})
	}
}
