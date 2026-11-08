package qdrant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (l *dataLease) importPoints(ctx context.Context, id int64, db, name string, r contract.DataImportRequest) (*contract.DataImportResult, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	if !slices.Contains([]string{"insert_only", "update_only", "upsert"}, r.Mode) || r.NewTable || r.ContinueOnError || len(r.MatchColumns) != 0 || len(r.Columns) != 0 {
		return nil, contract.ErrUnsupported
	}
	if !l.writeMu.TryLock() {
		return nil, ErrBusy
	}
	defer l.writeMu.Unlock()
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if l.write != nil {
		return &contract.DataImportResult{Saved: copyWriteResult(l.write.result), CommitUncertain: true}, contract.ErrDataCommitUncertain
	}
	edits, err := copyPointEdits(r.Edits, contract.MaxDataImportRows)
	if err != nil {
		return nil, err
	}
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	info, err := l.guard(ctx, c)
	if err != nil {
		return nil, err
	}
	specs, err := vectorWriteSchema(info)
	if err != nil {
		return nil, err
	}
	w := &dataWrite{edits: edits, importing: true, specs: specs}
	seen := map[string]bool{}
	rawPoints := make([]json.RawMessage, len(edits))
	size := 0
	for i, e := range edits {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if e.Kind != "insert" || e.Original != nil {
			return nil, ErrInvalid
		}
		raw := []byte(e.Values["point"].Text)
		size += len(raw) + 1
		if size > requestLimit {
			return nil, ErrBounds
		}
		points, err := parsePoints(append(append([]byte{'['}, raw...), ']'), 1)
		if err != nil || len(points) != 1 {
			return nil, ErrInvalid
		}
		p := points[0]
		if seen[pointIDKey(p.ID)] || !validWritePayload(p.Payload) {
			return nil, ErrInvalid
		}
		seen[pointIDKey(p.ID)] = true
		vectors, err := decodeWriteVectors(p.Vector, specs, true)
		if err != nil {
			return nil, err
		}
		w.points = append(w.points, p)
		w.vectors = append(w.vectors, vectors)
		rawPoints[i] = p.JSON
	}
	batch, err := PreparePointBatch(db, []PointWrite{{Action: "upsert", Mode: r.Mode, Points: rawPoints}})
	if err != nil {
		return nil, err
	}
	// Native mode preflight rejects currently conflicting insert/update inputs.
	// This live observation is not atomic; raced skips remain indeterminate after readback.
	if r.Mode != "upsert" {
		for start := 0; start < len(w.points); {
			end := importReadEnd(w.points, start)
			ids := make([]json.RawMessage, end-start)
			for i, p := range w.points[start:end] {
				ids[i] = p.ID
			}
			actual, _, err := c.RetrievePoints(ctx, db, ids)
			if err != nil {
				return nil, err
			}
			if r.Mode == "insert_only" && len(actual) != 0 || r.Mode == "update_only" && len(actual) != len(ids) {
				return nil, contract.ErrDataConflict
			}
			start = end
		}
	}
	saved, err := l.submitWrite(ctx, c, w, batch)
	if saved == nil {
		return nil, err
	}
	return &contract.DataImportResult{Saved: saved, CommitUncertain: saved.CommitUncertain}, err
}

// Small read groups bound expected data as well as ID count. A single large or
// concurrently expanded point still has the original client's 16 MiB limit.
func importReadEnd(points []Point, start int) int {
	end, size := start, 0
	for end < len(points) && end-start < 100 {
		n := len(points[end].JSON) + 1024
		if end > start && size+n > 256<<10 {
			break
		}
		size += n
		end++
	}
	return end
}
func (l *dataLease) verifyImportedPoints(ctx context.Context, c *Client, w *dataWrite, out *contract.DataVerification) (matched bool, err error) {
	type readEvidence struct {
		IDs                []json.RawMessage
		States             []string
		HTTPStatus         int
		ContentType        string
		Incomplete         bool
		Body, BodyEncoding string
		BodyTruncated      bool `json:",omitempty"`
		OriginalBodyBytes  int
		RetainedBodyBytes  int
	}
	evidence := []readEvidence{}
	bytesRead := 0
	matched = true
	defer func() { raw, _ := json.Marshal(evidence); w.result.Async.TaskJSON = string(raw) }()
	for start := 0; start < len(w.points); {
		end := importReadEnd(w.points, start)
		ids := make([]json.RawMessage, end-start)
		for i, p := range w.points[start:end] {
			ids[i] = p.ID
		}
		actual, response, readErr := c.RetrievePoints(ctx, l.object.Catalog, ids)
		retained := response.Body
		truncated := len(retained) > responseLimit/2-bytesRead
		if truncated {
			retained = retained[:responseLimit/2-bytesRead]
		}
		bytesRead += len(retained)
		body, encoding := originalBody(retained)
		e := readEvidence{IDs: ids, HTTPStatus: response.Status, ContentType: response.ContentType, Incomplete: response.Incomplete, Body: body, BodyEncoding: encoding, BodyTruncated: truncated, OriginalBodyBytes: len(response.Body), RetainedBodyBytes: len(retained)}
		lookup := map[string]Point{}
		for _, p := range actual {
			lookup[pointIDKey(p.ID)] = p
		}
		for i, p := range w.points[start:end] {
			state := "missing"
			a, exists := lookup[pointIDKey(p.ID)]
			if readErr != nil || truncated {
				state = "unreadable"
			} else if exists {
				v, err := decodeWriteVectors(a.Vector, w.specs, false)
				if err == nil && payloadEqual(p.Payload, a.Payload) && vectorsMatch(w.vectors[start+i], v, w.specs) {
					state = "matched"
				} else {
					state = "diverged"
				}
			}
			if state != "matched" {
				matched = false
			}
			e.States = append(e.States, state)
			out.States = append(out.States, state)
		}
		evidence = append(evidence, e)
		if truncated {
			return false, errors.Join(ErrBounds, readErr)
		}
		if readErr != nil {
			return false, readErr
		}
		start = end
	}
	return matched, nil
}
