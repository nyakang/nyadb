package qdrant

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

	"github.com/nyakang/nyadb/internal/db/contract"
)

func importRequest(mode string, points ...string) contract.DataImportRequest {
	r := contract.DataImportRequest{Mode: mode}
	for _, p := range points {
		r.Edits = append(r.Edits, pointEdit("insert", p, nil))
	}
	return r
}
func TestQdrantDataImportOneOriginalBatchGroupedReadbackAndCapturedSource(t *testing.T) {
	database := map[string]json.RawMessage{}
	var writes, reads atomic.Int32
	_, l := unitDataLease(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/batch") {
			writes.Add(1)
			var b struct {
				Operations []struct {
					Upsert struct {
						Points []json.RawMessage
						Mode   string `json:"update_mode"`
					}
				}
			}
			_ = json.Unmarshal(raw, &b)
			if len(b.Operations) != 1 || b.Operations[0].Upsert.Mode != "insert_only" {
				t.Error("import split or native mode lost")
			}
			for _, p := range b.Operations[0].Upsert.Points {
				f, _ := object(p)
				database[pointIDKey(f["id"])] = p
			}
			fmt.Fprint(w, `{"status":"ok","result":[{"status":"completed","operation_id":9007199254740993}]}`)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/collections/owned/points" {
			t.Error("import used unbounded individual reads", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		reads.Add(1)
		var request struct {
			IDs             []json.RawMessage
			Payload, Vector bool
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		_ = json.Unmarshal(fields["ids"], &request.IDs)
		if string(fields["with_payload"]) != "true" || string(fields["with_vector"]) != "true" || len(request.IDs) > 100 {
			t.Error("native retrieval projections/bounds lost")
		}
		rows := []json.RawMessage{}
		for _, id := range request.IDs {
			if p, ok := database[pointIDKey(id)]; ok {
				rows = append(rows, p)
			}
		}
		result, _ := json.Marshal(rows)
		fmt.Fprint(w, `{"status":"ok","result":`+string(result)+`}`)
	})
	r := contract.DataImportRequest{Mode: "insert_only"}
	for i := 0; i < 501; i++ {
		r.Edits = append(r.Edits, pointEdit("insert", fmt.Sprintf(`{"id":%d,"vector":{"dense":[1,0]},"payload":{"n":9007199254740993}}`, i), nil))
	}
	result, err := l.importPoints(context.Background(), l.object.ProfileID, "owned", "points", r)
	if err != nil || result.Saved.Async == nil || result.Saved.CommitConfirmed || writes.Load() != 1 || reads.Load() != 6 {
		t.Fatal("one import/bounded native preflight", err, writes.Load(), reads.Load())
	}
	r.Edits[0].Values["point"] = contract.DataValue{Kind: contract.JSON, Text: `{"id":0,"vector":{"dense":[0,1]}}`}
	if strings.Contains(string(l.write.points[0].Vector), "[0,1]") {
		t.Fatal("caller source mutation escaped")
	}
	if _, err = l.save(context.Background(), l.object.ProfileID, "owned", "points", []contract.DataEdit{pointEdit("insert", `{"id":999,"vector":{"dense":[1,0]}}`, nil)}); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("grid wrote across unresolved import", err)
	}
	v, err := l.verifyWrite(context.Background(), l.object.ProfileID, "owned", "points", result.Saved)
	if err != nil || v.Status != "applied" || !v.Recovered.Verified || v.Recovered.Applied != 501 || len(v.Recovered.Outcomes) != 0 || len(v.States) != 501 || reads.Load() != 12 || v.Recovered.Async.TaskJSON == "" {
		t.Fatal("import readback allocated previews or skipped source rows", err, reads.Load())
	}
	if _, err = l.importPoints(context.Background(), l.object.ProfileID, "owned", "points", importRequest("insert_only", `{"id":0,"vector":{"dense":[1,0]}}`)); !errors.Is(err, contract.ErrDataConflict) || writes.Load() != 1 {
		t.Fatal("conflicting insert admitted", err)
	}
}
func TestQdrantDataImportRejectsUnsupportedModesDuplicatesAndIncompleteReceipts(t *testing.T) {
	var writes atomic.Int32
	present := false
	_, l := unitDataLease(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			writes.Add(1)
			present = true
			fmt.Fprint(w, `{"status":"ok","result":[{"operation_id":0,"status":"acknowledged"}]}`)
			return
		}
		rows := "[]"
		if present {
			rows = `[{"id":1,"vector":{"dense":[1,0]}}]`
		}
		fmt.Fprint(w, `{"status":"ok","result":`+rows+`}`)
	})
	for _, mode := range []string{"append", "document_merge", "document_replace", "delete", ""} {
		if _, err := l.importPoints(context.Background(), l.object.ProfileID, "owned", "points", importRequest(mode, `{"id":1,"vector":{"dense":[1,0]}}`)); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("implicit native mode admitted", mode, err)
		}
	}
	for _, r := range []contract.DataImportRequest{importRequest("upsert", `{"id":1,"vector":{"dense":[1,0]}}`, `{"id":1,"vector":{"dense":[0,1]}}`), importRequest("upsert", `{"id":"ABCD0000-0000-0000-0000-000000000001","vector":{"dense":[1,0]}}`, `{"id":"abcd0000-0000-0000-0000-000000000001","vector":{"dense":[0,1]}}`), importRequest("upsert", `{"id":1,"vector":{"dense":[1]}}`)} {
		if _, err := l.importPoints(context.Background(), l.object.ProfileID, "owned", "points", r); err == nil || writes.Load() != 0 {
			t.Fatal("invalid source reached writer", err)
		}
	}
	r := importRequest("upsert", `{"id":1,"vector":{"dense":[1,0]}}`)
	result, err := l.importPoints(context.Background(), l.object.ProfileID, "owned", "points", r)
	if err != nil {
		t.Fatal(err)
	}
	v, err := l.verifyWrite(context.Background(), l.object.ProfileID, "owned", "points", result.Saved)
	if err != nil || v.Status != "indeterminate" || v.States[0] != "matched" || v.Recovered.CommitConfirmed {
		t.Fatal("acknowledged receipt became applied", err)
	}
	if _, err = l.importPoints(context.Background(), l.object.ProfileID, "owned", "points", r); !errors.Is(err, contract.ErrDataCommitUncertain) || writes.Load() != 1 {
		t.Fatal("incomplete receipt replayed", err)
	}
}

func TestQdrantDataImportBoundedEvidenceRetainsLastResponseMetadataAndSource(t *testing.T) {
	var writes atomic.Int32
	_, l := unitDataLease(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/batch") {
			writes.Add(1)
			fmt.Fprint(w, `{"status":"ok","result":[{"operation_id":1,"status":"completed"}]}`)
			return
		}
		var request struct{ IDs []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		rows := make([]json.RawMessage, len(request.IDs))
		for i, id := range request.IDs {
			payload := ""
			if i == 0 {
				// Concurrent payload growth makes both read groups exceed 4 MiB.
				payload = strings.Repeat("x", 5<<20)
			}
			rows[i] = json.RawMessage(fmt.Sprintf(`{"id":%s,"vector":{"dense":[1,0]},"payload":{"text":"%s"}}`, id, payload))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "result": rows})
	})
	r := contract.DataImportRequest{Mode: "upsert"}
	for i := 0; i < 101; i++ {
		r.Edits = append(r.Edits, pointEdit("insert", fmt.Sprintf(`{"id":%d,"vector":{"dense":[1,0]},"payload":{"text":""}}`, i), nil))
	}
	saved, err := l.importPoints(context.Background(), l.object.ProfileID, "owned", "points", r)
	if err != nil {
		t.Fatal(err)
	}
	v, err := l.verifyWrite(context.Background(), l.object.ProfileID, "owned", "points", saved.Saved)
	if !errors.Is(err, ErrBounds) || v.Recovered.CommitConfirmed || len(v.States) != 101 || v.States[100] != "unreadable" {
		t.Fatal("bounded readback confirmed or discarded original IDs", err)
	}
	var evidence []struct {
		HTTPStatus        int
		ContentType       string
		Body              string
		BodyEncoding      string
		BodyTruncated     bool
		OriginalBodyBytes int
		RetainedBodyBytes int
	}
	if err = json.Unmarshal([]byte(v.Recovered.Async.TaskJSON), &evidence); err != nil || len(evidence) != 2 {
		t.Fatal("last read response metadata discarded", err)
	}
	last := evidence[1]
	if !last.BodyTruncated || last.HTTPStatus != 200 || last.ContentType != "application/json" || last.BodyEncoding != "utf-8" || last.RetainedBodyBytes == 0 || last.OriginalBodyBytes <= last.RetainedBodyBytes || evidence[0].RetainedBodyBytes+last.RetainedBodyBytes != responseLimit/2 || len(last.Body) != last.RetainedBodyBytes {
		t.Fatal("bounded original evidence lacks explicit byte accounting")
	}
	if _, err = l.importPoints(context.Background(), l.object.ProfileID, "owned", "points", r); !errors.Is(err, contract.ErrDataCommitUncertain) || writes.Load() != 1 || len(l.write.points) != 101 {
		t.Fatal("evidence bounds released source or allowed replay", err)
	}
}

func TestQdrantRetrievePointsRejectsForeignDuplicateIDsAndCertifiesReadFaults(t *testing.T) {
	for _, rows := range []string{`[{"id":2}]`, `[{"id":1},{"id":1}]`, `[{"id":"ABCD0000-0000-0000-0000-000000000001"},{"id":"abcd0000-0000-0000-0000-000000000001"}]`} {
		t.Run(rows, func(t *testing.T) {
			s, l := unitDataLease(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"status":"ok","result":`+rows+`}`) })
			c, done, e := l.client(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			defer done()
			ids := []json.RawMessage{[]byte(`1`)}
			if strings.Contains(rows, "ABCD") {
				ids = []json.RawMessage{[]byte(`"ABCD0000-0000-0000-0000-000000000001"`), []byte(`3`)}
			}
			if _, _, e = c.RetrievePoints(context.Background(), "owned", ids); !errors.Is(e, ErrProtocol) {
				t.Fatal("foreign/duplicate original IDs accepted", e)
			}
			_ = s
		})
	}
	_, l := unitDataLease(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "200")
		fmt.Fprint(w, `{"status":"ok","result":[`)
	})
	c, done, e := l.client(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer done()
	_, response, e := c.RetrievePoints(context.Background(), "owned", []json.RawMessage{[]byte(`1`)})
	var interrupted *Interrupted
	if !errors.As(e, &interrupted) || interrupted.WriteOutcomeUnknown || !response.Incomplete || len(response.Body) == 0 {
		t.Fatal("native retrieval certified as mutation or lost partial bytes", e)
	}
}
