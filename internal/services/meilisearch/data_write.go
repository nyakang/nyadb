package meilisearch

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// One lease owns one unresolved native batch. Admission, task observation and
// readback are distinct; neither cancellation nor a lost response permits replay.
type dataWrite struct {
	edits   []contract.DataEdit
	docs    []dataDocument
	receipt Receipt
	result  contract.DataEditResult
}

func copyDataEdits(edits []contract.DataEdit) ([]contract.DataEdit, error) {
	if len(edits) < 1 || len(edits) > contract.DataMaxPageSize {
		return nil, ErrBounds
	}
	raw, err := json.Marshal(edits)
	if err != nil || len(raw) > 3*requestLimit {
		return nil, ErrBounds
	}
	var out []contract.DataEdit
	if json.Unmarshal(raw, &out) != nil {
		return nil, ErrInvalid
	}
	deleting := out[0].Kind == "delete"
	for _, edit := range out {
		if edit.Kind != "insert" && edit.Kind != "update" && edit.Kind != "delete" {
			return nil, contract.ErrUnsupported
		}
		if (edit.Kind == "delete") != deleting {
			return nil, contract.ErrUnsupported
		}
		if edit.Kind == "insert" && edit.Original != nil || edit.Kind != "insert" && edit.Original == nil {
			return nil, ErrInvalid
		}
		if deleting {
			if len(edit.Values) != 0 {
				return nil, ErrInvalid
			}
			continue
		}
		value, ok := edit.Values["document"]
		if !ok || len(edit.Values) != 1 || value.Kind != contract.JSON || value.Null || value.Default || value.Binary || len(value.Bytes) != 0 {
			return nil, ErrInvalid
		}
		if len(value.Text) > contract.DataEditMaxBytes {
			return nil, contract.ErrDataEditLimit
		}
		if _, err := nativeObject([]byte(value.Text)); err != nil {
			return nil, ErrInvalid
		}
	}
	return out, nil
}

func (l *dataLease) validateWriteSnapshot(snapshot *contract.RowSnapshot) (dataIdentity, error) {
	if snapshot == nil || !slices.Equal(snapshot.Columns, []string{"id", "document"}) {
		return dataIdentity{}, ErrInvalid
	}
	identity, err := l.decodeRef(snapshot.Ref)
	if err != nil || snapshot.Metadata != identity.Binding {
		return identity, ErrInvalid
	}
	switch snapshot.Mode {
	case "fingerprints":
		if len(snapshot.Original) != 0 || len(snapshot.Fingerprints) != 2 {
			return identity, ErrInvalid
		}
		for i, hash := range snapshot.Fingerprints {
			if hash == nil || *hash != identity.Fingerprints[i] {
				return identity, ErrInvalid
			}
		}
	case "values":
		if len(snapshot.Fingerprints) != 0 || len(snapshot.Original) != 2 || !reflect.DeepEqual(snapshot.Original[0], identity.IDValue) {
			return identity, ErrInvalid
		}
		cell := snapshot.Original[1]
		if cell.Kind != contract.JSON || cell.Truncated || len(cell.Bytes) != 0 || len(cell.Text) > contract.DataEditMaxBytes {
			return identity, ErrInvalid
		}
		doc, err := l.document([]byte(cell.Text))
		if err != nil {
			return identity, ErrInvalid
		}
		hashes, err := documentFingerprints(doc)
		if err != nil || !slices.Equal(hashes, identity.Fingerprints) || !reflect.DeepEqual(doc.IDValue, identity.IDValue) {
			return identity, ErrInvalid
		}
	default:
		return identity, ErrInvalid
	}
	return identity, nil
}

func documentMissing(response Response, err error) bool {
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 404 || response.Status != 404 {
		return false
	}
	fields, e := nativeObject(response.Body)
	code, e2 := nativeString(fields["code"])
	return e == nil && e2 == nil && code == "document_not_found"
}

func copyWriteResult(result contract.DataEditResult) *contract.DataEditResult {
	// This model contains typed string/byte payloads, never generic JSON numbers.
	raw, _ := json.Marshal(result)
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
	if _, err = l.guard(ctx, c); err != nil {
		return nil, err
	}
	if _, ok := l.primaryKey(); !ok {
		return nil, contract.ErrUnsupported
	}
	w := &dataWrite{edits: edits}
	seen := map[string]bool{}
	payloadBytes := 2
	for _, edit := range edits {
		var doc dataDocument
		var identity dataIdentity
		if edit.Original != nil {
			identity, err = l.validateWriteSnapshot(edit.Original)
			if err != nil {
				return nil, err
			}
			doc, err = l.originalDocument(ctx, c, identity)
			if err != nil {
				return nil, err
			}
		}
		if edit.Kind != "delete" {
			doc, err = l.document([]byte(edit.Values["document"].Text))
			if err != nil {
				return nil, ErrInvalid
			}
			if edit.Kind == "update" && (doc.ID != identity.ID || !reflect.DeepEqual(doc.IDValue, identity.IDValue)) {
				return nil, ErrInvalid
			}
			if edit.Kind == "insert" {
				_, response, readErr := c.ReadDocument(ctx, db, doc.ID)
				if readErr == nil {
					return nil, contract.ErrDataConflict
				}
				if !documentMissing(response, readErr) {
					return nil, readErr
				}
			}
		}
		if edit.Kind == "delete" {
			doc.JSON = nil
		} else {
			payloadBytes += len(doc.JSON) + 1
			if payloadBytes > requestLimit {
				return nil, ErrBounds
			}
		}
		if seen[doc.ID] {
			return nil, ErrInvalid
		}
		seen[doc.ID] = true
		w.docs = append(w.docs, doc)
	}
	request := Request{Method: "POST", Path: "/indexes/" + db + "/documents"}
	if edits[0].Kind == "delete" {
		request.Path += "/delete-batch"
		ids := make([]json.RawMessage, len(w.docs))
		for i, doc := range w.docs {
			if doc.IDValue.Kind == contract.Integer {
				ids[i] = json.RawMessage(doc.ID)
			} else {
				ids[i], _ = json.Marshal(doc.ID)
			}
		}
		request.Body, err = json.Marshal(ids)
	} else {
		docs := make([]json.RawMessage, len(w.docs))
		for i, doc := range w.docs {
			docs[i] = doc.JSON
		}
		request.Body, err = json.Marshal(docs)
	}
	if err != nil || len(request.Body) > requestLimit {
		return nil, ErrBounds
	}
	if _, err = l.guard(ctx, c); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	// From this boundary on, failure may have occurred after server admission.
	w.result.CommitUncertain = true
	l.write = w
	receipt, response, err := c.Submit(ctx, request)
	if err != nil {
		if settingsRejected(response, err) {
			l.write = nil
			return nil, err
		}
		return copyWriteResult(w.result), err
	}
	kind := "documentAdditionOrUpdate"
	if edits[0].Kind == "delete" {
		kind = "documentDeletion"
	}
	if receipt.IndexUID == nil || *receipt.IndexUID != db || receipt.Type != kind {
		return copyWriteResult(w.result), ErrProtocol
	}
	w.receipt = receipt
	w.result.CommitUncertain = false
	w.result.Async = &contract.DataWriteObservation{TaskID: strconv.FormatUint(uint64(receipt.UID), 10), Status: "enqueued", ReceiptJSON: string(receipt.Raw)}
	w.result.Async.Token = l.sign(struct {
		Receipt Receipt
		Binding string
	}{receipt, l.binding()})
	return copyWriteResult(w.result), nil
}

// Verify performs one bounded read of the original task and, only on success,
// reads the actual saved documents. It never polls or submits on its own.
func (l *dataLease) verifyWrite(ctx context.Context, id int64, db, name string, result *contract.DataEditResult) (*contract.DataVerification, error) {
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
	w := l.write
	if w == nil || result == nil {
		return nil, ErrInvalid
	}
	if w.result.Async == nil {
		return &contract.DataVerification{Status: "indeterminate"}, nil
	}
	if result.Async == nil || !hmac.Equal([]byte(result.Async.Token), []byte(w.result.Async.Token)) || result.Async.TaskID != w.result.Async.TaskID || result.Async.ReceiptJSON != w.result.Async.ReceiptJSON {
		return nil, ErrInvalid
	}
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	if _, err = l.guard(ctx, c); err != nil {
		return nil, err
	}
	task, err := c.ReadTask(ctx, w.receipt)
	if err != nil {
		return nil, err
	}
	w.result.Async.Status, w.result.Async.TaskJSON = task.Status, string(task.Raw)
	out := &contract.DataVerification{Status: "indeterminate", Recovered: copyWriteResult(w.result)}
	if task.Status == "enqueued" || task.Status == "processing" {
		out.Status = "pending"
		return out, nil
	}
	if task.Status != "succeeded" {
		// Failure/cancellation is evidence, not permission to repeat this batch.
		return out, nil
	}
	saved := contract.DataEditResult{CommitConfirmed: true, Verified: true, RefreshRequired: true, Applied: len(w.edits), Async: w.result.Async}
	budget := responseLimit
	for i, expected := range w.docs {
		outcome := contract.DataEditOutcome{Kind: w.edits[i].Kind, Original: w.edits[i].Original}
		raw, response, err := c.ReadDocument(ctx, db, expected.ID)
		if w.edits[i].Kind == "delete" {
			if !documentMissing(response, err) {
				return out, nil
			}
		} else {
			if err != nil {
				return out, err
			}
			actual, err := l.document(raw)
			if err != nil {
				return out, err
			}
			a, e1 := documentFingerprints(actual)
			b, e2 := documentFingerprints(expected)
			if e1 != nil || e2 != nil || !slices.Equal(a, b) {
				return out, nil
			}
			previews, snapshot, err := l.preview(actual, &budget)
			if err != nil {
				return out, err
			}
			outcome.Saved, outcome.Previews, outcome.PreviewColumns = snapshot, previews, []string{"id", "document"}
		}
		saved.Outcomes = append(saved.Outcomes, outcome)
	}
	if _, err = l.guard(ctx, c); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	l.write = nil
	out.Status, out.Recovered = "applied", copyWriteResult(saved)
	return out, nil
}
