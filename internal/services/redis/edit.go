package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"reflect"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Build an isolated temporary value. No live key changes until RENAME succeeds.
// All temporary commands are validated in Go and server failures remove the temp.
const saveScript = `
local op=ARGV[1]
local old=KEYS[1]
local dest=KEYS[2]
local temp=KEYS[3]
if op=='insert' then
 if redis.call('EXISTS',dest)==1 then return 0 end
else
 if redis.call('EXISTS',old)==0 or redis.call('DUMP',old)~=ARGV[2] or tostring(redis.call('PEXPIRETIME',old))~=ARGV[3] then return 0 end
 if dest~=old and redis.call('EXISTS',dest)==1 then return 0 end
end
if op=='delete' then redis.call('DEL',old);return 1 end
if redis.call('EXISTS',temp)==1 then return 0 end
local count=tonumber(ARGV[5])
local at=6
for i=1,count do
 local n=tonumber(ARGV[at]);at=at+1
 local args={}
 for j=1,n do args[j]=ARGV[at];at=at+1 end
 local result=redis.pcall(unpack(args))
 if type(result)=='table' and result.err then redis.call('DEL',temp);return redis.error_reply('NYADB_TEMP_FAILED') end
end
local expiry=tonumber(ARGV[4])
if expiry~=-1 then
 local now=redis.call('TIME')
 if expiry<=tonumber(now[1])*1000+math.floor(tonumber(now[2])/1000) then redis.call('DEL',temp);return 0 end
 local result=redis.pcall('PEXPIREAT',temp,ARGV[4])
 if type(result)=='table' and result.err then redis.call('DEL',temp);return redis.error_reply('NYADB_TEMP_FAILED') end
end
local renamed=redis.pcall('RENAME',temp,dest)
if type(renamed)=='table' and renamed.err then redis.call('DEL',temp);return redis.error_reply('NYADB_TEMP_FAILED') end
if op~='insert' and old~=dest then redis.call('DEL',old) end
return {1,redis.call('DUMP',dest)}
`

type preparedEdit struct {
	edit      contract.DataEdit
	key, kind string
	value     valueDocument
	hasValue  bool
	ttl       *int64
}

func (s *Session) prepare(o contract.ObjectRef, edits []contract.DataEdit) ([]preparedEdit, error) {
	if len(edits) < 1 || len(edits) > contract.DataMaxPageSize {
		return nil, connections.ErrInvalid
	}
	out := make([]preparedEdit, 0, len(edits))
	bytes := 0
	for _, e := range edits {
		p := preparedEdit{edit: e}
		if e.Kind != "insert" && e.Kind != "update" && e.Kind != "delete" {
			return nil, contract.ErrUnsupported
		}
		if e.Kind != "insert" {
			if err := s.validateSnapshot(o, e.Original); err != nil {
				return nil, err
			}
			p.key = string(e.Original.Ref.Values[0])
		} else if e.Original != nil {
			return nil, connections.ErrInvalid
		}
		if e.Kind == "delete" && len(e.Values) != 0 {
			return nil, connections.ErrInvalid
		}
		for name, v := range e.Values {
			bytes += len(v.Text) + len(v.Bytes)
			if v.Null || v.Default || v.Binary || len(v.Bytes) > 0 {
				return nil, contract.ErrUnsupported
			}
			switch name {
			case "key":
				key, err := unb64(v.Text)
				if err != nil {
					return nil, err
				}
				p.key = key
			case "type":
				p.kind = v.Text
			case "value":
				p.hasValue = true
			case "ttl_ms":
				n, err := strconv.ParseInt(v.Text, 10, 64)
				if err != nil || n < -1 || n == 0 || n > 3155760000000 {
					return nil, errors.New("Redis TTL must be -1 or a positive number of milliseconds, at most 100 years")
				}
				p.ttl = &n
			default:
				return nil, contract.ErrUnsupported
			}
		}
		if bytes > contract.DataEditMaxBytes {
			return nil, errors.New("Redis edit batch exceeds 1 MiB")
		}
		if e.Kind == "insert" {
			if _, ok := e.Values["key"]; !ok || p.kind == "" || !p.hasValue {
				return nil, errors.New("Redis insert requires key, type and complete value")
			}
		}
		if p.hasValue && p.kind != "" {
			v, err := decodeValue(p.kind, e.Values["value"].Text)
			if err != nil {
				return nil, err
			}
			p.value = v
		}
		out = append(out, p)
	}
	return out, nil
}
func makeCommands(temp string, r keyRecord, desired valueDocument, hasValue bool) ([][]string, error) {
	if !hasValue || desired.String != nil && r.kind == "string" && encodeValue(desired) == r.value {
		return [][]string{{"RESTORE", temp, "0", r.dump}}, nil
	}
	decode := func(v string) string { s, _ := unb64(v); return s }
	switch r.kind {
	case "string":
		return [][]string{{"SET", temp, decode(*desired.String)}}, nil
	case "hash":
		commands := [][]string{}
		previous := map[string]string{}
		if r.dump != "" {
			original, err := decodeValue("hash", r.value)
			if err != nil {
				return nil, err
			}
			commands = append(commands, []string{"RESTORE", temp, "0", r.dump})
			for _, f := range original.Hash {
				previous[f[0]] = f[1]
			}
		}
		keep := map[string]bool{}
		for _, f := range desired.Hash {
			keep[f[0]] = true
			if v, ok := previous[f[0]]; !ok || v != f[1] {
				commands = append(commands, []string{"HSET", temp, decode(f[0]), decode(f[1])})
			}
		}
		for f := range previous {
			if !keep[f] {
				commands = append(commands, []string{"HDEL", temp, decode(f)})
			}
		}
		return commands, nil
	case "list":
		commands := [][]string{}
		for start := 0; start < len(desired.List); start += 256 {
			args := []string{"RPUSH", temp}
			for _, v := range desired.List[start:min(start+256, len(desired.List))] {
				args = append(args, decode(v))
			}
			commands = append(commands, args)
		}
		return commands, nil
	case "set":
		commands := [][]string{}
		for start := 0; start < len(desired.Set); start += 256 {
			args := []string{"SADD", temp}
			for _, v := range desired.Set[start:min(start+256, len(desired.Set))] {
				args = append(args, decode(v))
			}
			commands = append(commands, args)
		}
		return commands, nil
	case "zset":
		commands := [][]string{}
		for start := 0; start < len(desired.ZSet); start += 128 {
			args := []string{"ZADD", temp}
			for _, v := range desired.ZSet[start:min(start+128, len(desired.ZSet))] {
				args = append(args, v.Score, decode(v.Member))
			}
			commands = append(commands, args)
		}
		return commands, nil
	case "stream":
		commands := [][]string{}
		original := valueDocument{}
		if r.dump != "" {
			var err error
			original, err = decodeValue("stream", r.value)
			if err != nil {
				return nil, err
			}
			commands = append(commands, []string{"RESTORE", temp, "0", r.dump})
		}
		old := map[string]streamEntry{}
		for _, e := range original.Stream {
			old[e.ID] = e
		}
		keep := map[string]bool{}
		for _, e := range desired.Stream {
			if prior, ok := old[e.ID]; ok {
				if !reflect.DeepEqual(prior, e) {
					return nil, errors.New("Redis stream entries are immutable; append a new ID or remove the entry")
				}
				keep[e.ID] = true
			} else {
				args := []string{"XADD", temp, e.ID}
				for _, f := range e.Fields {
					args = append(args, decode(f[0]), decode(f[1]))
				}
				commands = append(commands, args)
			}
		}
		for _, e := range original.Stream {
			if !keep[e.ID] {
				commands = append(commands, []string{"XDEL", temp, e.ID})
			}
		}
		if len(commands) == 0 {
			return nil, errors.New("a new Redis stream requires at least one entry")
		}
		return commands, nil
	}
	return nil, contract.ErrUnsupported
}
func (s *Session) DataEdits() contract.DataEdits {
	return contract.DataEdits{Saver: s.save, Verify: s.verify}
}
func (s *Session) save(ctx context.Context, id int64, db, name string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
	o := contract.ObjectRef{ProfileID: id, Catalog: db, Kind: "keyspace", Name: name}
	if err := s.object(o); err != nil {
		return nil, err
	}
	prepared, err := s.prepare(o, edits)
	if err != nil {
		return nil, err
	}
	ctx, cancel, c, err := s.request(ctx, db)
	if err != nil {
		return nil, err
	}
	defer cancel()
	result := &contract.DataEditResult{}
	defer func() {
		if !result.CommitConfirmed && result.Applied > 0 {
			result.Incomplete = true
		}
	}()
	for _, p := range prepared {
		current := keyRecord{key: p.key, kind: p.kind, expiry: -1, ttl: -1, complete: true}
		oldKey := p.key
		if p.edit.Kind != "insert" {
			oldKey = string(p.edit.Original.Ref.Values[0])
			current, err = s.read(ctx, c, oldKey)
			if err != nil || !matches(current, p.edit.Original, true) {
				result.Incomplete = result.Applied > 0
				if err != nil {
					return result, err
				}
				return result, contract.ErrDataConflict
			}
			if p.kind != "" && p.kind != current.kind {
				return result, errors.New("Redis type changes require an explicit delete and insert")
			}
		}
		desired := current
		desired.key = p.key
		if p.hasValue {
			p.value, err = decodeValue(desired.kind, p.edit.Values["value"].Text)
			if err != nil {
				return result, err
			}
			desired.value = encodeValue(p.value)
		}
		if p.ttl != nil {
			desired.ttl = *p.ttl
			desired.expiry = -1
			if *p.ttl != -1 {
				now, e := c.Time(ctx).Result()
				if e != nil {
					return result, safeError(e)
				}
				desired.expiry = now.UnixMilli() + *p.ttl
			}
		}
		outcome := contract.DataEditOutcome{Kind: p.edit.Kind, Original: p.edit.Original, PreviewColumns: []string{"key", "type", "ttl_ms", "value"}}
		if p.edit.Kind != "delete" {
			outcome.Saved = s.snapshot(o, desired)
			outcome.Previews = desired.cells()
		}
		var random [16]byte
		if _, err = rand.Read(random[:]); err != nil {
			return result, err
		}
		temp := "__nyadb_edit_" + hex.EncodeToString(random[:])
		commands := [][]string{}
		if p.edit.Kind != "delete" {
			commands, err = makeCommands(temp, current, p.value, p.hasValue)
			if err != nil {
				return result, err
			}
		}
		for _, command := range commands {
			if len(command) > 512 {
				return result, errors.New("Redis command within an edit exceeds 512 arguments; reduce the value or use native commands")
			}
		}
		args := []any{p.edit.Kind, current.dump, strconv.FormatInt(current.expiry, 10), strconv.FormatInt(desired.expiry, 10), len(commands)}
		for _, command := range commands {
			args = append(args, len(command))
			for _, a := range command {
				args = append(args, a)
			}
		}
		// EVAL, never EVALSHA: no automatic script retry following a failed send.
		raw, e := c.Eval(ctx, saveScript, []string{oldKey, p.key, temp}, args...).Result()
		applied := int64(0)
		if n, ok := raw.(int64); ok {
			applied = n
		}
		if values, ok := raw.([]any); ok && len(values) == 2 {
			applied, _ = values[0].(int64)
			desired.dump, _ = values[1].(string)
			outcome.Saved = s.snapshot(o, desired)
		}
		if e != nil {
			result.Incomplete = result.Applied > 0
			// A server script error can follow a mutation. Retain the draft until verified.
			result.CommitUncertain = true
			result.Outcomes = append(result.Outcomes, outcome)
			return result, safeError(e)
		}
		if applied != 1 {
			result.Incomplete = result.Applied > 0
			return result, contract.ErrDataConflict
		}
		result.Applied++
		result.Outcomes = append(result.Outcomes, outcome)
	}
	result.CommitConfirmed = true
	return result, nil
}
func (s *Session) verify(ctx context.Context, id int64, db, name string, result *contract.DataEditResult) (*contract.DataVerification, error) {
	o := contract.ObjectRef{ProfileID: id, Catalog: db, Kind: "keyspace", Name: name}
	if err := s.object(o); err != nil {
		return nil, err
	}
	if result == nil || len(result.Outcomes) == 0 || len(result.Outcomes) > contract.DataMaxPageSize {
		return nil, connections.ErrInvalid
	}
	ctx, cancel, c, err := s.request(ctx, db)
	if err != nil {
		return nil, err
	}
	defer cancel()
	v := &contract.DataVerification{Status: "indeterminate"}
	allApplied, allAbsent := true, true
	for _, out := range result.Outcomes {
		snap := out.Saved
		if snap == nil {
			snap = out.Original
		}
		if err = s.validateSnapshot(o, snap); err != nil {
			return nil, err
		}
		if out.Original != nil {
			if err = s.validateSnapshot(o, out.Original); err != nil {
				return nil, err
			}
		}
		r, e := s.read(ctx, c, string(snap.Ref.Values[0]))
		if e != nil && !errors.Is(e, contract.ErrDataConflict) {
			return nil, e
		}
		state := "indeterminate"
		if out.Kind == "delete" && errors.Is(e, contract.ErrDataConflict) || out.Kind != "delete" && e == nil && matches(r, out.Saved, false) {
			state = "applied"
		}
		if out.Kind == "insert" && errors.Is(e, contract.ErrDataConflict) || out.Kind != "insert" && e == nil && matches(r, out.Original, true) {
			state = "not_applied"
		}
		if out.Original != nil && out.Saved != nil && string(out.Original.Ref.Values[0]) != string(out.Saved.Ref.Values[0]) {
			old, e2 := s.read(ctx, c, string(out.Original.Ref.Values[0]))
			if e2 != nil && !errors.Is(e2, contract.ErrDataConflict) {
				return nil, e2
			}
			if state == "applied" && e2 == nil {
				state = "indeterminate"
			}
			if errors.Is(e, contract.ErrDataConflict) && e2 == nil && matches(old, out.Original, true) {
				state = "not_applied"
			}
		}
		v.States = append(v.States, state)
		allApplied = allApplied && state == "applied"
		allAbsent = allAbsent && state == "not_applied"
	}
	if allApplied && !result.Incomplete {
		v.Status = "applied"
		recovered := *result
		recovered.CommitConfirmed = true
		recovered.CommitUncertain = false
		recovered.Verified = true
		recovered.Applied = len(result.Outcomes)
		v.Recovered = &recovered
	} else if allAbsent {
		v.Status = "not_applied"
	}
	return v, nil
}
