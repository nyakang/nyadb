package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
)

// PointWrite describes one native batch operation. Upserts require an explicit
// mode; an update filter constrains existing points, never new inserts.
type PointWrite struct {
	Action, Mode string
	Points       []json.RawMessage
	IDs          []json.RawMessage
	Filter       json.RawMessage
}

// PointBatch is an immutable, endpoint-relative submission prepared without I/O.
// It is not a transaction, a collection incarnation guard or a retry token.
type PointBatch struct {
	collection string
	request    Request
	count      int
}

func (b PointBatch) Collection() string  { return b.collection }
func (b PointBatch) OperationCount() int { return b.count }
func (b PointBatch) Preview() Request {
	r := b.request
	r.Body = bytes.Clone(r.Body)
	return r
}

func validateWritePoint(raw json.RawMessage) error {
	if len(raw) > 1<<20 {
		return ErrBounds
	}
	f, err := object(raw)
	if err != nil || !validPointID(bytes.TrimSpace(f["id"])) {
		return ErrInvalid
	}
	for key := range f {
		if key != "id" && key != "vector" && key != "payload" {
			return ErrInvalid
		}
	}
	v := bytes.TrimSpace(f["vector"])
	// Native schema, dimensions and sparse/multi-vector interpretation belong to
	// the server. Preserve number spelling and never convert a vector to float64.
	if len(v) == 0 || v[0] != '[' && v[0] != '{' {
		return ErrInvalid
	}
	if p := f["payload"]; len(p) > 0 && !bytes.Equal(p, []byte("null")) {
		if _, err := object(p); err != nil {
			return ErrInvalid
		}
	}
	return nil
}

// PreparePointBatch captures complete point JSON and exact uint64/UUID IDs.
// wait=true and strong ordering request native acknowledgement of each operation;
// neither implies rollback or compare-and-swap between these operations.
func PreparePointBatch(collection string, writes []PointWrite) (PointBatch, error) {
	path, err := collectionPath(collection)
	if err != nil {
		return PointBatch{}, err
	}
	if len(writes) == 0 || len(writes) > 1000 {
		return PointBatch{}, ErrBounds
	}
	operations := make([]map[string]any, 0, len(writes))
	targets, size := 0, 0
	for _, write := range writes {
		size += len(write.Filter)
		if size > requestLimit {
			return PointBatch{}, ErrBounds
		}
		if len(write.Filter) > 0 {
			if _, err := object(write.Filter); err != nil {
				return PointBatch{}, ErrInvalid
			}
		}
		switch write.Action {
		case "upsert":
			if write.Mode != "insert_only" && write.Mode != "update_only" && write.Mode != "upsert" || len(write.IDs) != 0 || len(write.Points) == 0 {
				return PointBatch{}, ErrInvalid
			}
			targets += len(write.Points)
			if targets > 10000 {
				return PointBatch{}, ErrBounds
			}
			points := make([]json.RawMessage, len(write.Points))
			for i, point := range write.Points {
				size += len(point)
				if size > requestLimit {
					return PointBatch{}, ErrBounds
				}
				if err := validateWritePoint(point); err != nil {
					return PointBatch{}, err
				}
				points[i] = bytes.Clone(point)
			}
			body := map[string]any{"points": points, "update_mode": write.Mode}
			if len(write.Filter) > 0 {
				body["update_filter"] = json.RawMessage(bytes.Clone(write.Filter))
			}
			operations = append(operations, map[string]any{"upsert": body})
		case "delete":
			if write.Mode != "" || len(write.Points) != 0 || (len(write.IDs) == 0) == (len(write.Filter) == 0) {
				return PointBatch{}, ErrInvalid
			}
			targets += len(write.IDs)
			if targets > 10000 {
				return PointBatch{}, ErrBounds
			}
			body := map[string]any{}
			if len(write.Filter) > 0 {
				body["filter"] = json.RawMessage(bytes.Clone(write.Filter))
			} else {
				ids := make([]json.RawMessage, len(write.IDs))
				for i, id := range write.IDs {
					if !validPointID(id) {
						return PointBatch{}, ErrInvalid
					}
					ids[i] = bytes.Clone(id)
				}
				body["points"] = ids
			}
			operations = append(operations, map[string]any{"delete": body})
		default:
			return PointBatch{}, ErrInvalid
		}
	}
	body, err := json.Marshal(map[string]any{"operations": operations})
	if err != nil {
		return PointBatch{}, ErrInvalid
	}
	if len(body) > requestLimit {
		return PointBatch{}, ErrBounds
	}
	r := Request{Method: "POST", Path: path + "/points/batch?wait=true&ordering=strong", ContentType: "application/json", Body: body}
	if err := validateRequest(r); err != nil {
		return PointBatch{}, err
	}
	return PointBatch{collection: collection, request: r, count: len(writes)}, nil
}

type PointBatchReceipt struct {
	Collection string
	Operations []MutationReceipt
	Raw        json.RawMessage
	Completed  bool
}

// SubmitPointBatch submits exactly once using the original client. The native
// batch can partially apply even when HTTP reports failure. Retain its response;
// callers must inspect original points explicitly and must never replay it.
func (c *Client) SubmitPointBatch(ctx context.Context, batch PointBatch) (PointBatchReceipt, Response, error) {
	path, err := collectionPath(batch.collection)
	if err != nil || batch.count < 1 || batch.count > 1000 || batch.request.Method != "POST" || batch.request.Path != path+"/points/batch?wait=true&ordering=strong" {
		return PointBatchReceipt{}, Response{}, ErrInvalid
	}
	r, err := c.Do(ctx, batch.Preview())
	receipt := PointBatchReceipt{Collection: batch.collection, Raw: bytes.Clone(r.Body)}
	if err != nil {
		return receipt, r, err
	}
	result, parseErr := NativeResult(r.Body)
	var operations []json.RawMessage
	if r.Status != 200 || parseErr != nil || decodeJSON(result, &operations) != nil || len(operations) != batch.count {
		c.retire()
		return receipt, r, &Interrupted{WriteOutcomeUnknown: true, Cause: ErrProtocol}
	}
	completed := true
	for _, raw := range operations {
		f, err := object(raw)
		n, idErr := strconv.ParseUint(string(f["operation_id"]), 10, 64)
		var status string
		if err != nil || idErr != nil || strconv.FormatUint(n, 10) != string(f["operation_id"]) || decodeJSON(f["status"], &status) != nil || status != "completed" && status != "acknowledged" {
			c.retire()
			return receipt, r, &Interrupted{WriteOutcomeUnknown: true, Cause: ErrProtocol}
		}
		completed = completed && status == "completed"
		receipt.Operations = append(receipt.Operations, MutationReceipt{Collection: batch.collection, OperationID: n, Status: status, Raw: bytes.Clone(raw)})
	}
	receipt.Completed = completed
	return receipt, r, nil
}
