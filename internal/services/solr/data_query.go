package solr

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type projection struct {
	Key      contract.Cell
	Version  json.Number
	Document json.RawMessage
}
type projectionBatch struct {
	Documents []projection
	Mark      string
	Total     int64
}

func dataExpression(o contract.DataOptions) (*contract.FilterExpression[contract.DataFilterRule], error) {
	if o.Offset < 0 || o.Limit < 1 || o.Limit > 500 || o.SortColumn != "" || o.SortDescending || len(o.Sorts) > 0 {
		return nil, contract.ErrUnsupported
	}
	if o.Condition == nil && o.FilterMode == "" && len(o.Filters) == 0 && (o.FilterColumn != "" || o.FilterValue != "") {
		return nil, ErrInvalid
	}
	e, err := contract.EffectiveDataExpression(o)
	if err != nil {
		return nil, ErrInvalid
	}
	if e != nil {
		rules, err := e.Rules(contract.DataMaxFilterRules, 3)
		if err != nil {
			return nil, ErrInvalid
		}
		for _, rule := range rules {
			if rule.Column != "_key" || rule.Mode != "equal" && rule.Mode != "not_equal" || len(rule.Value) > 512 || !utf8.ValidString(rule.Value) || strings.ContainsRune(rule.Value, 0) {
				return nil, contract.ErrUnsupported
			}
		}
	}
	return e.Clone(func(r contract.DataFilterRule) contract.DataFilterRule { return r }), nil
}
func nativeQuery(e *contract.FilterExpression[contract.DataFilterRule], key string) any {
	if e == nil {
		return "*:*"
	}
	if e.Rule != nil {
		term := map[string]any{"term": map[string]string{"f": key, "query": e.Rule.Value}}
		if e.Rule.Mode == "not_equal" {
			return map[string]any{"bool": map[string]any{"must": "*:*", "must_not": term}}
		}
		return term
	}
	parts := make([]any, 0, len(e.Children))
	for i := range e.Children {
		parts = append(parts, nativeQuery(&e.Children[i], key))
	}
	if e.Op == "or" {
		return map[string]any{"bool": map[string]any{"should": parts, "mm": 1}}
	}
	return map[string]any{"bool": map[string]any{"must": parts}}
}
func (c *Client) selectProjection(ctx context.Context, target string, u readUnit, e *contract.FilterExpression[contract.DataFilterRule], mark string, limit int) (projectionBatch, error) {
	if !validTargetName(target) || !validTargetName(u.Member) || !nativeFieldName.MatchString(u.Key) || limit < 0 || limit > 500 || len(mark) > 8192 {
		return projectionBatch{}, ErrInvalid
	}
	params := map[string]any{"sort": u.Key + " asc", "start": 0, "shards.tolerant": false, "echoParams": "none", "distrib": false}
	if mark != "" {
		params["cursorMark"] = mark
	}
	if u.Shard == "" {
		params["shards"] = ""
	}
	if u.Shard != "" {
		if !validTargetName(u.Shard) {
			return projectionBatch{}, ErrInvalid
		}
		params["distrib"] = true
	}
	body, _ := json.Marshal(map[string]any{"query": nativeQuery(e, u.Key), "limit": limit, "fields": []string{"*", u.Key, "_version_"}, "params": params, "facet": map[string]any{"nyadb_duplicates": map[string]any{"type": "terms", "field": u.Key, "limit": 1, "mincount": 2, "sort": "count desc"}}})
	routing := url.Values{"wt": {"json"}, "distrib": {strconv.FormatBool(u.Shard != "")}, "shards": {u.Shard}}
	if u.Shard != "" {
		routing.Set("collection", u.Member)
		routing.Set("shards", u.Shard)
	}
	response, err := c.Do(ctx, Request{Method: "POST", Path: "/" + url.PathEscape(target) + "/select?" + routing.Encode(), Body: body})
	if err != nil {
		return projectionBatch{}, err
	}
	var receipt struct {
		Header *struct {
			Status  *int
			Partial json.RawMessage `json:"partialResults"`
		} `json:"responseHeader"`
		Response *struct {
			Total *int64            `json:"numFound"`
			Exact *bool             `json:"numFoundExact"`
			Start *int64            `json:"start"`
			Docs  []json.RawMessage `json:"docs"`
		} `json:"response"`
		Mark   string `json:"nextCursorMark"`
		Facets *struct {
			Count      *int64
			Duplicates *struct{ Buckets []json.RawMessage } `json:"nyadb_duplicates"`
		} `json:"facets"`
	}
	if decodeJSON(response.Body, &receipt) != nil || receipt.Header == nil || receipt.Header.Status == nil || *receipt.Header.Status != 0 || len(receipt.Header.Partial) > 0 && !bytes.Equal(receipt.Header.Partial, []byte("false")) || receipt.Response == nil || receipt.Response.Total == nil || *receipt.Response.Total < 0 || receipt.Response.Exact == nil || !*receipt.Response.Exact || receipt.Response.Start == nil || *receipt.Response.Start != 0 || receipt.Response.Docs == nil || len(receipt.Response.Docs) > limit || receipt.Facets == nil || receipt.Facets.Count == nil || *receipt.Facets.Count != *receipt.Response.Total || receipt.Facets.Duplicates == nil || receipt.Facets.Duplicates.Buckets == nil {
		return projectionBatch{}, ErrProtocol
	}
	if len(receipt.Facets.Duplicates.Buckets) > 0 && limit > 0 {
		return projectionBatch{}, ErrDuplicateKey
	}
	if mark != "" && (receipt.Mark == "" || len(receipt.Mark) > 8192 || receipt.Mark == mark && len(receipt.Response.Docs) != 0 || receipt.Mark != mark && len(receipt.Response.Docs) == 0) {
		return projectionBatch{}, ErrProtocol
	}
	out := projectionBatch{Mark: receipt.Mark, Total: *receipt.Response.Total}
	seen := map[string]bool{}
	for _, raw := range receipt.Response.Docs {
		var values map[string]json.RawMessage
		if decodeJSON(raw, &values) != nil || values == nil {
			return projectionBatch{}, ErrProtocol
		}
		key, err := keyCell(values[u.Key])
		if err != nil {
			return projectionBatch{}, err
		}
		var nativeVersion any
		if decodeJSON(values["_version_"], &nativeVersion) != nil {
			return projectionBatch{}, ErrProtocol
		}
		version, ok := nativeVersion.(json.Number)
		if !ok {
			return projectionBatch{}, ErrProtocol
		}
		if n, err := version.Int64(); err != nil || n <= 0 {
			return projectionBatch{}, ErrProtocol
		}
		sign := string(key.Kind) + ":" + key.Text
		if seen[sign] {
			return projectionBatch{}, ErrDuplicateKey
		}
		seen[sign] = true
		out.Documents = append(out.Documents, projection{Key: key, Version: version, Document: bytes.Clone(raw)})
	}
	return out, ctx.Err()
}
func keyCell(raw []byte) (contract.Cell, error) {
	var v any
	if decodeJSON(raw, &v) != nil {
		return contract.Cell{}, ErrProtocol
	}
	switch key := v.(type) {
	case string:
		if len(key) > 512 || strings.ContainsRune(key, 0) {
			return contract.Cell{}, ErrBounds
		}
		return contract.Cell{Kind: contract.Text, Text: key}, nil
	case json.Number:
		if _, err := strconv.ParseInt(key.String(), 10, 64); err != nil {
			return contract.Cell{}, contract.ErrUnsupported
		}
		return contract.Cell{Kind: contract.Integer, Text: key.String()}, nil
	default:
		return contract.Cell{}, ErrProtocol
	}
}
