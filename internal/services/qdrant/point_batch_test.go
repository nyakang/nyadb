package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func unitPointBatch(t *testing.T) PointBatch {
	t.Helper()
	b, err := PreparePointBatch("owned", []PointWrite{{Action: "upsert", Mode: "insert_only", Points: []json.RawMessage{[]byte(dataPoint)}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestQdrantPointBatchPreparationCapturesExactJSONAndExplicitModes(t *testing.T) {
	point := json.RawMessage(`{"id":18446744073709551615,"vector":{"dense":[1.00000001,0]},"payload":{"n":9007199254740993,"v":1.2300e-20}}`)
	filter := json.RawMessage(`{"must":[{"key":"n","match":{"value":9007199254740993}}]}`)
	id := json.RawMessage(`"550e8400-e29b-41d4-a716-446655440000"`)
	writes := []PointWrite{{Action: "upsert", Mode: "update_only", Points: []json.RawMessage{point}, Filter: filter}, {Action: "delete", IDs: []json.RawMessage{id}}}
	b, err := PreparePointBatch("owned", writes)
	if err != nil {
		t.Fatal(err)
	}
	original := b.Preview()
	if b.Collection() != "owned" || b.OperationCount() != 2 || original.Method != "POST" || original.Path != "/collections/owned/points/batch?wait=true&ordering=strong" || !bytes.Contains(original.Body, point) || !bytes.Contains(original.Body, filter) || !bytes.Contains(original.Body, id) {
		t.Fatal("native batch values or endpoint changed")
	}
	point[1], filter[1], id[1] = 'x', 'x', 'x'
	writes[0].Mode = "upsert"
	writes[0].Points[0] = nil
	preview := b.Preview()
	preview.Body[0] = 'x'
	if bytes.Equal(preview.Body, b.Preview().Body) || !bytes.Equal(original.Body, b.Preview().Body) {
		t.Fatal("prepared batch shares mutable source or preview bytes")
	}
}

func TestQdrantPointBatchInvalidValuesAndBoundsRejectBeforeDispatch(t *testing.T) {
	valid := PointWrite{Action: "upsert", Mode: "insert_only", Points: []json.RawMessage{[]byte(dataPoint)}}
	bad := []PointWrite{
		{Action: "upsert", Points: valid.Points},
		{Action: "upsert", Mode: "other", Points: valid.Points},
		{Action: "upsert", Mode: "upsert", Points: []json.RawMessage{[]byte(`{"id":1,"id":2,"vector":[1,0]}`)}},
		{Action: "upsert", Mode: "upsert", Points: []json.RawMessage{[]byte(`{"id":9007199254740993.0,"vector":[1,0]}`)}},
		{Action: "upsert", Mode: "upsert", Points: []json.RawMessage{[]byte(`{"id":1,"vector":null}`)}},
		{Action: "upsert", Mode: "upsert", Points: []json.RawMessage{[]byte(`{"id":1,"vector":[1,0],"version":1}`)}},
		{Action: "upsert", Mode: "upsert", Points: []json.RawMessage{[]byte(`{"id":1,"vector":[1,0],"payload":[]}`)}},
		{Action: "upsert", Mode: "upsert", Points: []json.RawMessage{[]byte(`{"id":1,"vector":[1,0],"payload":{"s":"\ud800"}}`)}},
		{Action: "upsert", Mode: "upsert", Points: valid.Points, IDs: []json.RawMessage{[]byte("1")}},
		{Action: "upsert", Mode: "upsert", Points: valid.Points, Filter: []byte("[]")},
		{Action: "delete"},
		{Action: "delete", IDs: []json.RawMessage{[]byte("1")}, Filter: []byte("{}")},
		{Action: "delete", IDs: []json.RawMessage{[]byte("18446744073709551616")}},
		{Action: "delete", Mode: "upsert", IDs: []json.RawMessage{[]byte("1")}},
		{Action: "unknown", Points: valid.Points},
	}
	for i, write := range bad {
		if _, err := PreparePointBatch("owned", []PointWrite{write}); err == nil {
			t.Fatalf("invalid native batch %d accepted", i)
		}
	}
	if _, err := PreparePointBatch("alias/elsewhere", []PointWrite{valid}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unqualified endpoint accepted", err)
	}
	if _, err := PreparePointBatch("owned", make([]PointWrite, 1001)); !errors.Is(err, ErrBounds) {
		t.Fatal("unbounded operation count", err)
	}
	if _, err := PreparePointBatch("owned", []PointWrite{{Action: "delete", IDs: make([]json.RawMessage, 10001)}}); !errors.Is(err, ErrBounds) {
		t.Fatal("unbounded native IDs", err)
	}
	large := json.RawMessage(`{"id":1,"vector":[1,0],"payload":{"memo":"` + strings.Repeat("x", 1<<20) + `"}}`)
	if _, err := PreparePointBatch("owned", []PointWrite{{Action: "upsert", Mode: "insert_only", Points: []json.RawMessage{large}}}); !errors.Is(err, ErrBounds) {
		t.Fatal("unbounded point accepted", err)
	}
	filter := json.RawMessage(`{"must":[],"text":"` + strings.Repeat("x", requestLimit/2) + `"}`)
	if _, err := PreparePointBatch("owned", []PointWrite{{Action: "delete", Filter: filter}, {Action: "delete", Filter: filter}}); !errors.Is(err, ErrBounds) {
		t.Fatal("aggregate native filters escaped the request bound", err)
	}
	var calls atomic.Int32
	c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
		if unitHandshake(w, r) {
			return
		}
		calls.Add(1)
	})
	if _, _, err := c.SubmitPointBatch(context.Background(), PointBatch{}); !errors.Is(err, ErrInvalid) || calls.Load() != 0 {
		t.Fatal("unprepared batch dispatched", err)
	}
}

func TestQdrantPointBatchReceiptsPreservePerOperationAdmissionAndExactIDs(t *testing.T) {
	for _, status := range []string{"completed", "acknowledged"} {
		t.Run(status, func(t *testing.T) {
			c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
				if unitHandshake(w, r) {
					return
				}
				if r.Method != "POST" || r.URL.Query().Get("wait") != "true" || r.URL.Query().Get("ordering") != "strong" {
					t.Error("native write ordering changed")
				}
				fmt.Fprint(w, `{"status":"ok","result":[{"operation_id":18446744073709551615,"status":"`+status+`"}]}`)
			})
			receipt, response, err := c.SubmitPointBatch(context.Background(), unitPointBatch(t))
			if err != nil || len(receipt.Operations) != 1 || receipt.Operations[0].OperationID != ^uint64(0) || receipt.Operations[0].Status != status || receipt.Completed != (status == "completed") || !bytes.Equal(receipt.Raw, response.Body) {
				t.Fatal("native admission was mistaken for completion", receipt, err)
			}
		})
	}
}

func TestQdrantPointBatchPartialFailureMalformedReceiptAndCancellationNeverReplay(t *testing.T) {
	for _, response := range []struct {
		status int
		body   string
	}{{400, `{"status":{"error":"second operation failed"}}`}, {200, `{"status":"ok","result":[]}`}, {200, `{"status":"ok","result":[{"operation_id":1,"status":"failed"}]}`}, {200, `{"status":"ok","result":[{"operation_id":1.0,"status":"completed"}]}`}} {
		var calls atomic.Int32
		c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
			if unitHandshake(w, r) {
				return
			}
			calls.Add(1)
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(response.status)
			fmt.Fprint(w, response.body)
		})
		batch := unitPointBatch(t)
		receipt, raw, err := c.SubmitPointBatch(context.Background(), batch)
		var interrupted *Interrupted
		if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || receipt.Completed || !bytes.Equal(receipt.Raw, raw.Body) || string(raw.Body) != response.body {
			t.Fatal("partial batch failure claimed rollback or discarded evidence", err)
		}
		if _, _, err = c.SubmitPointBatch(context.Background(), batch); !errors.Is(err, ErrRetired) || calls.Load() != 1 {
			t.Fatal("failed batch replayed", err)
		}
	}
	var calls atomic.Int32
	c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !unitHandshake(w, r) {
			calls.Add(1)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.SubmitPointBatch(ctx, unitPointBatch(t)); !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatal("canceled-before-admission batch dispatched", err)
	}
}
