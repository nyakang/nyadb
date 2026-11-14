// Package consulspec owns pure native Consul archive and import rules.
package consulspec

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid Consul bundle or complete observation")

const BundleLimit = 64 << 20
const BundleValuesLimit = 32 << 20
const BundleEntriesLimit = 10000

type Bundle struct {
	Format     string        `json:"format"`
	Version    int           `json:"version"`
	Prefix     string        `json:"prefix"`
	ScopeKind  string        `json:"scope_kind"`
	Datacenter string        `json:"datacenter"`
	Namespace  string        `json:"namespace"`
	Partition  string        `json:"partition"`
	Entries    []BundleEntry `json:"entries"`
}
type BundleEntry struct {
	Key         string `json:"key"`
	Value       string `json:"value_base64"`
	Flags       string `json:"flags"`
	CreateIndex string `json:"create_index"`
	ModifyIndex string `json:"modify_index"`
}

func (b Bundle) Clone() Bundle { b.Entries = slices.Clone(b.Entries); return b }
func exactUnsigned(s string) bool {
	n, e := strconv.ParseUint(s, 10, 64)
	return e == nil && strconv.FormatUint(n, 10) == s
}
func validKey(s string) bool {
	return s != "" && len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func ValidateBundle(b Bundle) error {
	if b.Format != "nyadb-consul-kv-bundle" || b.Version != 1 || b.ScopeKind != "key" && b.ScopeKind != "prefix" || len(b.Entries) > BundleEntriesLimit || b.Entries == nil || len(b.Prefix) > 4096 || !utf8.ValidString(b.Prefix) || strings.ContainsRune(b.Prefix, 0) {
		return ErrInvalid
	}
	seen := map[string]bool{}
	size := 0
	for _, e := range b.Entries {
		if !validKey(e.Key) || seen[e.Key] || !exactUnsigned(e.Flags) || !exactUnsigned(e.CreateIndex) || !exactUnsigned(e.ModifyIndex) || b.ScopeKind == "key" && e.Key != b.Prefix || b.ScopeKind == "prefix" && !strings.HasPrefix(e.Key, b.Prefix) {
			return ErrInvalid
		}
		seen[e.Key] = true
		v, err := base64.StdEncoding.Strict().DecodeString(e.Value)
		if err != nil || base64.StdEncoding.EncodeToString(v) != e.Value || len(v) > 512<<10 {
			return ErrInvalid
		}
		size += len(v)
		if size > BundleValuesLimit {
			return ErrInvalid
		}
	}
	if b.ScopeKind == "key" && len(b.Entries) != 1 {
		return ErrInvalid
	}
	return nil
}
func DecodeBundle(raw []byte) (Bundle, error) {
	var b Bundle
	if jsondocument.Decode(raw, &b, BundleLimit) != nil {
		return b, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&b) != nil || d.Decode(new(any)) != io.EOF {
		return b, ErrInvalid
	}
	// Null or absent fields cannot silently become an empty key/value.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 8 {
		return b, ErrInvalid
	}
	for _, k := range []string{"format", "prefix", "scope_kind", "datacenter", "namespace", "partition"} {
		v := bytes.TrimSpace(fields[k])
		if len(v) == 0 || v[0] != '"' {
			return b, ErrInvalid
		}
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(fields["entries"], &entries) != nil {
		return b, ErrInvalid
	}
	for _, e := range entries {
		if len(e) != 5 {
			return b, ErrInvalid
		}
		for _, v := range e {
			v = bytes.TrimSpace(v)
			if len(v) == 0 || v[0] != '"' {
				return b, ErrInvalid
			}
		}
	}
	return b, ValidateBundle(b)
}
func Envelope(r contract.KVHTTPRequest) (contract.KVRequest, error) {
	e := struct {
		Method string              `json:"method"`
		Path   string              `json:"path"`
		Query  map[string][]string `json:"query,omitempty"`
		Body   *string             `json:"body_base64,omitempty"`
	}{Method: r.Method, Path: r.Path, Query: r.Query}
	if r.Body != nil {
		v := base64.StdEncoding.EncodeToString(r.Body)
		e.Body = &v
	}
	raw, err := json.MarshalIndent(e, "", "  ")
	return contract.KVRequest{Method: contract.KVConsulHTTPRequestMethod, JSON: string(raw)}, err
}
func ReadRequest(key string, prefix bool) (contract.KVRequest, error) {
	canonical := true
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			canonical = false
		}
	}
	if key == "" {
		canonical = true
	}
	if !canonical {
		raw, _ := json.Marshal([]map[string]any{{"KV": map[string]any{"Verb": "get-tree", "Key": key}}})
		return Envelope(contract.KVHTTPRequest{Method: "PUT", Path: "/v1/txn", Body: raw})
	}
	q := map[string][]string(nil)
	if prefix {
		q = map[string][]string{"recurse": {""}}
	}
	return Envelope(contract.KVHTTPRequest{Method: "GET", Path: "/v1/kv/" + url.PathEscape(key), Query: q})
}
func readTarget(h *contract.KVHTTPResult) (string, bool) {
	if h == nil {
		return "", false
	}
	if h.Request.Method == "GET" && strings.HasPrefix(h.Request.Path, "/v1/kv/") {
		key, e := url.PathUnescape(strings.TrimPrefix(h.Request.Path, "/v1/kv/"))
		return key, e == nil
	}
	if h.Request.Method == "PUT" && h.Request.Path == "/v1/txn" {
		var ops []struct{ KV struct{ Verb, Key string } }
		if json.Unmarshal(h.Request.Body, &ops) == nil && len(ops) == 1 && ops[0].KV.Verb == "get-tree" {
			return ops[0].KV.Key, true
		}
	}
	return "", false
}
func ReadEntries(r contract.KVResult) []contract.KVHTTPEntry {
	if r.HTTP == nil {
		return nil
	}
	if r.HTTP.Txn != nil {
		return r.HTTP.Txn.Results
	}
	return r.HTTP.Entries
}
func CompleteRead(r contract.KVResult) bool {
	h := r.HTTP
	_, supported := readTarget(h)
	return supported && h.Ended && h.Captured && h.EOF && h.Acknowledged && h.Succeeded && h.CaptureError == "" && h.ProjectionError == "" && (h.Metadata.ACLFiltered == nil || !*h.Metadata.ACLFiltered) && (h.Status == 200 || h.Status == 404) && (h.Txn == nil || h.Txn.Committed)
}
func ExportBundle(scope contract.KVScope, key string, prefix bool, r contract.KVResult) (Bundle, error) {
	b := Bundle{Format: "nyadb-consul-kv-bundle", Version: 1, Prefix: key, ScopeKind: "key", Datacenter: scope.HTTP.Datacenter, Namespace: scope.HTTP.Namespace, Partition: scope.HTTP.Partition, Entries: []BundleEntry{}}
	if prefix {
		b.ScopeKind = "prefix"
	}
	if !CompleteRead(r) {
		return b, ErrInvalid
	}
	target, ok := readTarget(r.HTTP)
	if !ok || target != key {
		return b, ErrInvalid
	}
	for _, e := range ReadEntries(r) {
		if !prefix && e.Key != key {
			continue
		}
		if !e.ValuePresent || e.ValueNull || !slices.Contains(e.PresentFields, "Flags") || !slices.Contains(e.PresentFields, "ModifyIndex") || !slices.Contains(e.PresentFields, "CreateIndex") {
			return b, ErrInvalid
		}
		b.Entries = append(b.Entries, BundleEntry{e.Key, base64.StdEncoding.EncodeToString(e.Value), strconv.FormatUint(e.Flags, 10), strconv.FormatUint(e.CreateIndex, 10), strconv.FormatUint(e.ModifyIndex, 10)})
	}
	return b, ValidateBundle(b)
}

type ImportRow struct {
	Key, Operation string
	ExpectedIndex  uint64
	Session        string
}
type ImportPreview struct {
	Bundle   Bundle
	Policy   string
	Rows     []ImportRow
	Requests []contract.KVRequest
	CanApply bool
}

func (p ImportPreview) Clone() ImportPreview {
	p.Bundle = p.Bundle.Clone()
	p.Rows = slices.Clone(p.Rows)
	p.Requests = slices.Clone(p.Requests)
	return p
}
func PlanImport(b Bundle, policy string, observations []contract.KVResult) (ImportPreview, error) {
	p := ImportPreview{Bundle: b.Clone(), Policy: policy, CanApply: true}
	if ValidateBundle(b) != nil || policy != "abort" && policy != "skip" && policy != "cas" || len(observations) != len(b.Entries) {
		return p, ErrInvalid
	}
	var pending []map[string]any
	for i, source := range b.Entries {
		r := observations[i]
		if !CompleteRead(r) || !sameReadTarget(r.HTTP, source.Key) {
			return p, ErrInvalid
		}
		value, _ := base64.StdEncoding.DecodeString(source.Value)
		flags, _ := strconv.ParseUint(source.Flags, 10, 64)
		row := ImportRow{Key: source.Key, Operation: "create"}
		var found []contract.KVHTTPEntry
		for _, e := range ReadEntries(r) {
			if e.Key == source.Key {
				found = append(found, e)
			}
		}
		if r.HTTP.Status == 404 || len(found) == 0 {
			if len(found) != 0 {
				return p, ErrInvalid
			}
		} else {
			if len(found) != 1 {
				return p, ErrInvalid
			}
			target := found[0]
			if target.Key != source.Key || !slices.Contains(target.PresentFields, "ModifyIndex") || target.ModifyIndex == 0 || target.SessionNull || !slices.Contains(target.PresentFields, "Flags") || !target.ValuePresent {
				return p, ErrInvalid
			}
			row.ExpectedIndex = target.ModifyIndex
			row.Session = target.Session
			switch {
			case target.Session != "":
				row.Operation = "locked"
			case !target.ValueNull && bytes.Equal(target.Value, value) && target.Flags == flags:
				row.Operation = "unchanged"
			case policy == "abort":
				row.Operation = "conflict"
			case policy == "skip":
				row.Operation = "skipped"
			default:
				row.Operation = "update"
			}
		}
		if row.Operation == "conflict" || row.Operation == "locked" {
			p.CanApply = false
		}
		p.Rows = append(p.Rows, row)
		if row.Operation == "create" || row.Operation == "update" {
			pending = append(pending, map[string]any{"KV": map[string]any{"Verb": "cas", "Key": source.Key, "Value": source.Value, "Flags": flags, "Index": row.ExpectedIndex}})
		}
	}
	// Every native batch is independent and explicitly confirmed. Both operation
	// count and encoded request bytes are limited before it reaches a provider.
	for len(pending) > 0 {
		count := min(64, len(pending))
		for count > 0 {
			raw, err := json.Marshal(pending[:count])
			if err != nil {
				return p, ErrInvalid
			}
			r, err := Envelope(contract.KVHTTPRequest{Method: "PUT", Path: "/v1/txn", Body: raw})
			if err != nil {
				return p, err
			}
			if len(r.JSON) <= 8<<20 {
				p.Requests = append(p.Requests, r)
				break
			}
			count--
		}
		if count == 0 {
			return p, ErrInvalid
		}
		pending = pending[count:]
	}
	return p, nil
}

type SearchRequest struct {
	Prefix, Query               string
	Keys, Values, CaseSensitive bool
	Limit, MaxScan              int
}
type SearchMatch struct {
	Entry      contract.KVHTTPEntry
	Key, Value bool
}
type SearchResult struct {
	Request           SearchRequest
	Original          contract.KVResult
	Matches           []SearchMatch
	Scanned, Matched  int
	Limited, Canceled bool
}

func (r SearchResult) Clone() SearchResult {
	r.Original = r.Original.Clone()
	r.Matches = slices.Clone(r.Matches)
	for i := range r.Matches {
		r.Matches[i].Entry = r.Matches[i].Entry.Clone()
	}
	return r
}
func ValidateSearch(r SearchRequest) error {
	if r.Query == "" || len(r.Query) > 4096 || !utf8.ValidString(r.Query) || !r.Keys && !r.Values || r.Limit < 1 || r.Limit > 1000 || r.MaxScan < 1 || r.MaxScan > BundleEntriesLimit {
		return ErrInvalid
	}
	return nil
}
func Search(r SearchRequest, original contract.KVResult, check func(int) error) (SearchResult, error) {
	out := SearchResult{Request: r, Original: original.Clone()}
	if ValidateSearch(r) != nil || !CompleteRead(original) {
		return out, ErrInvalid
	}
	normalize := func(s string) string {
		if !r.CaseSensitive {
			return strings.ToLower(s)
		}
		return s
	}
	query := normalize(r.Query)
	if !sameReadTarget(original.HTTP, r.Prefix) {
		return out, ErrInvalid
	}
	for _, e := range ReadEntries(original) {
		if out.Scanned >= r.MaxScan {
			out.Limited = true
			break
		}
		if check != nil {
			if err := check(out.Scanned); err != nil {
				out.Canceled = true
				return out, err
			}
		}
		out.Scanned++
		if !strings.HasPrefix(e.Key, r.Prefix) {
			return out, ErrInvalid
		}
		key := r.Keys && strings.Contains(normalize(e.Key), query)
		value := r.Values && e.ValuePresent && !e.ValueNull && utf8.Valid(e.Value) && strings.Contains(normalize(string(e.Value)), query)
		if key || value {
			out.Matched++
			if len(out.Matches) < r.Limit {
				out.Matches = append(out.Matches, SearchMatch{e.Clone(), key, value})
			} else {
				out.Limited = true
			}
		}
	}
	if check != nil {
		if err := check(out.Scanned); err != nil {
			out.Canceled = true
			return out, err
		}
	}
	return out, nil
}

func sameReadTarget(h *contract.KVHTTPResult, key string) bool {
	target, ok := readTarget(h)
	return ok && target == key
}
