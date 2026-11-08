package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
)

// SearchResult preserves complete native hits, exact numbers and all response
// fields. These hits are a bounded search result, never a full-export promise.
type SearchResult struct {
	Hits []json.RawMessage
	Raw  json.RawMessage
}

// Search certifies only the constructed native POST as read-only. Raw Do POST
// requests remain conservatively mutating, including custom request paths.
func (c *Client) Search(ctx context.Context, indexUID string, body []byte) (SearchResult, Response, error) {
	if !validIndex(indexUID) || len(body) > requestLimit {
		return SearchResult{}, Response{}, ErrInvalid
	}
	if _, err := nativeObject(body); err != nil {
		return SearchResult{}, Response{}, ErrInvalid
	}
	response, err := c.do(ctx, Request{Method: "POST", Path: "/indexes/" + indexUID + "/search", Body: body}, true)
	if err != nil {
		return SearchResult{}, response, err
	}
	fields, err := nativeObject(response.Body)
	if err != nil {
		return SearchResult{}, response, err
	}
	var hits []json.RawMessage
	if decodeJSON(fields["hits"], &hits) != nil || hits == nil {
		return SearchResult{}, response, ErrProtocol
	}
	for _, hit := range hits {
		if _, err := nativeObject(hit); err != nil {
			return SearchResult{}, response, err
		}
	}
	if _, err := nativeString(fields["query"]); err != nil {
		return SearchResult{}, response, err
	}
	if _, err := nativeUint(fields["processingTimeMs"], 64); err != nil {
		return SearchResult{}, response, err
	}
	return SearchResult{Hits: hits, Raw: bytes.Clone(response.Body)}, response, nil
}
