package qdrant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func pointEdit(kind, raw string, snapshot *contract.RowSnapshot) contract.DataEdit {
	e := contract.DataEdit{Kind: kind, Original: snapshot}
	if kind != "delete" {
		e.Values = map[string]contract.DataValue{"point": {Kind: contract.JSON, Text: raw}}
	}
	return e
}
func cloneSnapshot(s *contract.RowSnapshot) *contract.RowSnapshot {
	raw, _ := json.Marshal(s)
	var out contract.RowSnapshot
	_ = json.Unmarshal(raw, &out)
	return &out
}
func TestQdrantDataWriteOriginalSnapshotsReceiptReadbackAndNoReplay(t *testing.T) {
	point := `{"id":9007199254740993,"vector":{"dense":[1,0]},"payload":null}`
	var writes atomic.Int32
	_, l := unitDataLease(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/scroll"):
			fmt.Fprint(w, `{"status":"ok","result":{"points":[`+point+`],"next_page_offset":9007199254740995}}`)
		case r.Method == "GET":
			fmt.Fprint(w, `{"status":"ok","result":`+point+`}`)
		case strings.HasSuffix(r.URL.Path, "/batch"):
			writes.Add(1)
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Operations []struct {
					Upsert struct {
						Points []json.RawMessage
						Mode   string `json:"update_mode"`
					}
				}
			}
			_ = json.Unmarshal(raw, &body)
			if len(body.Operations) != 1 || body.Operations[0].Upsert.Mode != "update_only" {
				t.Error("unbound native mode")
			}
			point = string(body.Operations[0].Upsert.Points[0])
			fmt.Fprint(w, `{"status":"ok","result":[{"operation_id":18446744073709551615,"status":"completed"}]}`)
		default:
			t.Error(r.URL.Path)
		}
	})
	ctx := context.Background()
	page, err := l.page(ctx, l.object.ProfileID, "owned", "points", dataOptions(l))
	if err != nil {
		t.Fatal(err)
	}
	row, err := l.Runtime().RowReader(ctx, l.object.ProfileID, "owned", "points", *page.RowRefs[0])
	if err != nil || !reflect.DeepEqual(row.Snapshot, page.RowSnapshots[0]) {
		t.Fatal("full edit read changed signed preview", err)
	}
	full, err := l.row(ctx, l.object.ProfileID, "owned", "points", *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*contract.RowSnapshot{row.Snapshot, full.Snapshot} {
		if _, err = l.validateWriteSnapshot(s); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 4; n++ {
			forged := cloneSnapshot(s)
			if s.Mode == "values" {
				forged.Original[n].Text = "forged"
			} else if forged.Fingerprints[n] == nil {
				h := "forged"
				forged.Fingerprints[n] = &h
			} else {
				*forged.Fingerprints[n] = "forged"
			}
			if _, err = l.save(ctx, l.object.ProfileID, "owned", "points", []contract.DataEdit{pointEdit("delete", "", forged)}); !errors.Is(err, ErrInvalid) {
				t.Fatal("forged snapshot admitted", n, err)
			}
		}
	}
	edit := pointEdit("update", `{"id":9007199254740993,"vector":{"dense":[0,1]},"payload":{"n":9007199254740993}}`, full.Snapshot)
	result, err := l.save(ctx, l.object.ProfileID, "owned", "points", []contract.DataEdit{edit})
	if err != nil || result.Async == nil || result.Async.TaskID != "18446744073709551615" || result.CommitConfirmed || result.Applied != 0 {
		t.Fatal("receipt claimed affected count", err)
	}
	result.Async.Status = "changed caller copy"
	if l.write.result.Async.Status != "completed" {
		t.Fatal("mutable receipt escaped")
	}
	if _, err = l.save(ctx, l.object.ProfileID, "owned", "points", []contract.DataEdit{edit}); !errors.Is(err, contract.ErrDataCommitUncertain) || writes.Load() != 1 {
		t.Fatal("batch replayed", err)
	}
	forged := copyWriteResult(*result)
	forged.Async.ReceiptJSON = "{}"
	if _, err = l.verifyWrite(ctx, l.object.ProfileID, "owned", "points", forged); !errors.Is(err, ErrInvalid) {
		t.Fatal("foreign receipt accepted", err)
	}
	v, err := l.verifyWrite(ctx, l.object.ProfileID, "owned", "points", result)
	if err != nil || v.Status != "applied" || !v.Recovered.Verified || v.Recovered.CommitUncertain || len(v.Recovered.Outcomes) != 1 || v.Recovered.Async.TaskJSON == "" {
		t.Fatal("native outcome missing", v, err)
	}
	o := dataOptions(l)
	o.Offset = page.NextCursor.Offset
	o.After = page.NextCursor
	if _, err = l.page(ctx, l.object.ProfileID, "owned", "points", o); !errors.Is(err, ErrInvalid) {
		t.Fatal("old cursor accepted after write", err)
	}
}
func TestQdrantDataWriteLimitsAndPayloadPrecision(t *testing.T) {
	for _, raw := range []string{`{"id":1,"vector":[],"payload":{"x":1e999999999}}`, `{"id":1,"vector":[],"payload":{"x":18446744073709551616}}`} {
		f, _ := object([]byte(raw))
		if validWritePayload(f["payload"]) {
			t.Fatal("native JSON precision boundary ignored")
		}
	}
	if payloadEqual([]byte(`{"n":9007199254740993}`), []byte(`{"n":9007199254740992}`)) || payloadEqual([]byte(`{"n":1e999999999}`), []byte(`{"n":1e999999999}`)) {
		t.Fatal("integer rounding/nonfinite payload accepted")
	}
	if !payloadEqual(nil, []byte(`{}`)) || !payloadEqual([]byte(`{"x":0.10000000000000001}`), []byte(`{"x":0.1}`)) || payloadEqual([]byte(`{"x":null}`), []byte(`{}`)) {
		t.Fatal("native null/float semantics")
	}
	e := pointEdit("insert", `{"id":1,"vector":[1,0]}`, nil)
	for _, v := range []contract.DataValue{{Kind: contract.JSON, Text: e.Values["point"].Text, Null: true}, {Kind: contract.JSON, Text: e.Values["point"].Text, Default: true}, {Kind: contract.Text, Text: e.Values["point"].Text}, {Kind: contract.JSON, Text: strings.Repeat("x", contract.DataEditMaxBytes+1)}} {
		bad := e
		bad.Values = map[string]contract.DataValue{"point": v}
		if _, err := copyDataEdits([]contract.DataEdit{bad}); err == nil {
			t.Fatal("unsupported edit admitted")
		}
	}
	if _, err := copyDataEdits(make([]contract.DataEdit, contract.DataMaxPageSize+1)); !errors.Is(err, ErrBounds) {
		t.Fatal(err)
	}
	if pointMissing(Response{Status: 404, Body: []byte(`{"status":{"error":"Not found: Collection owned doesn't exist!"}}`)}, &HTTPError{Status: 404}) {
		t.Fatal("collection missing became point absence")
	}
}
func TestQdrantDataVectorWriteSchemaAndNativePrecision(t *testing.T) {
	for _, test := range []struct{ dtype, distance, raw, want string }{
		{"float32", "Cosine", "[3,4]", "[0.6,0.8]"}, {"float16", "Dot", "[1.0003,-0.3333]", "[1,-0.333251953125]"}, {"uint8", "Cosine", "[-1,300.5]", "[0,255]"},
	} {
		spec := map[string]vectorWriteSpec{"": {size: 2, distance: test.distance, datatype: test.dtype}}
		a, e := decodeWriteVectors([]byte(test.raw), spec, true)
		b, e2 := decodeWriteVectors([]byte(test.want), spec, false)
		if e != nil || e2 != nil || !vectorsMatch(a, b, spec) {
			t.Fatal(test, e, e2)
		}
	}
	for _, dt := range []string{"turbo4", "turbo8", "turbo16", "future"} {
		body := `{"status":"ok","result":{"config":{"params":{"vectors":{"size":2,"distance":"Dot","datatype":"` + dt + `"}}}}}`
		if _, err := vectorWriteSchema([]byte(body)); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("unknown precision editable", err)
		}
	}
}

func TestQdrantDataWriteNonJSONReceiptPreservesOriginalBytesAndBinding(t *testing.T) {
	_, l := unitDataLease(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"status":{"error":"Not found: Point with id 1 does not exists!"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(400)
		w.Write([]byte{0xff, 0x00, 0xfe})
	})
	edits := []contract.DataEdit{pointEdit("insert", `{"id":1,"vector":{"dense":[1,0]}}`, nil)}
	saved, err := l.save(context.Background(), l.object.ProfileID, "owned", "points", edits)
	if err == nil || saved == nil || saved.Async == nil || !saved.CommitUncertain {
		t.Fatal("native non-JSON receipt discarded", err)
	}
	var evidence struct {
		HTTPStatus                      int
		ContentType, Body, BodyEncoding string
	}
	if json.Unmarshal([]byte(saved.Async.ReceiptJSON), &evidence) != nil || evidence.HTTPStatus != 400 || evidence.ContentType != "application/octet-stream" || evidence.BodyEncoding != "base64" || evidence.Body != "/wD+" {
		t.Fatal("original HTTP bytes/status/content type lost", saved.Async.ReceiptJSON)
	}
	batch, _ := PreparePointBatch("owned", []PointWrite{{Action: "upsert", Mode: "insert_only", Points: []json.RawMessage{[]byte(edits[0].Values["point"].Text)}}})
	expected := l.sign(struct {
		Object                        contract.ObjectRef
		Observation, Request, Receipt string
	}{l.object, l.binding(), fingerprint(batch.request.Body), saved.Async.ReceiptJSON})
	if expected != saved.Async.Token {
		t.Fatal("non-JSON receipt lost request/collection signature binding")
	}
	if _, err = l.save(context.Background(), l.object.ProfileID, "owned", "points", edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("invalid receipt allowed replay", err)
	}
}
