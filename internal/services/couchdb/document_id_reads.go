package couchdb

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"unicode/utf8"
)

type DocumentIdentity struct{ ID, Revision string }
type DocumentIdentityPage struct {
	Documents         []DocumentIdentity
	TotalRows         int64
	UpdateSequence    []byte
	NextKey, Bookmark string
	HasMore           bool
}

// Identity pages never accumulate complete bodies. Reading each captured revision
// separately keeps a page/export bounded even when it contains many large documents.
func (c *Client) AllDocumentIdentities(ctx context.Context, database, start string, limit int, descending bool) (*DocumentIdentityPage, error) {
	if !validDatabase(database) || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	if start != "" {
		if _, err := documentPath(database, start); err != nil {
			return nil, err
		}
	}
	query := url.Values{"update_seq": {"true"}, "limit": {strconv.Itoa(limit + 2)}}
	if descending {
		query.Set("descending", "true")
	}
	if start != "" {
		key, _ := json.Marshal(start)
		query.Set("startkey", string(key))
	}
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/" + url.PathEscape(database) + "/_all_docs?" + query.Encode()})
	if err != nil {
		return nil, err
	}
	var native struct {
		Total    *json.Number    `json:"total_rows"`
		Offset   *json.Number    `json:"offset"`
		Sequence json.RawMessage `json:"update_seq"`
		Rows     []struct {
			ID, Key string
			Value   struct {
				Revision string `json:"rev"`
				Deleted  bool   `json:"deleted"`
			}
		}
	}
	if err := decodeJSON(r.Body, &native); err != nil {
		return nil, err
	}
	if native.Rows == nil || len(native.Rows) > limit+2 || native.Total == nil || native.Offset == nil || len(native.Sequence) == 0 || len(native.Sequence) > 8192 || bytes.Equal(native.Sequence, []byte("null")) {
		return nil, ErrProtocol
	}
	total, e1 := native.Total.Int64()
	offset, e2 := native.Offset.Int64()
	if e1 != nil || e2 != nil || total < 0 || offset < 0 {
		return nil, ErrProtocol
	}
	docs := make([]DocumentIdentity, 0, len(native.Rows))
	previous := ""
	for i, row := range native.Rows {
		if row.ID != row.Key || row.Value.Deleted || !validRevision(row.Value.Revision) {
			return nil, ErrProtocol
		}
		if _, err := documentPath(database, row.ID); err != nil {
			return nil, ErrProtocol
		}
		if i > 0 && (!descending && row.ID <= previous || descending && row.ID >= previous) || start != "" && (!descending && row.ID < start || descending && row.ID > start) {
			return nil, ErrProtocol
		}
		previous = row.ID
		docs = append(docs, DocumentIdentity{row.ID, row.Value.Revision})
	}
	if start != "" && len(docs) > 0 && docs[0].ID == start {
		docs = docs[1:]
	}
	page := &DocumentIdentityPage{TotalRows: total, UpdateSequence: bytes.Clone(native.Sequence), HasMore: len(docs) > limit}
	if page.HasMore {
		docs = docs[:limit]
		page.NextKey = docs[len(docs)-1].ID
	}
	page.Documents = docs
	return page, ctx.Err()
}

// ReadDocument requires the captured revision and never falls back to the winner
// if compaction, deletion or replacement made the original revision unavailable.
func (c *Client) ReadDocument(ctx context.Context, database string, identity DocumentIdentity) (NativeDocument, error) {
	path, err := documentPath(database, identity.ID)
	if err != nil || !validRevision(identity.Revision) {
		return NativeDocument{}, ErrInvalid
	}
	r, err := c.Do(ctx, Request{Method: "GET", Path: path + "?rev=" + url.QueryEscape(identity.Revision) + "&conflicts=true"})
	if err != nil {
		return NativeDocument{}, err
	}
	doc, err := nativeDocumentJSON(database, r.Body)
	if err != nil {
		return NativeDocument{}, err
	}
	if doc.ID != identity.ID || doc.Revision != identity.Revision {
		return NativeDocument{}, ErrProtocol
	}
	return doc, ctx.Err()
}

func (c *Client) FindDocumentIdentities(ctx context.Context, database string, request FindRequest) (*DocumentIdentityPage, error) {
	if !validDatabase(database) || request.Limit < 1 || request.Limit > 1000 || len(request.Bookmark) > 64<<10 || !utf8.ValidString(request.Bookmark) {
		return nil, ErrInvalid
	}
	var selector map[string]json.RawMessage
	if decodeJSON(request.Selector, &selector) != nil || selector == nil {
		return nil, ErrInvalid
	}
	if len(request.Sort) > 0 {
		var sort []json.RawMessage
		if decodeJSON(request.Sort, &sort) != nil || sort == nil {
			return nil, ErrInvalid
		}
	}
	body, err := json.Marshal(struct {
		Selector json.RawMessage `json:"selector"`
		Sort     json.RawMessage `json:"sort,omitempty"`
		Bookmark string          `json:"bookmark,omitempty"`
		Limit    int             `json:"limit"`
		Fields   []string        `json:"fields"`
	}{bytes.Clone(request.Selector), bytes.Clone(request.Sort), request.Bookmark, request.Limit, []string{"_id", "_rev"}})
	if err != nil || len(body) > requestLimit {
		return nil, ErrInvalid
	}
	r, err := c.do(ctx, Request{Method: "POST", Path: "/" + url.PathEscape(database) + "/_find", Body: body}, true)
	if err != nil {
		return nil, err
	}
	var native struct {
		Documents []struct {
			ID       string `json:"_id"`
			Revision string `json:"_rev"`
		} `json:"docs"`
		Bookmark string `json:"bookmark"`
	}
	if err := decodeJSON(r.Body, &native); err != nil {
		return nil, err
	}
	if native.Documents == nil || len(native.Documents) > request.Limit || native.Bookmark == "" || len(native.Bookmark) > 64<<10 || len(native.Documents) > 0 && request.Bookmark != "" && request.Bookmark == native.Bookmark {
		return nil, ErrProtocol
	}
	page := &DocumentIdentityPage{Bookmark: native.Bookmark, HasMore: len(native.Documents) == request.Limit, Documents: make([]DocumentIdentity, 0, len(native.Documents))}
	seen := map[string]bool{}
	for _, doc := range native.Documents {
		if _, err := documentPath(database, doc.ID); err != nil || !validRevision(doc.Revision) || seen[doc.ID] {
			return nil, ErrProtocol
		}
		seen[doc.ID] = true
		page.Documents = append(page.Documents, DocumentIdentity{doc.ID, doc.Revision})
	}
	return page, ctx.Err()
}
