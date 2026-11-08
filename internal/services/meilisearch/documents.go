package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strconv"
)

const documentPageLimit = 1000

// DocumentsRequest explicitly bounds one native enumeration page. Limit zero
// requests only the current total. Enumeration has no atomic snapshot guarantee.
type DocumentsRequest struct {
	Offset uint64
	Limit  int
}

// DocumentsPage retains complete native document objects and the original
// envelope. Total is an observation, distinct from search's maxTotalHits limit.
type DocumentsPage struct {
	Documents     []json.RawMessage
	Offset, Total uint64
	Limit         int
	Raw           json.RawMessage
}

func validDocumentID(id string) bool {
	if len(id) == 0 || len(id) > 511 {
		return false
	}
	for i := range len(id) {
		b := id[i]
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_') {
			return false
		}
	}
	return true
}
func (c *Client) documentIndex(index string) error {
	if !validIndex(index) || c.config.IndexUID != "" && c.config.IndexUID != index {
		return ErrInvalid
	}
	return nil
}

// ListDocuments uses the native documents endpoint, never bounded search hits.
// A caller traversing pages must detect changing totals/duplicate identities;
// the native offset/total tuple alone is not a snapshot or incarnation guard.
func (c *Client) ListDocuments(ctx context.Context, index string, request DocumentsRequest) (DocumentsPage, Response, error) {
	if err := c.documentIndex(index); err != nil {
		return DocumentsPage{}, Response{}, err
	}
	if request.Limit < 0 || request.Limit > documentPageLimit || request.Offset > math.MaxInt64 {
		return DocumentsPage{}, Response{}, ErrBounds
	}
	path := "/indexes/" + index + "/documents?offset=" + strconv.FormatUint(request.Offset, 10) + "&limit=" + strconv.Itoa(request.Limit)
	response, err := c.Do(ctx, Request{Method: "GET", Path: path})
	if err != nil {
		return DocumentsPage{}, response, err
	}
	if response.Status != 200 {
		return DocumentsPage{}, response, ErrProtocol
	}
	fields, err := nativeObject(response.Body)
	if err != nil {
		return DocumentsPage{}, response, err
	}
	offset, e1 := nativeUint(fields["offset"], 64)
	limit, e2 := nativeUint(fields["limit"], 64)
	total, e3 := nativeUint(fields["total"], 64)
	var documents []json.RawMessage
	if e1 != nil || e2 != nil || e3 != nil || offset != request.Offset || limit != uint64(request.Limit) || decodeJSON(fields["results"], &documents) != nil || documents == nil {
		return DocumentsPage{}, response, ErrProtocol
	}
	if total > math.MaxInt64 {
		return DocumentsPage{}, response, ErrBounds
	}
	expected := uint64(0)
	if offset < total {
		expected = min(limit, total-offset)
	}
	if uint64(len(documents)) != expected {
		return DocumentsPage{}, response, ErrProtocol
	}
	for i, raw := range documents {
		if _, err = nativeObject(raw); err != nil {
			return DocumentsPage{}, response, err
		}
		documents[i] = bytes.Clone(raw)
	}
	return DocumentsPage{Documents: documents, Offset: offset, Limit: request.Limit, Total: total, Raw: bytes.Clone(response.Body)}, response, nil
}

// ReadDocument accepts the native ID's exact ASCII spelling. Integer IDs are
// supplied as decimal strings; the returned JSON preserves their original type
// and precision. Missing IDs remain explicit native HTTP errors.
func (c *Client) ReadDocument(ctx context.Context, index, id string) (json.RawMessage, Response, error) {
	if err := c.documentIndex(index); err != nil {
		return nil, Response{}, err
	}
	if !validDocumentID(id) {
		return nil, Response{}, ErrInvalid
	}
	response, err := c.Do(ctx, Request{Method: "GET", Path: "/indexes/" + index + "/documents/" + id})
	if err != nil {
		return nil, response, err
	}
	if response.Status != 200 {
		return nil, response, ErrProtocol
	}
	if _, err = nativeObject(response.Body); err != nil {
		return nil, response, err
	}
	return bytes.Clone(response.Body), response, nil
}
