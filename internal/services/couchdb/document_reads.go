package couchdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// NativeDocument preserves the server's complete JSON and original revision.
// It contains attachment stubs, not separately downloaded attachment bodies.
type NativeDocument struct {
	ID, Revision string
	JSON         []byte
}

// AllDocumentsPage is a live native observation, not a database incarnation or
// a consistent snapshot shared with another request. Design documents are included.
type AllDocumentsPage struct {
	Documents      []NativeDocument
	TotalRows      int64
	UpdateSequence []byte
	NextKey        string
	HasMore        bool
}

type FindRequest struct {
	Selector json.RawMessage
	Sort     json.RawMessage
	Bookmark string
	Limit    int
}
type FindPage struct {
	Documents []NativeDocument
	Bookmark  string
	Warning   string
	HasMore   bool
}

func documentPath(database, id string) (string, error) {
	if !validDatabase(database) || id == "" || len(id) > 4096 {
		return "", ErrInvalid
	}
	if strings.HasPrefix(id, "_design/") {
		return designPath(database, id)
	}
	if strings.HasPrefix(id, "_") {
		return "", ErrInvalid
	}
	path := "/" + url.PathEscape(database) + "/" + url.PathEscape(id)
	return path, validateRequest(Request{Method: "GET", Path: path})
}

func nativeDocumentJSON(database string, raw []byte) (NativeDocument, error) {
	var fields struct {
		ID       string `json:"_id"`
		Revision string `json:"_rev"`
		Deleted  bool   `json:"_deleted"`
	}
	if err := decodeJSON(raw, &fields); err != nil {
		return NativeDocument{}, err
	}
	if !validRevision(fields.Revision) || fields.Deleted {
		return NativeDocument{}, ErrProtocol
	}
	if _, err := documentPath(database, fields.ID); err != nil {
		return NativeDocument{}, ErrProtocol
	}
	return NativeDocument{ID: fields.ID, Revision: fields.Revision, JSON: bytes.Clone(raw)}, nil
}

func validRevision(revision string) bool {
	generation, hash, ok := strings.Cut(revision, "-")
	n, err := strconv.ParseUint(generation, 10, 64)
	if !ok || err != nil || n == 0 || strconv.FormatUint(n, 10) != generation || len(hash) != 32 {
		return false
	}
	for _, ch := range hash {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

// AllDocuments uses inclusive startkey and removes the old boundary only when
// it is actually returned. Deleting that row never skips its successor.
func (c *Client) AllDocuments(ctx context.Context, database, start string, limit int) (*AllDocumentsPage, error) {
	if !validDatabase(database) || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	if start != "" {
		if _, err := documentPath(database, start); err != nil {
			return nil, err
		}
	}
	query := url.Values{"include_docs": {"true"}, "conflicts": {"true"}, "update_seq": {"true"}, "limit": {strconv.Itoa(limit + 2)}}
	if start != "" {
		key, _ := json.Marshal(start)
		query.Set("startkey", string(key))
	}
	response, err := c.Do(ctx, Request{Method: "GET", Path: "/" + url.PathEscape(database) + "/_all_docs?" + query.Encode()})
	if err != nil {
		return nil, err
	}
	var native struct {
		TotalRows      *json.Number    `json:"total_rows"`
		Offset         *json.Number    `json:"offset"`
		UpdateSequence json.RawMessage `json:"update_seq"`
		Rows           []struct {
			ID, Key string
			Value   struct {
				Revision string `json:"rev"`
				Deleted  bool   `json:"deleted"`
			}
			Document json.RawMessage `json:"doc"`
		}
	}
	if err := decodeJSON(response.Body, &native); err != nil {
		return nil, err
	}
	if native.TotalRows == nil || native.Offset == nil || native.Rows == nil || len(native.Rows) > limit+2 || len(native.UpdateSequence) == 0 || len(native.UpdateSequence) > 8192 || bytes.Equal(native.UpdateSequence, []byte("null")) {
		return nil, ErrProtocol
	}
	total, e1 := native.TotalRows.Int64()
	offset, e2 := native.Offset.Int64()
	if e1 != nil || e2 != nil || total < 0 || offset < 0 {
		return nil, ErrProtocol
	}
	docs := make([]NativeDocument, 0, len(native.Rows))
	previous := ""
	for i, row := range native.Rows {
		if row.ID != row.Key || row.Value.Deleted || i > 0 && row.Key <= previous || start != "" && row.Key < start {
			return nil, ErrProtocol
		}
		doc, err := nativeDocumentJSON(database, row.Document)
		if err != nil {
			return nil, err
		}
		if doc.ID != row.ID || doc.Revision != row.Value.Revision {
			return nil, ErrProtocol
		}
		previous = row.Key
		docs = append(docs, doc)
	}
	if start != "" && len(docs) > 0 && docs[0].ID == start {
		docs = docs[1:]
	}
	page := &AllDocumentsPage{TotalRows: total, UpdateSequence: bytes.Clone(native.UpdateSequence), HasMore: len(docs) > limit}
	if page.HasMore {
		docs = docs[:limit]
		page.NextKey = docs[len(docs)-1].ID
	}
	page.Documents = docs
	return page, ctx.Err()
}

// Find certifies this narrowly constructed POST as a read. A failed request
// still retires the original client and is never automatically replayed.
func (c *Client) Find(ctx context.Context, database string, request FindRequest) (*FindPage, error) {
	if !validDatabase(database) || request.Limit < 1 || request.Limit > 1000 || len(request.Bookmark) > 64<<10 || !utf8.ValidString(request.Bookmark) {
		return nil, ErrInvalid
	}
	var selector map[string]json.RawMessage
	if err := decodeJSON(request.Selector, &selector); err != nil || selector == nil {
		return nil, ErrInvalid
	}
	if len(request.Sort) > 0 {
		var sort []json.RawMessage
		if err := decodeJSON(request.Sort, &sort); err != nil || sort == nil {
			return nil, ErrInvalid
		}
	}
	body, err := json.Marshal(struct {
		Selector json.RawMessage `json:"selector"`
		Sort     json.RawMessage `json:"sort,omitempty"`
		Bookmark string          `json:"bookmark,omitempty"`
		Limit    int             `json:"limit"`
	}{bytes.Clone(request.Selector), bytes.Clone(request.Sort), request.Bookmark, request.Limit})
	if err != nil || len(body) > requestLimit {
		return nil, ErrInvalid
	}
	response, err := c.Do(ctx, Request{Method: "POST", Path: "/" + url.PathEscape(database) + "/_find", Body: body})
	if err != nil {
		var interrupted *Interrupted
		if errors.As(err, &interrupted) {
			return nil, &Interrupted{Cause: interrupted.Cause, WriteOutcomeUnknown: false}
		}
		return nil, err
	}
	var native struct {
		Documents []json.RawMessage `json:"docs"`
		Bookmark  string            `json:"bookmark"`
		Warning   string            `json:"warning"`
	}
	if err := decodeJSON(response.Body, &native); err != nil {
		return nil, err
	}
	if native.Documents == nil || len(native.Documents) > request.Limit || native.Bookmark == "" || len(native.Bookmark) > 64<<10 || len(native.Documents) > 0 && request.Bookmark != "" && request.Bookmark == native.Bookmark {
		return nil, ErrProtocol
	}
	page := &FindPage{Bookmark: native.Bookmark, Warning: native.Warning, HasMore: len(native.Documents) == request.Limit, Documents: make([]NativeDocument, 0, len(native.Documents))}
	seen := map[string]bool{}
	for _, raw := range native.Documents {
		doc, err := nativeDocumentJSON(database, raw)
		if err != nil {
			return nil, err
		}
		if seen[doc.ID] {
			return nil, ErrProtocol
		}
		seen[doc.ID] = true
		page.Documents = append(page.Documents, doc)
	}
	return page, ctx.Err()
}
