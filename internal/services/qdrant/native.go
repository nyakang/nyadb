package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func collectionPath(name string) (string, error) {
	if name == "" || len(name) > 255 || !utf8.ValidString(name) || name == "." || name == ".." || strings.ContainsAny(name, "/\\%") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", ErrInvalid
	}
	return "/collections/" + url.PathEscape(name), nil
}

func (c *Client) Collections(ctx context.Context) ([]string, Response, error) {
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/collections"})
	if err != nil {
		return nil, r, err
	}
	result, err := NativeResult(r.Body)
	var native struct {
		Collections []struct{ Name string } `json:"collections"`
	}
	if err != nil || decodeJSON(result, &native) != nil || native.Collections == nil {
		return nil, r, ErrProtocol
	}
	seen := map[string]bool{}
	names := make([]string, 0, len(native.Collections))
	for _, item := range native.Collections {
		if _, err := collectionPath(item.Name); err != nil || seen[item.Name] {
			return nil, r, ErrProtocol
		}
		seen[item.Name] = true
		names = append(names, item.Name)
	}
	sort.Strings(names)
	return names, r, nil
}

// CollectionInfo retains the complete native configuration, including named,
// sparse and multi-vector schemas, payload indexes and shard/replica settings.
// Counters and configuration are live observations, not collection identity.
type CollectionInfo struct {
	Name   string
	Result json.RawMessage
}

func (c *Client) Collection(ctx context.Context, name string) (CollectionInfo, Response, error) {
	path, err := collectionPath(name)
	if err != nil {
		return CollectionInfo{}, Response{}, err
	}
	r, err := c.Do(ctx, Request{Method: "GET", Path: path})
	if err != nil {
		return CollectionInfo{}, r, err
	}
	result, err := NativeResult(r.Body)
	fields, e := object(result)
	var status string
	_, configErr := object(fields["config"])
	if err != nil || e != nil || configErr != nil || decodeJSON(fields["status"], &status) != nil || status == "" {
		return CollectionInfo{}, r, ErrProtocol
	}
	return CollectionInfo{Name: name, Result: result}, r, nil
}

type Alias struct{ Alias, Collection string }

func (c *Client) Aliases(ctx context.Context) ([]Alias, Response, error) {
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/aliases"})
	if err != nil {
		return nil, r, err
	}
	result, err := NativeResult(r.Body)
	var native struct {
		Aliases []struct {
			Alias      string `json:"alias_name"`
			Collection string `json:"collection_name"`
		} `json:"aliases"`
	}
	if err != nil || decodeJSON(result, &native) != nil || native.Aliases == nil {
		return nil, r, ErrProtocol
	}
	seen := map[string]bool{}
	aliases := make([]Alias, 0, len(native.Aliases))
	for _, item := range native.Aliases {
		_, e1 := collectionPath(item.Alias)
		_, e2 := collectionPath(item.Collection)
		if e1 != nil || e2 != nil || seen[item.Alias] {
			return nil, r, ErrProtocol
		}
		seen[item.Alias] = true
		aliases = append(aliases, Alias{item.Alias, item.Collection})
	}
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].Alias < aliases[j].Alias })
	return aliases, r, nil
}

func validPointID(raw []byte) bool {
	if len(raw) == 0 || len(raw) > 40 {
		return false
	}
	if raw[0] != '"' {
		n, err := strconv.ParseUint(string(raw), 10, 64)
		return err == nil && strconv.FormatUint(n, 10) == string(raw)
	}
	var id string
	if decodeJSON(raw, &id) != nil || len(id) != 36 {
		return false
	}
	for i, ch := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
		} else if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
			return false
		}
	}
	return true
}

// Point preserves a uint64 or UUID ID, payload/vector JSON, native scores,
// versions and shard keys. A point version is not a conditional-write token.
type Point struct {
	ID, Payload, Vector, JSON json.RawMessage
}
type PointPage struct {
	Points     []Point
	NextOffset json.RawMessage
}

func parsePoints(raw []byte, limit int) ([]Point, error) {
	var rows []json.RawMessage
	if decodeJSON(raw, &rows) != nil || rows == nil || len(rows) > limit {
		return nil, ErrProtocol
	}
	points := make([]Point, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		fields, err := object(row)
		id := bytes.TrimSpace(fields["id"])
		if err != nil || !validPointID(id) || seen[string(id)] {
			return nil, ErrProtocol
		}
		seen[string(id)] = true
		if len(fields["payload"]) != 0 && !bytes.Equal(fields["payload"], []byte("null")) {
			if _, err := object(fields["payload"]); err != nil {
				return nil, ErrProtocol
			}
		}
		points = append(points, Point{ID: bytes.Clone(id), Payload: bytes.Clone(fields["payload"]), Vector: bytes.Clone(fields["vector"]), JSON: bytes.Clone(row)})
	}
	return points, nil
}

// Scroll uses the native next_page_offset verbatim. It is a live ordered scan,
// never a row count/skip, frozen snapshot, or a cross-collection cursor.
func (c *Client) Scroll(ctx context.Context, collection string, offset json.RawMessage, limit int, filter json.RawMessage) (PointPage, Response, error) {
	path, err := collectionPath(collection)
	if err != nil || limit < 1 || limit > 1000 || len(offset) > 40 || len(offset) > 0 && !validPointID(bytes.TrimSpace(offset)) {
		return PointPage{}, Response{}, ErrInvalid
	}
	input := map[string]any{"limit": limit, "with_payload": true, "with_vector": true}
	if len(offset) > 0 {
		input["offset"] = json.RawMessage(bytes.Clone(offset))
	}
	if len(filter) > 0 {
		if _, err := object(filter); err != nil {
			return PointPage{}, Response{}, ErrInvalid
		}
		input["filter"] = json.RawMessage(bytes.Clone(filter))
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) > requestLimit {
		return PointPage{}, Response{}, ErrInvalid
	}
	r, err := c.do(ctx, Request{Method: "POST", Path: path + "/points/scroll", Body: body}, true)
	if err != nil {
		return PointPage{}, r, err
	}
	result, err := NativeResult(r.Body)
	fields, e := object(result)
	if err != nil || e != nil {
		return PointPage{}, r, ErrProtocol
	}
	points, err := parsePoints(fields["points"], limit)
	next := bytes.TrimSpace(fields["next_page_offset"])
	if err != nil || len(next) == 0 || !bytes.Equal(next, []byte("null")) && !validPointID(next) {
		return PointPage{}, r, ErrProtocol
	}
	if bytes.Equal(next, []byte("null")) {
		next = nil
	}
	if len(next) > 0 && (len(points) == 0 || bytes.Equal(next, bytes.TrimSpace(offset))) {
		return PointPage{}, r, ErrProtocol
	}
	return PointPage{Points: points, NextOffset: bytes.Clone(next)}, r, nil
}

// Query uses Qdrant's complete native query API, including filters, prefetch,
// named vectors, sparse/multi-vector queries and result projection. It never
// rewrites a native score into a SQL cost or mutation acknowledgement.
func (c *Client) Query(ctx context.Context, collection string, body json.RawMessage) ([]Point, Response, error) {
	path, err := collectionPath(collection)
	if err != nil || len(body) > requestLimit {
		return nil, Response{}, ErrInvalid
	}
	if _, err = object(body); err != nil {
		return nil, Response{}, ErrInvalid
	}
	r, err := c.do(ctx, Request{Method: "POST", Path: path + "/points/query", Body: bytes.Clone(body)}, true)
	if err != nil {
		return nil, r, err
	}
	result, err := NativeResult(r.Body)
	fields, e := object(result)
	if err != nil || e != nil {
		return nil, r, ErrProtocol
	}
	points, err := parsePoints(fields["points"], 10000)
	return points, r, err
}

func (c *Client) Count(ctx context.Context, collection string, filter json.RawMessage) (uint64, Response, error) {
	path, err := collectionPath(collection)
	if err != nil {
		return 0, Response{}, err
	}
	input := map[string]any{"exact": true}
	if len(filter) > 0 {
		if _, err := object(filter); err != nil {
			return 0, Response{}, ErrInvalid
		}
		input["filter"] = json.RawMessage(bytes.Clone(filter))
	}
	body, _ := json.Marshal(input)
	r, err := c.do(ctx, Request{Method: "POST", Path: path + "/points/count", Body: body}, true)
	if err != nil {
		return 0, r, err
	}
	result, err := NativeResult(r.Body)
	fields, e := object(result)
	n, parseErr := strconv.ParseUint(string(fields["count"]), 10, 64)
	if err != nil || e != nil || parseErr != nil || strconv.FormatUint(n, 10) != string(fields["count"]) {
		return 0, r, ErrProtocol
	}
	return n, r, nil
}

type MutationReceipt struct {
	Collection, Status string
	OperationID        uint64
	Raw                json.RawMessage
}

// SubmitPoints certifies only native point upsert/delete envelopes. Completed
// means the server reports operation completion; acknowledged means admission.
// Neither supplies conditional writes or an immutable collection identity.
func (c *Client) SubmitPoints(ctx context.Context, collection, action string, body json.RawMessage, wait bool) (MutationReceipt, Response, error) {
	path, err := collectionPath(collection)
	method := "PUT"
	switch action {
	case "upsert":
		path += "/points"
	case "delete":
		method, path = "POST", path+"/points/delete"
	default:
		return MutationReceipt{}, Response{}, ErrInvalid
	}
	if err != nil {
		return MutationReceipt{}, Response{}, err
	}
	if _, err := object(body); err != nil {
		return MutationReceipt{}, Response{}, ErrInvalid
	}
	r, err := c.Do(ctx, Request{Method: method, Path: path + "?wait=" + strconv.FormatBool(wait), Body: bytes.Clone(body)})
	if err != nil {
		return MutationReceipt{}, r, err
	}
	result, err := NativeResult(r.Body)
	fields, e := object(result)
	var status string
	n, idErr := strconv.ParseUint(string(fields["operation_id"]), 10, 64)
	if r.Status != 200 || err != nil || e != nil || idErr != nil || strconv.FormatUint(n, 10) != string(fields["operation_id"]) || decodeJSON(fields["status"], &status) != nil || status != "acknowledged" && status != "completed" || wait && status != "completed" {
		c.retire()
		return MutationReceipt{}, r, &Interrupted{WriteOutcomeUnknown: true, Cause: ErrProtocol}
	}
	return MutationReceipt{Collection: collection, Status: status, OperationID: n, Raw: bytes.Clone(r.Body)}, r, nil
}
