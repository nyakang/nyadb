package redis

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	redisdriver "github.com/redis/go-redis/v9"
)

const readScript = `
local k=KEYS[1]
local t=redis.call('TYPE',k).ok
if t=='none' then return false end
local expiry=redis.call('PEXPIRETIME',k)
local ttl=redis.call('PTTL',k)
local memory=redis.call('MEMORY','USAGE',k,'SAMPLES',0)
if memory and memory>1048576 then
 if t=='string' then return {t,expiry,ttl,'',redis.call('GETRANGE',k,0,767),0} end
 return {t,expiry,ttl,'','value exceeds complete editing limit',0}
end
local dump=redis.call('DUMP',k)
if string.len(dump)>1048576 then return {t,expiry,ttl,'','value exceeds complete editing limit',0} end
local v
if t=='string' then v=redis.call('GET',k)
elseif t=='hash' then v=redis.call('HGETALL',k)
elseif t=='list' then v=redis.call('LRANGE',k,0,-1)
elseif t=='set' then v=redis.call('SMEMBERS',k)
elseif t=='zset' then v=redis.call('ZRANGE',k,0,-1,'WITHSCORES')
elseif t=='stream' then v=redis.call('XRANGE',k,'-','+')
else return {t,expiry,ttl,'','unsupported Redis module value',0} end
return {t,expiry,ttl,dump,v,1}
`

func (s *Session) read(ctx context.Context, c *redisdriver.Client, key string) (keyRecord, error) {
	raw, err := c.Eval(ctx, readScript, []string{key}).Result()
	if err != nil {
		return keyRecord{}, safeError(err)
	}
	if raw == nil {
		return keyRecord{}, contract.ErrDataConflict
	}
	a, ok := raw.([]any)
	if !ok || len(a) != 6 {
		return keyRecord{}, errors.New("invalid Redis key response")
	}
	r := keyRecord{key: key, kind: a[0].(string), expiry: a[1].(int64), ttl: a[2].(int64), dump: a[3].(string), complete: a[5].(int64) == 1}
	if !r.complete {
		r.value = fmt.Sprintf(`{"preview_base64":%q,"complete":false}`, b64(a[4].(string)))
		return r, nil
	}
	v := valueDocument{}
	switch r.kind {
	case "string":
		x := b64(a[4].(string))
		v.String = &x
	case "hash":
		x := a[4].([]any)
		for i := 0; i < len(x); i += 2 {
			v.Hash = append(v.Hash, [2]string{b64(x[i].(string)), b64(x[i+1].(string))})
		}
	case "list":
		for _, x := range a[4].([]any) {
			v.List = append(v.List, b64(x.(string)))
		}
	case "set":
		for _, x := range a[4].([]any) {
			v.Set = append(v.Set, b64(x.(string)))
		}
	case "zset":
		x := a[4].([]any)
		for i := 0; i < len(x); i += 2 {
			v.ZSet = append(v.ZSet, scoredMember{b64(x[i].(string)), x[i+1].(string)})
		}
	case "stream":
		v.Stream = []streamEntry{}
		for _, x := range a[4].([]any) {
			entry := x.([]any)
			e := streamEntry{ID: entry[0].(string)}
			fields := entry[1].([]any)
			for i := 0; i < len(fields); i += 2 {
				e.Fields = append(e.Fields, [2]string{b64(fields[i].(string)), b64(fields[i+1].(string))})
			}
			v.Stream = append(v.Stream, e)
		}
	}
	text := encodeValue(v)
	if len(text) > contract.DataEditMaxBytes {
		r.complete = false
		r.dump = ""
		r.value = string([]rune(text)[:min(previewUnits, len([]rune(text)))])
		return r, nil
	}
	v, err = decodeValue(r.kind, text)
	if err != nil {
		return r, err
	}
	r.value = encodeValue(v)
	if len(r.value) > contract.DataEditMaxBytes {
		r.complete = false
		r.dump = ""
		r.value = string([]rune(r.value)[:min(previewUnits, len([]rune(r.value)))])
	}
	return r, nil
}
func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	ctx, cancel, c, err := s.request(ctx, s.profile.Database)
	if err != nil {
		return nil, err
	}
	defer cancel()
	// CONFIG is optional: ACLs commonly deny it. In that case expose the selected DB.
	n := 0
	values, e := c.ConfigGet(ctx, "databases").Result()
	if e == nil {
		n, _ = strconv.Atoi(values["databases"])
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	n = min(n, 256)
	out := make([]contract.Namespace, 0, n+1)
	found := false
	for i := range n {
		db := strconv.Itoa(i)
		found = found || db == s.profile.Database
		out = append(out, contract.Namespace{Ref: s.Namespace(db), Label: db})
	}
	if !found {
		out = append(out, contract.Namespace{Ref: s.Namespace(s.profile.Database), Label: s.profile.Database})
	}
	return out, nil
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	o := n.Object("keyspace", "keys")
	if err := s.object(o); err != nil {
		return nil, err
	}
	ctx, cancel, c, err := s.request(ctx, n.Catalog)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if err = c.Ping(ctx).Err(); err != nil {
		return nil, safeError(err)
	}
	return []contract.DBObject{{Ref: o, Name: "keys", Kind: "键", Engine: "Redis", Comment: "SCAN; order and membership may change during traversal"}}, nil
}
func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.object(o); err != nil {
		return nil, err
	}
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return &contract.TableStructure{Object: o, Database: o.Catalog, Name: o.Name, Kind: "键", Engine: "Redis", Editable: true, Columns: cols(), Comment: "Redis bytes use base64. SCAN is not a snapshot. Updates compare the original serialized value and absolute expiry."}, nil
}
func (s *Session) metadata(ctx context.Context, id int64, db, name string) (*contract.TableStructure, error) {
	return s.TableMetadata(ctx, contract.ObjectRef{ProfileID: id, Catalog: db, Kind: "keyspace", Name: name})
}
func (s *Session) info(ctx context.Context, id int64, db, name string) (*contract.TableInfo, error) {
	m, e := s.metadata(ctx, id, db, name)
	if e != nil {
		return nil, e
	}
	return &contract.TableInfo{Database: db, Name: name, Kind: "KEYSPACE", Engine: "Redis", Comment: m.Comment}, nil
}
func (s *Session) DataReads() contract.DataReads {
	return contract.DataReads{Loader: s.load, AdvancedLoader: s.page, RowReader: s.row, TableMetadata: s.metadata, TableInfo: s.info, Count: s.count, Chunk: s.chunk}
}
func (s *Session) DataFilterModes() []string { return []string{"equal", "contains", "like"} }
func (s *Session) load(ctx context.Context, id int64, db, name string, offset int64, limit int) (*contract.DataPage, error) {
	return s.page(ctx, id, db, name, contract.DataOptions{Offset: offset, Limit: limit})
}

type scanState struct {
	Cursor  uint64
	Pending []string
	Started bool
}

func (s *Session) cursorSignature(o contract.ObjectRef, state []byte, match string) string {
	h := hmac.New(sha256.New, s.key[:])
	raw, _ := json.Marshal(o)
	h.Write(raw)
	h.Write([]byte(match))
	h.Write(state)
	return hex.EncodeToString(h.Sum(nil))
}
func scanPattern(o contract.DataOptions) (string, error) {
	if o.Condition != nil || len(o.Filters) > 0 || len(o.Sorts) > 0 || o.SortColumn != "" || o.SortDescending || o.Limit < 1 || o.Limit > contract.DataMaxPageSize || o.Offset < 0 {
		return "", contract.ErrUnsupported
	}
	if o.FilterColumn == "" {
		return "*", nil
	}
	if o.FilterColumn != "key" {
		return "", contract.ErrUnsupported
	}
	value, e := unb64(o.FilterValue)
	if e != nil {
		return "", e
	}
	escape := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]")
	switch o.FilterMode {
	case "equal":
		return escape.Replace(value), nil
	case "contains":
		return "*" + escape.Replace(value) + "*", nil
	case "like":
		return value, nil
	}
	return "", contract.ErrUnsupported
}
func (s *Session) page(ctx context.Context, id int64, db, name string, o contract.DataOptions) (*contract.DataPage, error) {
	object := o.Object
	if object.ProfileID == 0 {
		object = contract.ObjectRef{ProfileID: id, Catalog: db, Kind: "keyspace", Name: name}
	}
	if id != s.ProfileID() || object.Catalog != db || object.Name != name {
		return nil, connections.ErrInvalid
	}
	if err := s.object(object); err != nil {
		return nil, err
	}
	match, err := scanPattern(o)
	if err != nil {
		return nil, err
	}
	state := scanState{}
	skip := o.Offset
	if o.After != nil {
		cur := o.After
		if cur.Database != db || cur.Table != name || len(cur.Values) != 1 || len(cur.Values[0]) > 2<<20 || !hmac.Equal([]byte(cur.RuleSignature), []byte(s.cursorSignature(object, cur.Values[0], match))) || json.Unmarshal(cur.Values[0], &state) != nil {
			return nil, errors.New("Redis SCAN cursor belongs to another scope, filter or session")
		}
		skip = 0
	}
	if skip > 10000 {
		return nil, errors.New("Redis deep paging requires a SCAN cursor")
	}
	ctx, cancel, c, err := s.request(ctx, db)
	if err != nil {
		return nil, err
	}
	defer cancel()
	start := time.Now()
	editable := true
	p := &contract.DataPage{Object: object, Database: db, Table: name, Columns: []string{"key", "type", "ttl_ms", "value"}, ColumnKinds: []string{"text", "text", "bigint", "json"}, ColumnMetadata: cols(), Offset: o.Offset, Limit: o.Limit, Editable: &editable, KeysetAvailable: true, QuerySQL: "Redis SCAN MATCH; results are not a stable snapshot"}
	seen := map[string]bool{}
	scans := 0
	for len(p.Rows) < o.Limit {
		if len(state.Pending) == 0 {
			if state.Started && state.Cursor == 0 {
				break
			}
			if scans >= 128 {
				break
			}
			scans++
			keys, next, e := c.Scan(ctx, state.Cursor, match, 200).Result()
			if e != nil {
				return nil, safeError(e)
			}
			state.Started = true
			state.Cursor = next
			size := 0
			for _, k := range keys {
				size += len(k)
			}
			if len(keys) > 16384 || size > 1<<20 {
				return nil, errors.New("Redis SCAN batch exceeds safe paging limits")
			}
			state.Pending = make([]string, len(keys))
			for i, key := range keys {
				state.Pending[i] = b64(key)
			}
			if len(keys) == 0 {
				continue
			}
		}
		key, decodeErr := unb64(state.Pending[0])
		if decodeErr != nil {
			return nil, decodeErr
		}
		state.Pending = state.Pending[1:]
		if seen[key] {
			continue
		}
		seen[key] = true
		if skip > 0 {
			skip--
			continue
		}
		r, e := s.read(ctx, c, key)
		if errors.Is(e, contract.ErrDataConflict) {
			continue
		}
		if e != nil {
			return nil, e
		}
		p.Rows = append(p.Rows, r.cells())
		ref := contract.RowRef{Object: object, Database: db, Table: name, Columns: []string{"key"}, Values: [][]byte{[]byte(key)}}
		p.RowRefs = append(p.RowRefs, &ref)
		var snap *contract.RowSnapshot
		if r.complete {
			snap = s.snapshot(object, r)
		}
		p.RowSnapshots = append(p.RowSnapshots, snap)
	}
	p.HasMore = len(state.Pending) > 0 || state.Cursor != 0 || !state.Started
	if p.HasMore {
		raw, _ := json.Marshal(state)
		p.NextCursor = &contract.DataCursor{Database: db, Table: name, Columns: []string{"scan"}, Values: [][]byte{raw}, RuleSignature: s.cursorSignature(object, raw, match)}
	}
	p.CompletedAt = time.Now()
	p.Duration = time.Since(start)
	return p, nil
}
func (s *Session) validateRef(id int64, db, name string, ref contract.RowRef) error {
	if id != s.ProfileID() || ref.Object.Catalog != db || ref.Object.Name != name || ref.Database != db || ref.Table != name || len(ref.Columns) != 1 || ref.Columns[0] != "key" || len(ref.Values) != 1 || len(ref.Values[0]) > contract.DataEditMaxBytes {
		return connections.ErrInvalid
	}
	return s.object(ref.Object)
}
func (s *Session) row(ctx context.Context, id int64, db, name string, ref contract.RowRef) (*contract.DataRow, error) {
	if e := s.validateRef(id, db, name, ref); e != nil {
		return nil, e
	}
	ctx, cancel, c, err := s.request(ctx, db)
	if err != nil {
		return nil, err
	}
	defer cancel()
	r, err := s.read(ctx, c, string(ref.Values[0]))
	if err != nil {
		return nil, err
	}
	if !r.complete {
		return nil, errors.New("Redis complete value exceeds 1 MiB editing limit or has an unsupported type")
	}
	return &contract.DataRow{Columns: cols(), Snapshot: s.snapshot(ref.Object, r), Values: map[string]contract.DataValue{"key": {Kind: contract.Text, Text: b64(r.key)}, "type": {Kind: contract.Text, Text: r.kind}, "ttl_ms": {Kind: contract.Integer, Text: strconv.FormatInt(r.ttl, 10)}, "value": {Kind: contract.JSON, Text: r.value}}}, nil
}
func (s *Session) count(ctx context.Context, id int64, db, name string) (int64, error) {
	if _, err := s.metadata(ctx, id, db, name); err != nil {
		return 0, err
	}
	ctx, cancel, c, err := s.request(ctx, db)
	if err != nil {
		return 0, err
	}
	defer cancel()
	n, err := c.DBSize(ctx).Result()
	return n, safeError(err)
}
func (s *Session) chunk(ctx context.Context, id int64, db, name, field string, ref contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
	if offset < 0 || units < 1 || units > previewUnits {
		return nil, contract.ErrUnsupported
	}
	if err := s.validateRef(id, db, name, ref); err != nil {
		return nil, err
	}
	if field == "value" {
		chunk, err := s.stringChunk(ctx, db, string(ref.Values[0]), offset, units)
		if err != nil {
			return nil, err
		}
		if chunk != nil {
			return chunk, nil
		}
	}
	r, err := s.row(ctx, id, db, name, ref)
	if err != nil {
		return nil, err
	}
	v, ok := r.Values[field]
	if !ok {
		return nil, contract.ErrUnsupported
	}
	runes := []rune(v.Text)
	a := min(offset, int64(len(runes)))
	b := min(a+int64(units), int64(len(runes)))
	return &contract.FieldChunk{Text: string(runes[a:b]), Offset: offset, NextOffset: b, TotalUnits: int64(len(runes)), HasMore: b < int64(len(runes))}, nil
}
func (s *Session) DataExports() contract.DataExports {
	return contract.DataExports{Exporter: s.exportRows, JSONL: s.jsonl}
}
func (s *Session) exportRows(ctx context.Context, id int64, db, name string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if sink == nil {
		return connections.ErrInvalid
	}
	if len(fields) == 0 {
		fields = []string{"key", "type", "ttl_ms", "value"}
	}
	for _, f := range fields {
		if f != "key" && f != "type" && f != "ttl_ms" && f != "value" {
			return contract.ErrUnsupported
		}
	}
	o.Limit = contract.DataMaxPageSize
	o.Offset = 0
	o.After = nil
	for {
		p, e := s.page(ctx, id, db, name, o)
		if e != nil {
			return e
		}
		for _, ref := range p.RowRefs {
			r, e := s.row(ctx, id, db, name, *ref)
			if e != nil {
				return e
			}
			values := make([]contract.DataValue, len(fields))
			for i, f := range fields {
				values[i] = r.Values[f]
			}
			if e = sink(values); e != nil {
				return e
			}
		}
		if !p.HasMore {
			return nil
		}
		o.After = p.NextCursor
	}
}
func (s *Session) jsonl(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if e := s.object(o); e != nil {
		return e
	}
	return s.exportRows(ctx, o.ProfileID, o.Catalog, o.Name, contract.DataOptions{Object: o}, nil, func(v []contract.DataValue) error {
		raw, e := json.Marshal(struct {
			Key   string          `json:"key"`
			Type  string          `json:"type"`
			TTL   string          `json:"ttl_ms"`
			Value json.RawMessage `json:"value"`
		}{v[0].Text, v[1].Text, v[2].Text, json.RawMessage(v[3].Text)})
		if e != nil {
			return e
		}
		_, e = w.Write(append(raw, '\n'))
		return e
	})
}

// stringChunk reads only the source bytes needed for one JSON/base64 slice.
// Length and bytes are captured by the same atomic script.
func (s *Session) stringChunk(ctx context.Context, db, key string, offset int64, units int) (*contract.FieldChunk, error) {
	ctx, cancel, c, err := s.request(ctx, db)
	if err != nil {
		return nil, err
	}
	defer cancel()
	const prefix = `{"string":"`
	const suffix = `"}`
	b64Start := max(int64(0), offset-int64(len(prefix)))
	block := b64Start / 4
	byteStart := block * 3
	byteEnd := byteStart + int64((units+12)/4*3) - 1
	raw, err := c.Eval(ctx, `local t=redis.call('TYPE',KEYS[1]).ok; if t=='none' then return false end; if t~='string' then return {t} end;return {t,redis.call('STRLEN',KEYS[1]),redis.call('GETRANGE',KEYS[1],ARGV[1],ARGV[2])}`, []string{key}, byteStart, byteEnd).Result()
	if err != nil {
		return nil, safeError(err)
	}
	if raw == nil {
		return nil, contract.ErrDataConflict
	}
	a, ok := raw.([]any)
	if !ok || len(a) < 1 {
		return nil, errors.New("invalid Redis field response")
	}
	if a[0] != "string" {
		return nil, nil
	}
	length := a[1].(int64)
	encodedLength := (length + 2) / 3 * 4
	total := int64(len(prefix)) + encodedLength + int64(len(suffix))
	start := min(offset, total)
	if start == total {
		return &contract.FieldChunk{Offset: offset, NextOffset: total, TotalUnits: total}, nil
	}
	end := min(start+int64(units), total)
	text := prefix + b64(a[2].(string))
	base := int64(0)
	if block > 0 {
		text = b64(a[2].(string))
		base = int64(len(prefix)) + block*4
	}
	if byteEnd >= length-1 {
		text += suffix
	}
	if start < base || end-base > int64(len(text)) {
		return nil, errors.New("Redis field boundary is invalid")
	}
	return &contract.FieldChunk{Text: text[start-base : end-base], Offset: offset, NextOffset: end, TotalUnits: total, HasMore: end < total}, nil
}
