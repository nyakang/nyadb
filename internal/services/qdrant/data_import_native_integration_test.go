package qdrant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeImportPoints(t *testing.T, l *dataLease, r contract.DataImportRequest) *contract.DataEditResult {
	t.Helper()
	result, e := l.Runtime().Importer(context.Background(), l.object.ProfileID, l.object.Catalog, "points", r)
	if e != nil || result == nil || result.Saved == nil || result.Saved.Async == nil || result.Saved.CommitConfirmed {
		t.Fatal("native original import", e)
	}
	return result.Saved
}
func TestQdrantNativeDataImportExactModesLargeSourceNativeRetrievalAndReplacement(t *testing.T) {
	s, c := nativeData(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Cosine"}}`)
	l := reservePointData(t, s, name)
	request := importRequest("insert_only", `{"id":9007199254740993,"vector":[3,4],"payload":{"n":9007199254740993,"old":true}}`, `{"id":18446744073709551615,"vector":[1,0]}`, `{"id":"ABCD0000-0000-0000-0000-000000000007","vector":[1,0],"payload":null}`)
	for i := 0; i < 5001; i++ {
		request.Edits = append(request.Edits, pointEdit("insert", fmt.Sprintf(`{"id":%d,"vector":[1,0]}`, i), nil))
	}
	result := nativeImportPoints(t, l, request)
	if len(l.write.receipt.Operations) != 1 || !l.write.receipt.Completed {
		t.Fatal("source split into multiple server operations")
	}
	v := nativePointVerify(t, l, result)
	if v.Status != "applied" || v.Recovered.Applied != 5004 || len(v.Recovered.Outcomes) != 0 || len(v.States) != 5004 || !v.Recovered.Verified {
		t.Fatal("complete native import readback", v.Status)
	}
	var evidence []struct {
		IDs    []json.RawMessage
		States []string
	}
	if json.Unmarshal([]byte(v.Recovered.Async.TaskJSON), &evidence) != nil || len(evidence) < 51 || len(evidence) > 60 {
		t.Fatal("unbounded individual point reads", len(evidence))
	}
	for _, e := range evidence {
		if len(e.IDs) > 100 || len(e.IDs) != len(e.States) {
			t.Fatal("native retrieval group bounds")
		}
	}
	if _, e := l.importPoints(context.Background(), s.ProfileID(), name, "points", importRequest("insert_only", `{"id":9007199254740993,"vector":[1,0]}`)); !errors.Is(e, contract.ErrDataConflict) {
		t.Fatal("insert mode overwrote original", e)
	}
	if _, e := l.importPoints(context.Background(), s.ProfileID(), name, "points", importRequest("update_only", `{"id":7000,"vector":[1,0]}`)); !errors.Is(e, contract.ErrDataConflict) {
		t.Fatal("update created a missing point", e)
	}
	result = nativeImportPoints(t, l, importRequest("update_only", `{"id":9007199254740993,"vector":[1,0]}`))
	v = nativePointVerify(t, l, result)
	if v.Status != "applied" {
		t.Fatal(v.Status)
	}
	actual, _, e := c.ReadPoint(context.Background(), name, []byte(`9007199254740993`))
	if e != nil || string(actual.Payload) != "{}" {
		t.Fatal("native omitted payload survived replacement", e)
	}
	result = nativeImportPoints(t, l, importRequest("upsert", `{"id":"abcd0000-0000-0000-0000-000000000007","vector":[0,1],"payload":{"decimal":0.10000000000000001}}`, `{"id":7000,"vector":[1,0],"payload":{"u64":18446744073709551615}}`))
	v = nativePointVerify(t, l, result)
	if v.Status != "applied" || v.Recovered.Applied != 2 {
		t.Fatal("mixed native upsert", v.Status)
	}
	if n, _, e := c.Count(context.Background(), name, nil); e != nil || n != 5005 {
		t.Fatal("import damaged unrelated records", n, e)
	}
	named := nativeCollection(t, c, `{"vectors":{"a":{"size":2,"distance":"Dot"},"b":{"size":2,"distance":"Dot"}}}`)
	nl := reservePointData(t, s, named)
	result = nativeImportPoints(t, nl, importRequest("upsert", `{"id":1,"vector":{"a":[1,0],"b":[0,1]},"payload":{"old":true}}`))
	if nativePointVerify(t, nl, result).Status != "applied" {
		t.Fatal("named source")
	}
	result = nativeImportPoints(t, nl, importRequest("upsert", `{"id":1,"vector":{"a":[0,1]}}`))
	if nativePointVerify(t, nl, result).Status != "applied" {
		t.Fatal("named replacement")
	}
	actual, _, e = c.ReadPoint(context.Background(), named, []byte(`1`))
	if e != nil || strings.Contains(string(actual.Vector), `"b"`) || string(actual.Payload) != "{}" {
		t.Fatal("old named vector/payload survived", e)
	}
	t.Log("5004 exact integer/UUID points, one operation, bounded native multi-ID retrieval, insert/update preflight, upsert, original receipts, payload/named-vector clearing and complete explicit readback verified")
}
func TestQdrantNativeDataImportRacedModeSkipAndReadOnlyRetainOriginalEvidence(t *testing.T) {
	_, c := nativeData(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	dataSeed(t, c, name, []any{map[string]any{"id": 1, "vector": []float64{1, 0}}, map[string]any{"id": 2, "vector": []float64{1, 0}}})
	var writes atomic.Int32
	cfg := nativeDataWriteRelay(t, c, nil, func(r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			writes.Add(1)
			nativeRun(t, c, Request{Method: "POST", Path: "/collections/" + name + "/points/delete?wait=true", Body: []byte(`{"points":[2]}`)})
		}
	})
	s := testSession(t, cfg, "")
	l := reservePointData(t, s, name)
	request := importRequest("update_only", `{"id":1,"vector":[0,1]}`, `{"id":2,"vector":[0,1]}`)
	result := nativeImportPoints(t, l, request)
	v := nativePointVerify(t, l, result)
	if v.Status != "indeterminate" || !reflect.DeepEqual(v.States, []string{"matched", "missing"}) || v.Recovered.CommitConfirmed || v.Recovered.Async.TaskJSON == "" {
		t.Fatal("native update-only skip falsely applied", v)
	}
	if _, e := l.importPoints(context.Background(), s.ProfileID(), name, "points", request); !errors.Is(e, contract.ErrDataCommitUncertain) || writes.Load() != 1 {
		t.Fatal("partial import replayed", e)
	}
	auth, _, readonly := nativeFixture(t)
	if auth.Auth == "api_key" {
		ro := testSession(t, auth, readonly)
		rl := reservePointData(t, ro, name)
		denied, e := rl.importPoints(context.Background(), ro.ProfileID(), name, "points", importRequest("upsert", `{"id":7,"vector":[1,0]}`))
		if e == nil || denied == nil || denied.Saved.Async == nil || denied.Saved.Async.ReceiptJSON == "" {
			t.Fatal("native readonly receipt discarded", e)
		}
		v = nativePointVerify(t, rl, denied.Saved)
		if v.Status != "indeterminate" || v.States[0] != "missing" {
			t.Fatal("denial became applied", v)
		}
	}
}
func TestQdrantNativeDataImportLostCanceledCloseAndReadbackFaultsNeverReplay(t *testing.T) {
	for _, fault := range []string{"lost", "canceled", "close", "session_close", "read_lost", "read_canceled"} {
		t.Run(fault, func(t *testing.T) {
			_, c := nativeData(t)
			name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
			observed, acked := make(chan struct{}), make(chan struct{})
			var enabled atomic.Bool
			var writes atomic.Int32
			cfg := nativeDataWriteRelay(t, c, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				mutation := strings.HasSuffix(r.URL.Path, "/batch")
				if mutation {
					writes.Add(1)
				}
				match := mutation
				if strings.HasPrefix(fault, "read_") {
					match = r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/points") && enabled.Load()
				}
				if !match {
					return false
				}
				close(observed)
				if strings.Contains(fault, "lost") {
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
			request := importRequest("upsert", `{"id":1,"vector":[1,0]}`)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				saved  *contract.DataEditResult
				verify *contract.DataVerification
				err    error
			}
			out := make(chan outcome, 1)
			var submitted *contract.DataEditResult
			if strings.HasPrefix(fault, "read_") {
				submitted = nativeImportPoints(t, l, request)
				enabled.Store(true)
				go func() {
					v, e := l.verifyWrite(ctx, s.ProfileID(), name, "points", submitted)
					out <- outcome{verify: v, err: e}
				}()
			} else {
				go func() {
					r, e := l.importPoints(ctx, s.ProfileID(), name, "points", request)
					var saved *contract.DataEditResult
					if r != nil {
						saved = r.Saved
					}
					out <- outcome{saved: saved, err: e}
				}()
			}
			waitSessionSignal(t, observed)
			switch fault {
			case "canceled", "read_canceled":
				cancel()
			case "close":
				l.Close()
			case "session_close":
				s.Close()
			}
			var got outcome
			select {
			case got = <-out:
			case <-time.After(10 * time.Second):
				t.Fatal("original importer did not acknowledge interruption")
			}
			waitSessionSignal(t, acked)
			if got.err == nil {
				t.Fatal("fault claimed success")
			}
			if !strings.HasPrefix(fault, "read_") && (got.saved == nil || !got.saved.CommitUncertain || got.saved.Async == nil) {
				t.Fatal("unknown import receipt discarded")
			}
			if strings.HasPrefix(fault, "read_") {
				var interrupted *Interrupted
				if !errors.As(got.err, &interrupted) || interrupted.WriteOutcomeUnknown || got.verify == nil || got.verify.Status != "indeterminate" {
					t.Fatal("readback fault became mutation/admission", got.err)
				}
			}
			if fault != "close" && fault != "session_close" {
				if _, e := l.importPoints(context.Background(), s.ProfileID(), name, "points", request); !errors.Is(e, contract.ErrDataCommitUncertain) {
					t.Fatal("interrupted import replayed", e)
				}
			}
			if writes.Load() != 1 {
				t.Fatal("native import repeated")
			}
			if n, _, e := c.Count(context.Background(), name, nil); e != nil || n != 1 {
				t.Fatal("original admitted server mutation lost", n, e)
			}
		})
	}
}
