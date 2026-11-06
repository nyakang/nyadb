package redis

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/rediscommand"
)

const previewUnits = 1024

func preview(text string) contract.Cell {
	c := contract.Cell{Kind: contract.JSON, Text: text}
	r := []rune(text)
	if len(r) > previewUnits {
		c.Text = string(r[:previewUnits])
		c.Truncated = true
	}
	return c
}

// All Redis bytes use base64 in value documents, including keys and hash fields.
// This avoids ambiguity between a literal JSON object and a binary marker.
type valueDocument struct {
	String *string        `json:"string,omitempty"`
	Hash   [][2]string    `json:"hash,omitempty"`
	List   []string       `json:"list,omitempty"`
	Set    []string       `json:"set,omitempty"`
	ZSet   []scoredMember `json:"zset,omitempty"`
	Stream []streamEntry  `json:"stream,omitempty"`
}
type scoredMember struct {
	Member string `json:"member"`
	Score  string `json:"score"`
}
type streamEntry struct {
	ID     string      `json:"id"`
	Fields [][2]string `json:"fields"`
}
type keyRecord struct {
	key, kind, dump, value string
	expiry, ttl            int64
	complete               bool
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
func unb64(s string) (string, error) {
	b, e := base64.StdEncoding.DecodeString(s)
	if e != nil || b64(string(b)) != s {
		return "", errors.New("Redis bytes require canonical base64")
	}
	return string(b), nil
}
func cols() []contract.StructureColumn {
	return []contract.StructureColumn{
		{Name: "key", Type: "base64", DataType: "text", ValueKind: contract.Text, Key: "PRI", Position: 1, Comment: "Raw key bytes encoded as base64"},
		{Name: "type", Type: "Redis type", DataType: "text", ValueKind: contract.Text, Position: 2},
		{Name: "ttl_ms", Type: "milliseconds", DataType: "bigint", ValueKind: contract.Integer, Position: 3, Comment: "Remaining TTL; -1 is persistent. Omit this field in an update to preserve the absolute expiry."},
		{Name: "value", Type: "Redis JSON", DataType: "json", ValueKind: contract.JSON, Position: 4, Comment: "Typed JSON; every byte string is encoded as base64. Stream IDs and scores remain text."},
	}
}
func (r keyRecord) cells() []contract.DataCell {
	values := []string{b64(r.key), r.kind, strconv.FormatInt(r.ttl, 10), r.value}
	out := make([]contract.DataCell, 4)
	for i, v := range values {
		c := preview(v)
		c.Kind = cols()[i].ValueKind
		if i == 3 && !r.complete {
			c.Truncated = true
		}
		out[i] = contract.DataCell{Text: c.Text, Truncated: c.Truncated, Value: &c}
	}
	return out
}
func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func (s *Session) snapshot(o contract.ObjectRef, r keyRecord) *contract.RowSnapshot {
	ref := contract.RowRef{Object: o, Database: o.Catalog, Table: o.Name, Columns: []string{"key"}, Values: [][]byte{[]byte(r.key)}, TypedValues: []contract.Cell{{Kind: contract.Text, Text: b64(r.key)}}}
	vals := []string{digest(r.key), digest(r.kind), digest(strconv.FormatInt(r.expiry, 10)), digest(r.value), digest(r.dump)}
	snap := &contract.RowSnapshot{Mode: "fingerprints", Ref: ref, Columns: []string{"key", "type", "ttl_ms", "value"}, Fingerprints: make([]*string, 4)}
	for i := range 4 {
		snap.Fingerprints[i] = &vals[i]
	}
	metadata := vals[4] + ":" + strconv.FormatInt(r.expiry, 10)
	snap.Metadata = metadata + ":" + s.signature(snap, metadata)
	return snap
}
func (s *Session) signature(snap *contract.RowSnapshot, dumpHash string) string {
	mac := hmac.New(sha256.New, s.key[:])
	raw, _ := json.Marshal(struct {
		Ref          contract.RowRef
		Mode         string
		Columns      []string
		Fingerprints []*string
		Dump         string
	}{snap.Ref, snap.Mode, snap.Columns, snap.Fingerprints, dumpHash})
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}
func (s *Session) validateSnapshot(o contract.ObjectRef, snap *contract.RowSnapshot) error {
	if snap == nil || snap.Ref.Object != o || snap.Ref.Database != o.Catalog || snap.Ref.Table != o.Name || len(snap.Ref.Values) != 1 || len(snap.Ref.Columns) != 1 || snap.Ref.Columns[0] != "key" || snap.Mode != "fingerprints" || len(snap.Fingerprints) != 4 {
		return errors.New("invalid Redis original key")
	}
	for _, fp := range snap.Fingerprints {
		if fp == nil {
			return errors.New("invalid Redis original key")
		}
	}
	parts := strings.Split(snap.Metadata, ":")
	if len(parts) != 3 || !hmac.Equal([]byte(parts[2]), []byte(s.signature(snap, parts[0]+":"+parts[1]))) {
		return errors.New("Redis original key belongs to another session or was altered")
	}
	return nil
}
func matches(r keyRecord, snap *contract.RowSnapshot, strict bool) bool {
	if snap == nil || len(snap.Fingerprints) != 4 || !r.complete {
		return false
	}
	vals := []string{digest(r.key), digest(r.kind), digest(strconv.FormatInt(r.expiry, 10)), digest(r.value)}
	for i, v := range vals {
		if snap.Fingerprints[i] == nil || *snap.Fingerprints[i] != v {
			return false
		}
	}
	return !strict || strings.HasPrefix(snap.Metadata, digest(r.dump)+":")
}
func decodeValue(kind, text string) (valueDocument, error) {
	var v valueDocument
	if len(text) > contract.DataEditMaxBytes || !utf8.ValidString(text) {
		return v, errors.New("Redis value exceeds the 1 MiB editing limit")
	}
	if rediscommand.ValidateJSON(text) != nil {
		return v, errors.New("Redis JSON requires unique keys and bounded nesting")
	}
	d := json.NewDecoder(strings.NewReader(text))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil || d.Decode(new(any)) != io.EOF {
		return v, errors.New("invalid typed Redis JSON")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &fields) != nil || len(fields) != 1 || fields[kind] == nil {
		return v, errors.New("Redis value requires exactly one matching type field")
	}
	if kind == "list" || kind == "set" {
		if err := validateStrings(fields[kind]); err != nil {
			return v, err
		}
	}
	if kind == "zset" {
		var members []map[string]json.RawMessage
		if json.Unmarshal(fields[kind], &members) != nil {
			return v, errors.New("invalid Redis sorted set")
		}
		for _, m := range members {
			if len(m) != 2 || len(m["member"]) == 0 || m["member"][0] != '"' || len(m["score"]) == 0 || m["score"][0] != '"' {
				return v, errors.New("Redis sorted set records require member and score strings")
			}
		}
	}
	if kind == "hash" {
		if err := validatePairs(fields[kind]); err != nil {
			return v, err
		}
	}
	if kind == "stream" {
		var entries []map[string]json.RawMessage
		if json.Unmarshal(fields[kind], &entries) != nil {
			return v, errors.New("invalid Redis stream entries")
		}
		for _, entry := range entries {
			if len(entry) != 2 || entry["id"] == nil || entry["fields"] == nil {
				return v, errors.New("Redis stream entries require ID and fields")
			}
			if err := validatePairs(entry["fields"]); err != nil {
				return v, err
			}
		}
	}
	count := 0
	for _, ok := range []bool{v.String != nil, v.Hash != nil, v.List != nil, v.Set != nil, v.ZSet != nil, v.Stream != nil} {
		if ok {
			count++
		}
	}
	if count != 1 {
		return v, errors.New("Redis value must contain exactly one matching type")
	}
	check := func(s string) error { _, e := unb64(s); return e }
	var err error
	switch kind {
	case "string":
		if v.String == nil {
			err = contract.ErrUnsupported
		} else {
			err = check(*v.String)
		}
	case "hash":
		if len(v.Hash) == 0 {
			err = contract.ErrUnsupported
		}
		seen := map[string]bool{}
		for _, p := range v.Hash {
			if seen[p[0]] {
				return v, errors.New("duplicate Redis hash field")
			}
			seen[p[0]] = true
			if err = check(p[0]); err != nil {
				return v, err
			}
			if err = check(p[1]); err != nil {
				return v, err
			}
		}
		sort.Slice(v.Hash, func(i, j int) bool { return v.Hash[i][0] < v.Hash[j][0] })
	case "list":
		if len(v.List) == 0 {
			err = contract.ErrUnsupported
		}
		for _, p := range v.List {
			if err = check(p); err != nil {
				return v, err
			}
		}
	case "set":
		if len(v.Set) == 0 {
			err = contract.ErrUnsupported
		}
		sort.Strings(v.Set)
		for i, p := range v.Set {
			if i > 0 && v.Set[i-1] == p {
				return v, errors.New("duplicate Redis set member")
			}
			if err = check(p); err != nil {
				return v, err
			}
		}
	case "zset":
		if len(v.ZSet) == 0 {
			err = contract.ErrUnsupported
		}
		seen := map[string]bool{}
		for i, p := range v.ZSet {
			if seen[p.Member] {
				return v, errors.New("duplicate Redis sorted set member")
			}
			seen[p.Member] = true
			if err = check(p.Member); err != nil {
				return v, err
			}
			score, e := strconv.ParseFloat(p.Score, 64)
			if e != nil || math.IsNaN(score) {
				return v, errors.New("Redis score must be a number or signed infinity")
			}
			v.ZSet[i].Score = strconv.FormatFloat(score, 'g', -1, 64)
			if math.IsInf(score, 1) {
				v.ZSet[i].Score = "+inf"
			}
			if math.IsInf(score, -1) {
				v.ZSet[i].Score = "-inf"
			}
		}
		sort.Slice(v.ZSet, func(i, j int) bool { return v.ZSet[i].Member < v.ZSet[j].Member })
	case "stream":
		if v.Stream == nil {
			err = contract.ErrUnsupported
		}
		last := "0-0"
		for _, p := range v.Stream {
			if !validStreamID(p.ID) || !idGreater(p.ID, last) || len(p.Fields) == 0 {
				return v, errors.New("Redis stream requires increasing explicit IDs and nonempty fields")
			}
			last = p.ID
			for _, f := range p.Fields {
				if err = check(f[0]); err != nil {
					return v, err
				}
				if err = check(f[1]); err != nil {
					return v, err
				}
			}
		}
	default:
		err = contract.ErrUnsupported
	}
	return v, err
}
func validStreamID(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		n, e := strconv.ParseUint(p, 10, 64)
		if e != nil || strconv.FormatUint(n, 10) != p {
			return false
		}
	}
	return s != "0-0"
}
func idGreater(a, b string) bool {
	aa := strings.Split(a, "-")
	bb := strings.Split(b, "-")
	if len(aa) != 2 || len(bb) != 2 {
		return false
	}
	a0, _ := strconv.ParseUint(aa[0], 10, 64)
	b0, _ := strconv.ParseUint(bb[0], 10, 64)
	a1, _ := strconv.ParseUint(aa[1], 10, 64)
	b1, _ := strconv.ParseUint(bb[1], 10, 64)
	return a0 > b0 || a0 == b0 && a1 > b1
}
func encodeValue(v valueDocument) string {
	if v.Stream != nil && len(v.Stream) == 0 {
		return `{"stream":[]}`
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

func validatePairs(raw json.RawMessage) error {
	var pairs [][]json.RawMessage
	if json.Unmarshal(raw, &pairs) != nil {
		return errors.New("Redis fields require pairs of base64 strings")
	}
	for _, pair := range pairs {
		if len(pair) != 2 {
			return errors.New("Redis fields require exactly two base64 strings")
		}
		for _, value := range pair {
			if len(value) == 0 || value[0] != '"' {
				return errors.New("Redis field bytes require base64 strings")
			}
		}
	}
	return nil
}

func validateStrings(raw json.RawMessage) error {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return errors.New("Redis members require base64 strings")
	}
	for _, v := range values {
		if len(v) == 0 || v[0] != '"' {
			return errors.New("Redis members require base64 strings")
		}
	}
	return nil
}
