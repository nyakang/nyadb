package qdrant

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// A lease retains one unresolved original batch, including unsuccessful HTTP
// receipts. Native batches are not atomic; no outcome permits automatic replay.
type dataWrite struct {
	edits   []contract.DataEdit
	points  []Point
	vectors []map[string]writeVector
	specs   map[string]vectorWriteSpec
	receipt PointBatchReceipt
	result  contract.DataEditResult
}

func copyDataEdits(edits []contract.DataEdit) ([]contract.DataEdit, error) {
	if len(edits) == 0 || len(edits) > contract.DataMaxPageSize {
		return nil, ErrBounds
	}
	// Bound every variable-sized member before serializing the deep copy.
	size := 0
	add := func(n int) bool {
		if n > 3*requestLimit-size {
			return false
		}
		size += n
		return true
	}
	cells := func(values []contract.Cell) bool {
		if len(values) > 4 {
			return false
		}
		for _, v := range values {
			if !add(len(string(v.Kind))) || !add(len(v.Text)) || !add(len(v.Bytes)) {
				return false
			}
		}
		return true
	}
	for _, e := range edits {
		if e.Kind != "insert" && e.Kind != "update" && e.Kind != "delete" {
			return nil, contract.ErrUnsupported
		}
		if (e.Kind == "insert") != (e.Original == nil) {
			return nil, ErrInvalid
		}
		if e.Kind == "delete" {
			if len(e.Values) != 0 {
				return nil, ErrInvalid
			}
		} else {
			v, ok := e.Values["point"]
			if !ok || len(e.Values) != 1 || v.Kind != contract.JSON || v.Null || v.Default || v.Binary || len(v.Bytes) != 0 {
				return nil, ErrInvalid
			}
			if len(v.Text) > contract.DataEditMaxBytes {
				return nil, contract.ErrDataEditLimit
			}
			if !add(len(v.Text)) {
				return nil, ErrBounds
			}
			if err := validateWritePoint([]byte(v.Text)); err != nil {
				return nil, err
			}
		}
		if s := e.Original; s != nil {
			for _, v := range []string{s.Mode, s.Ref.Database, s.Ref.Table, s.Ref.Object.Catalog, s.Ref.Object.Schema, s.Ref.Object.Name, s.Ref.Object.Kind} {
				if !add(len(v)) {
					return nil, ErrBounds
				}
			}

			if len(s.Columns) != 4 || len(s.Fingerprints) > 4 || len(s.Ref.Columns) != 1 || len(s.Ref.Values) != 1 || !add(len(s.Ref.Values[0])) || !cells(s.Original) || !cells(s.Ref.TypedValues) || !add(len(s.Metadata)) {
				return nil, ErrBounds
			}
			for _, name := range append(slices.Clone(s.Columns), s.Ref.Columns...) {
				if !add(len(name)) {
					return nil, ErrBounds
				}
			}
			for _, h := range s.Fingerprints {
				if h != nil && !add(len(*h)) {
					return nil, ErrBounds
				}
			}
		}
	}
	raw, err := json.Marshal(edits)
	if err != nil || len(raw) > 6*requestLimit {
		return nil, ErrBounds
	}
	var out []contract.DataEdit
	if json.Unmarshal(raw, &out) != nil {
		return nil, ErrInvalid
	}
	return out, nil
}

func cellFingerprint(v contract.Cell) (string, error) {
	if v.Kind == contract.JSON {
		raw, err := canonicalJSON([]byte(v.Text))
		if err != nil {
			return "", err
		}
		v.Text = string(raw)
	}
	raw, err := json.Marshal(v)
	return fingerprint(raw), err
}

func (l *dataLease) validateWriteSnapshot(s *contract.RowSnapshot) (dataIdentity, error) {
	if s == nil || !slices.Equal(s.Columns, []string{"id", "vector", "payload", "point"}) {
		return dataIdentity{}, ErrInvalid
	}
	i, err := l.decodeRef(s.Ref)
	if err != nil || s.Metadata != i.Observation {
		return i, ErrInvalid
	}
	nullHash, _ := cellFingerprint(contract.Cell{Kind: contract.Null})
	switch s.Mode {
	case "fingerprints":
		if len(s.Original) != 0 || len(s.Fingerprints) != 4 {
			return i, ErrInvalid
		}
		for n, h := range s.Fingerprints {
			if h == nil {
				if i.Fingerprints[n] != nullHash {
					return i, ErrInvalid
				}
			} else if *h != i.Fingerprints[n] || i.Fingerprints[n] == nullHash {
				return i, ErrInvalid
			}
		}
	case "values":
		if len(s.Fingerprints) != 0 || len(s.Original) != 4 || !reflect.DeepEqual(s.Original[0], i.IDValue) {
			return i, ErrInvalid
		}
		for n, v := range s.Original {
			if v.Truncated || len(v.Bytes) != 0 || len(v.Text) > contract.DataEditMaxBytes {
				return i, ErrInvalid
			}
			h, err := cellFingerprint(v)
			if err != nil || h != i.Fingerprints[n] {
				return i, ErrInvalid
			}
		}
	default:
		return i, ErrInvalid
	}
	return i, nil
}

func pointIDKey(id []byte) string {
	id = bytes.TrimSpace(id)
	if len(id) > 0 && id[0] == '"' {
		var value string
		if decodeJSON(id, &value) == nil {
			return "uuid:" + strings.ToLower(value)
		}
	}
	return "uint64:" + string(id)
}
func samePointID(a, b []byte) bool {
	return validPointID(a) && validPointID(b) && pointIDKey(a) == pointIDKey(b)
}
func pointMissing(response Response, err error) bool {
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 404 || response.Status != 404 {
		return false
	}
	f, e := object(response.Body)
	s, e2 := object(f["status"])
	var message string
	return e == nil && e2 == nil && decodeJSON(s["error"], &message) == nil && strings.HasPrefix(message, "Not found: Point with id ") && strings.HasSuffix(message, " does not exists!")
}
func copyWriteResult(r contract.DataEditResult) *contract.DataEditResult {
	raw, _ := json.Marshal(r)
	var out contract.DataEditResult
	_ = json.Unmarshal(raw, &out)
	return &out
}

func (l *dataLease) save(ctx context.Context, id int64, db, name string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
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
		return copyWriteResult(l.write.result), contract.ErrDataCommitUncertain
	}
	edits, err = copyDataEdits(edits)
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
	w := &dataWrite{edits: edits, specs: specs}
	writes := make([]PointWrite, 0, len(edits))
	seen := map[string]bool{}
	size := 0
	for _, edit := range edits {
		var original Point
		var identity dataIdentity
		if edit.Original != nil {
			identity, err = l.validateWriteSnapshot(edit.Original)
			if err != nil {
				return nil, err
			}
			original, err = l.originalPoint(ctx, c, identity)
			if err != nil {
				return nil, err
			}
			size += len(original.JSON)
			if len(original.JSON) > contract.DataEditMaxBytes || size > requestLimit {
				return nil, ErrBounds
			}
		}
		p := original
		var vectors map[string]writeVector
		if edit.Kind != "delete" {
			raw := []byte(edit.Values["point"].Text)
			points, e := parsePoints(append(append([]byte{'['}, raw...), ']'), 1)
			if e != nil || len(points) != 1 {
				return nil, ErrInvalid
			}
			p = points[0]
			if !validWritePayload(p.Payload) {
				return nil, ErrInvalid
			}
			vectors, err = decodeWriteVectors(p.Vector, specs, true)
			if err != nil {
				return nil, err
			}
			if edit.Kind == "update" && !samePointID(p.ID, identity.ID) {
				return nil, ErrInvalid
			}
			if edit.Kind == "insert" {
				_, response, e := c.ReadPoint(ctx, db, p.ID)
				if e == nil {
					return nil, contract.ErrDataConflict
				}
				if !pointMissing(response, e) {
					return nil, e
				}
			}
			size += len(p.JSON)
			if size > requestLimit {
				return nil, ErrBounds
			}
			mode := "update_only"
			if edit.Kind == "insert" {
				mode = "insert_only"
			}
			writes = append(writes, PointWrite{Action: "upsert", Mode: mode, Points: []json.RawMessage{p.JSON}})
		} else {
			writes = append(writes, PointWrite{Action: "delete", IDs: []json.RawMessage{p.ID}})
		}
		key := pointIDKey(p.ID)
		if seen[key] {
			return nil, ErrInvalid
		}
		seen[key] = true
		w.points = append(w.points, p)
		w.vectors = append(w.vectors, vectors)
	}
	batch, err := PreparePointBatch(db, writes)
	if err != nil {
		return nil, err
	}
	if _, err = l.guard(ctx, c); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	// Preflight is a live check, not CAS. From this boundary, retain every outcome.
	w.result.CommitUncertain = true
	l.write = w
	receipt, response, err := c.SubmitPointBatch(ctx, batch)
	w.receipt = receipt
	status := "interrupted"
	if err == nil {
		status = "acknowledged"
		if receipt.Completed {
			status = "completed"
		}
	}
	ids := make([]string, len(receipt.Operations))
	for i, op := range receipt.Operations {
		ids[i] = strconv.FormatUint(op.OperationID, 10)
	}
	// Preserve even incomplete/non-JSON HTTP bytes without feeding untrusted
	// RawMessage into signing or losing status/content type.
	body, encoding := originalBody(response.Body)
	receiptJSON, _ := json.Marshal(struct {
		HTTPStatus         int
		ContentType        string
		Incomplete         bool
		Body, BodyEncoding string
	}{response.Status, response.ContentType, response.Incomplete, body, encoding})
	w.result.Async = &contract.DataWriteObservation{TaskID: strings.Join(ids, ", "), Status: status, ReceiptJSON: string(receiptJSON)}
	w.result.Async.Token = l.sign(struct {
		Object                        contract.ObjectRef
		Observation, Request, Receipt string
	}{l.object, l.binding(), fingerprint(batch.request.Body), string(receiptJSON)})
	return copyWriteResult(w.result), err
}

// Verify performs one explicit bounded readback against the original physical
// collection. It never polls operations or submits another mutation.
func (l *dataLease) verifyWrite(ctx context.Context, id int64, db, name string, result *contract.DataEditResult) (out *contract.DataVerification, err error) {
	if err = l.identity(id, db, name); err != nil {
		return nil, err
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
	w := l.write
	if w == nil || result == nil || result.Async == nil || !hmac.Equal([]byte(result.Async.Token), []byte(w.result.Async.Token)) || result.Async.TaskID != w.result.Async.TaskID || result.Async.ReceiptJSON != w.result.Async.ReceiptJSON {
		return nil, ErrInvalid
	}
	out = &contract.DataVerification{Status: "indeterminate"}
	type observedPoint struct {
		ID         json.RawMessage
		State      string
		HTTPStatus int
		Response   json.RawMessage
	}
	evidence := []observedPoint{}
	evidenceBytes := 0
	defer func() {
		raw, _ := json.Marshal(evidence)
		w.result.Async.TaskJSON = string(raw)
		out.Recovered = copyWriteResult(w.result)
		if out.Status == "applied" {
			out.Recovered.CommitUncertain = false
			out.Recovered.CommitConfirmed = true
			out.Recovered.Verified = true
			out.Recovered.RefreshRequired = true
		}
	}()
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return out, err
	}
	defer closeClient()
	if _, err = l.guard(ctx, c); err != nil {
		return out, err
	}
	budget := responseLimit
	matches := true
	saved := []contract.DataEditOutcome{}
	for i, expected := range w.points {
		actual, response, readErr := c.ReadPoint(ctx, db, expected.ID)
		evidenceBytes += len(response.Body)
		if evidenceBytes > responseLimit/2 {
			return out, ErrBounds
		}
		state := "diverged"
		outcome := contract.DataEditOutcome{Kind: w.edits[i].Kind, Original: w.edits[i].Original}
		missing := pointMissing(response, readErr)
		if w.edits[i].Kind == "delete" {
			if missing {
				state = "absent"
			} else {
				matches = false
			}
		} else if missing {
			state = "missing"
			matches = false
		} else if readErr == nil {
			vectors, e := decodeWriteVectors(actual.Vector, w.specs, false)
			if e == nil && samePointID(expected.ID, actual.ID) && payloadEqual(expected.Payload, actual.Payload) && vectorsMatch(w.vectors[i], vectors, w.specs) {
				state = "matched"
				previews, snapshot, e := l.preview(actual, &budget)
				if e != nil {
					return out, e
				}
				outcome.Saved, outcome.Previews, outcome.PreviewColumns = snapshot, previews, []string{"id", "vector", "payload", "point"}
			} else {
				matches = false
			}
		} else {
			state = "unreadable"
			matches = false
		}
		// Preserve valid original JSON; a truncated/non-JSON body is a JSON string.
		raw := json.RawMessage(bytes.Clone(response.Body))
		if !json.Valid(raw) {
			body, encoding := originalBody(raw)
			raw, _ = json.Marshal(struct{ Body, BodyEncoding string }{body, encoding})
		}
		evidence = append(evidence, observedPoint{bytes.Clone(expected.ID), state, response.Status, raw})
		out.States = append(out.States, state)
		if readErr != nil && !missing {
			return out, readErr
		}
		saved = append(saved, outcome)
	}
	if _, err = l.guard(ctx, c); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if !w.receipt.Completed || !matches {
		return out, nil
	}
	w.result.Applied, w.result.Outcomes = len(saved), saved
	l.pageMu.Lock()
	l.epoch++
	l.pageMu.Unlock()
	l.write = nil
	out.Status = "applied"
	return out, nil
}

// JSON strings replace invalid UTF-8; preserve those original bytes as base64.
func originalBody(raw []byte) (string, string) {
	if utf8.Valid(raw) {
		return string(raw), "utf-8"
	}
	return base64.StdEncoding.EncodeToString(raw), "base64"
}
