package qdrant

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeWritePointPage(t *testing.T, l *dataLease) *contract.DataPage {
	t.Helper()
	o := dataOptions(l)
	o.Limit = 100
	p, e := l.Runtime().AdvancedLoader(context.Background(), l.object.ProfileID, l.object.Catalog, "points", o)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func nativePointSave(t *testing.T, l *dataLease, edits []contract.DataEdit) *contract.DataEditResult {
	t.Helper()
	r, e := l.Runtime().Saver(context.Background(), l.object.ProfileID, l.object.Catalog, "points", edits)
	if e != nil || r == nil || r.Async == nil || r.CommitConfirmed || r.Applied != 0 {
		t.Fatal("batch admission/completion misrepresented", e)
	}
	return r
}
func nativePointVerify(t *testing.T, l *dataLease, r *contract.DataEditResult) *contract.DataVerification {
	t.Helper()
	v, e := l.Runtime().Verify(context.Background(), l.object.ProfileID, l.object.Catalog, "points", r)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestQdrantNativeDataWritesExactMixedReplacementDeletionAndSnapshots(t *testing.T) {
	s, c := nativeData(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	dataSeed(t, c, name, []any{map[string]any{"id": uint64(9007199254740993), "vector": []float64{1, 0}, "payload": map[string]any{"old": true, "memo": strings.Repeat("猫", 2000)}}, map[string]any{"id": uint64(18446744073709551615), "vector": []float64{0, 1}}})
	l := reservePointData(t, s, name)
	p := nativeWritePointPage(t, l)
	full, e := l.Runtime().RowReader(context.Background(), s.ProfileID(), name, "points", *p.RowRefs[0])
	if e != nil || !reflect.DeepEqual(full.Snapshot, p.RowSnapshots[0]) || len(full.Values["point"].Text) <= 1024 {
		t.Fatal("truncated point full editor snapshot", e)
	}
	legacy, e := l.row(context.Background(), s.ProfileID(), name, "points", *p.RowRefs[1])
	if e != nil {
		t.Fatal(e)
	}
	const uuid = "ABCD0000-0000-0000-0000-000000000007"
	edits := []contract.DataEdit{pointEdit("update", `{"id":9007199254740993,"vector":[0.125,0.25],"payload":{"n":9007199254740993,"nested":{"null":null}}}`, full.Snapshot), pointEdit("delete", "", legacy.Snapshot), pointEdit("insert", `{"id":"`+uuid+`","vector":[1,0],"payload":null}`, nil)}
	result := nativePointSave(t, l, edits)
	if len(l.write.receipt.Operations) != 3 || !l.write.receipt.Completed {
		t.Fatal("native original receipts missing")
	}
	if _, e = l.save(context.Background(), s.ProfileID(), name, "points", edits); !errors.Is(e, contract.ErrDataCommitUncertain) {
		t.Fatal("native batch replayed", e)
	}
	v := nativePointVerify(t, l, result)
	if v.Status != "applied" || len(v.Recovered.Outcomes) != 3 || !v.Recovered.Verified || !v.Recovered.CommitConfirmed || !v.Recovered.RefreshRequired {
		t.Fatal("actual native outcomes", v)
	}
	actual, _, e := c.ReadPoint(context.Background(), name, json.RawMessage(`9007199254740993`))
	if e != nil || strings.Contains(string(actual.Payload), "old") || !strings.Contains(string(actual.Payload), "9007199254740993") {
		t.Fatal("whole point replacement/precision", e)
	}
	actual, _, e = c.ReadPoint(context.Background(), name, json.RawMessage(`"`+uuid+`"`))
	if e != nil || string(actual.Payload) != "{}" || strings.Contains(string(actual.ID), "ABCD") {
		t.Fatal("native uppercase UUID/null payload", actual, e)
	}
	stale := v.Recovered.Outcomes[0].Saved
	dataSeed(t, c, name, []any{map[string]any{"id": uint64(9007199254740993), "vector": []float64{1, 0}}})
	if _, e = l.save(context.Background(), s.ProfileID(), name, "points", []contract.DataEdit{pointEdit("delete", "", stale)}); !errors.Is(e, contract.ErrDataConflict) {
		t.Fatal("stale native snapshot admitted", e)
	}
	p = nativeWritePointPage(t, l)
	dup := []contract.DataEdit{pointEdit("delete", "", p.RowSnapshots[0]), pointEdit("delete", "", p.RowSnapshots[0])}
	if _, e = l.save(context.Background(), s.ProfileID(), name, "points", dup); !errors.Is(e, ErrInvalid) {
		t.Fatal("duplicate identities admitted", e)
	}
	t.Log("Original values/fingerprints, long full edit reads, exact uint64, canonical UUID, whole replacement, mixed insert_only/update_only/delete, explicit receipts and point readback verified")
}
func TestQdrantNativeDataWritesVectorDatatypesNormalizationAndPayloadClearing(t *testing.T) {
	for _, test := range []struct{ name, config, vector string }{
		{"float32_cosine", `{"vectors":{"size":2,"distance":"Cosine"}}`, `[3,4]`},
		{"float16_dot", `{"vectors":{"size":2,"distance":"Dot","datatype":"float16"}}`, `[1.0003,-0.3333]`},
		{"float16_cosine", `{"vectors":{"size":2,"distance":"Cosine","datatype":"float16"}}`, `[3,4]`},
		{"uint8_dot", `{"vectors":{"size":2,"distance":"Dot","datatype":"uint8"}}`, `[-1,300.5]`},
		{"uint8_cosine", `{"vectors":{"size":2,"distance":"Cosine","datatype":"uint8"}}`, `[3.7,4.9]`},
		{"named_multi_sparse", `{"vectors":{"dense":{"size":2,"distance":"Cosine"},"multi":{"size":2,"distance":"Dot","datatype":"float16","multivector_config":{"comparator":"max_sim"}}},"sparse_vectors":{"sparse":{"index":{"datatype":"float16"}}}}`, `{"dense":[3,4],"multi":[[1.0003,-0.3333],[1,0]],"sparse":{"indices":[17,2],"values":[0.123456789,0.5]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, c := nativeData(t)
			name := nativeCollection(t, c, test.config)
			l := reservePointData(t, s, name)
			r := nativePointSave(t, l, []contract.DataEdit{pointEdit("insert", `{"id":1,"vector":`+test.vector+`,"payload":{"decimal":0.10000000000000001,"u64":18446744073709551615,"i64":-9223372036854775808}}`, nil)})
			v := nativePointVerify(t, l, r)
			if v.Status != "applied" {
				t.Fatal("native datatype verification", v.Recovered.Async.TaskJSON)
			}
			replacement := test.vector
			if test.name == "named_multi_sparse" {
				replacement = `{"dense":[1,0]}`
			}
			r = nativePointSave(t, l, []contract.DataEdit{pointEdit("update", `{"id":1,"vector":`+replacement+`}`, v.Recovered.Outcomes[0].Saved)})
			v = nativePointVerify(t, l, r)
			if v.Status != "applied" {
				t.Fatal("omitted payload/named vector whole replacement", v.Recovered.Async.TaskJSON)
			}
			p, _, e := c.ReadPoint(context.Background(), name, []byte(`1`))
			if e != nil || string(p.Payload) != "{}" || test.name == "named_multi_sparse" && strings.Contains(string(p.Vector), "multi") {
				t.Fatal("old point fields survived", p, e)
			}
		})
	}
}

func nativeDataWriteRelay(t *testing.T, admin *Client, hook func(http.ResponseWriter, *http.Request, Response) bool, before func(*http.Request)) connections.QdrantConfig {
	t.Helper()
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
		if before != nil {
			before(r)
		}
		resp, e := admin.Do(r.Context(), Request{Method: r.Method, Path: strings.TrimPrefix(r.URL.RequestURI(), "/gateway/qdrant"), Body: raw})
		if e != nil && resp.Status == 0 {
			w.WriteHeader(502)
			return
		}
		if hook != nil && hook(w, r, resp) {
			return
		}
		w.Header().Set("Content-Type", resp.ContentType)
		w.WriteHeader(resp.Status)
		w.Write(resp.Body)
	}))
	t.Cleanup(relay.Close)
	ca := filepath.Join(t.TempDir(), "relay.pem")
	if e := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	return connections.QdrantConfig{Endpoint: relay.URL + "/gateway/qdrant", CAFile: ca, ServerName: "example.com"}
}
func TestQdrantNativeDataWritesCompletedBatchSkippedUpdateRetainsDrafts(t *testing.T) {
	_, c := nativeData(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	dataSeed(t, c, name, []any{map[string]any{"id": 1, "vector": []float64{1, 0}}})
	var writes atomic.Int32
	cfg := nativeDataWriteRelay(t, c, nil, func(r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points/batch") {
			writes.Add(1)
			nativeRun(t, c, Request{Method: "POST", Path: "/collections/" + name + "/points/delete?wait=true", Body: []byte(`{"points":[1]}`)})
		}
	})
	s := testSession(t, cfg, "")
	l := reservePointData(t, s, name)
	p := nativeWritePointPage(t, l)
	edits := []contract.DataEdit{pointEdit("insert", `{"id":2,"vector":[1,0]}`, nil), pointEdit("update", `{"id":1,"vector":[0,1]}`, p.RowSnapshots[0])}
	result := nativePointSave(t, l, edits)
	if !l.write.receipt.Completed {
		t.Fatal("native mixed batch not completed")
	}
	v := nativePointVerify(t, l, result)
	if v.Status != "indeterminate" || !reflect.DeepEqual(v.States, []string{"matched", "missing"}) || v.Recovered.CommitConfirmed {
		t.Fatal("completed skipped update claimed applied", v)
	}
	if _, e := l.save(context.Background(), s.ProfileID(), name, "points", edits); !errors.Is(e, contract.ErrDataCommitUncertain) || writes.Load() != 1 {
		t.Fatal("partial batch replayed", e)
	}
	t.Log("Native insert applied, raced update_only skipped, receipt completed; explicit readback retained unresolved draft and prohibited replay")
}
func TestQdrantNativeDataWritesLostCanceledAndCloseNeverReplays(t *testing.T) {
	for _, fault := range []string{"lost", "canceled", "close", "session_close"} {
		t.Run(fault, func(t *testing.T) {
			_, c := nativeData(t)
			name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
			observed, acked := make(chan struct{}), make(chan struct{})
			var writes atomic.Int32
			cfg := nativeDataWriteRelay(t, c, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if !strings.HasSuffix(r.URL.Path, "/points/batch") {
					return false
				}
				writes.Add(1)
				close(observed)
				if fault == "lost" {
					conn, _, e := w.(http.Hijacker).Hijack()
					if e != nil {
						t.Error(e)
					} else {
						conn.Close()
					}
				} else {
					<-r.Context().Done()
				}
				close(acked)
				return true
			}, nil)
			s := testSession(t, cfg, "")
			l := reservePointData(t, s, name)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			edits := []contract.DataEdit{pointEdit("insert", `{"id":1,"vector":[1,0]}`, nil)}
			type saved struct {
				r *contract.DataEditResult
				e error
			}
			result := make(chan saved, 1)
			go func() { r, e := l.save(ctx, s.ProfileID(), name, "points", edits); result <- saved{r, e} }()
			waitSessionSignal(t, observed)
			switch fault {
			case "canceled":
				cancel()
			case "close":
				l.Close()
			case "session_close":
				s.Close()
			}
			var got saved
			select {
			case got = <-result:
			case <-time.After(10 * time.Second):
				t.Fatal("original writer did not acknowledge cancellation")
			}
			waitSessionSignal(t, acked)
			if got.e == nil || got.r == nil || got.r.Async == nil || !got.r.CommitUncertain || got.r.CommitConfirmed || got.r.Async.Status != "interrupted" {
				t.Fatal("unknown write outcome discarded", got.e)
			}
			if fault == "lost" || fault == "canceled" {
				v := nativePointVerify(t, l, got.r)
				if v.Status != "indeterminate" || v.States[0] != "matched" {
					t.Fatal("unknown receipt became commit success", v)
				}
				if _, e := l.save(context.Background(), s.ProfileID(), name, "points", edits); !errors.Is(e, contract.ErrDataCommitUncertain) {
					t.Fatal("unknown write replayed", e)
				}
			}
			if writes.Load() != 1 {
				t.Fatal("native request repeated")
			}
			if n, _, e := c.Count(context.Background(), name, nil); e != nil || n != 1 {
				t.Fatal("server mutation evidence", n, e)
			}
		})
	}
}
