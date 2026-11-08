package meilisearch

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Import admits exactly one bounded native batch on the original data lease.
// Complete replacement and top-level field merge use distinct native methods;
// neither mode clears the index. The task and every expected document must be
// explicitly observed before the public workflow confirms completion.
func (l *dataLease) importDocuments(ctx context.Context, id int64, db, name string, r contract.DataImportRequest) (*contract.DataImportResult, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	if r.Mode != "document_replace" && r.Mode != "document_merge" || r.NewTable || r.ContinueOnError || len(r.Columns) != 0 || len(r.MatchColumns) != 0 {
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
	edits, err := copyDataEditsLimit(r.Edits, contract.MaxDataImportRows)
	if err != nil {
		return nil, err
	}
	for _, edit := range edits {
		if edit.Kind != "insert" || edit.Original != nil {
			return nil, ErrInvalid
		}
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
	w := &dataWrite{edits: edits, importing: true}
	seen := map[string]bool{}
	payload := make([]json.RawMessage, len(edits))
	expectedBytes, requestBytes := 0, 2
	for i, edit := range edits {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		doc, err := l.document([]byte(edit.Values["document"].Text))
		if err != nil {
			return nil, err
		}
		if seen[doc.ID] {
			return nil, ErrInvalid
		}
		seen[doc.ID] = true
		payload[i] = doc.JSON
		requestBytes += len(doc.JSON) + 1
		if requestBytes > requestLimit {
			return nil, ErrBounds
		}
		if r.Mode == "document_merge" {
			raw, response, readErr := c.ReadDocument(ctx, db, doc.ID)
			if readErr == nil {
				original, e := l.document(raw)
				if e != nil {
					return nil, e
				}
				if !reflect.DeepEqual(original.IDValue, doc.IDValue) {
					return nil, ErrInvalid
				}
				fields, e := nativeObject(original.JSON)
				if e != nil {
					return nil, e
				}
				updates, e := nativeObject(doc.JSON)
				if e != nil {
					return nil, e
				}
				for key, value := range updates {
					fields[key] = value
				}
				merged, e := json.Marshal(fields)
				if e != nil {
					return nil, e
				}
				if len(merged) > contract.DataEditMaxBytes {
					return nil, contract.ErrDataEditLimit
				}
				doc, err = l.document(merged)
				if err != nil {
					return nil, err
				}
			} else if !documentMissing(response, readErr) {
				return nil, readErr
			}
		}
		expectedBytes += len(doc.JSON)
		if expectedBytes > responseLimit {
			return nil, ErrBounds
		}
		w.docs = append(w.docs, doc)
	}
	body, err := json.Marshal(payload)
	if err != nil || len(body) > requestLimit {
		return nil, ErrBounds
	}
	method := "POST"
	if r.Mode == "document_merge" {
		method = "PUT"
	}
	saved, err := l.submitWrite(ctx, c, w, Request{Method: method, Path: "/indexes/" + db + "/documents", Body: body})
	if saved == nil {
		return nil, err
	}
	return &contract.DataImportResult{Saved: saved, CommitUncertain: saved.CommitUncertain}, err
}
