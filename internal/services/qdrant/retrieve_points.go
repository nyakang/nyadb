package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
)

// RetrievePoints captures exact original IDs, certifies only the native read
// endpoint, and rejects duplicate or foreign response identities. Missing IDs
// are omitted by Qdrant; response ordering is not meaningful.
func (c *Client) RetrievePoints(ctx context.Context, collection string, ids []json.RawMessage) ([]Point, Response, error) {
	path, err := collectionPath(collection)
	if err != nil || len(ids) == 0 || len(ids) > 100 {
		return nil, Response{}, ErrInvalid
	}
	captured := make([]json.RawMessage, len(ids))
	seen := map[string]bool{}
	for i, id := range ids {
		if !validPointID(id) || seen[pointIDKey(id)] {
			return nil, Response{}, ErrInvalid
		}
		seen[pointIDKey(id)] = true
		captured[i] = bytes.Clone(id)
	}
	body, err := json.Marshal(map[string]any{"ids": captured, "with_payload": true, "with_vector": true})
	if err != nil || len(body) > requestLimit {
		return nil, Response{}, ErrBounds
	}
	r, err := c.do(ctx, Request{Method: "POST", Path: path + "/points", Body: body}, true)
	if err != nil {
		return nil, r, err
	}
	raw, err := NativeResult(r.Body)
	if err != nil {
		return nil, r, err
	}
	points, err := parsePoints(raw, len(ids))
	if err != nil {
		return nil, r, err
	}
	returned := map[string]bool{}
	for _, p := range points {
		key := pointIDKey(p.ID)
		if !seen[key] || returned[key] {
			return nil, r, ErrProtocol
		}
		returned[key] = true
	}
	return points, r, nil
}
