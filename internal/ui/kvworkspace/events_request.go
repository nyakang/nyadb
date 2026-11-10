package kvworkspace

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// These document methods are enabled only by an injected events provider.
// History describes a native exact-key Range followed by a fixed Watch window.
const WatchMethod = "/etcdserverpb.Watch/Watch"
const HistoryMethod = "/nyadb.KV/History"

func eventMethod(method string) bool { return method == WatchMethod || method == HistoryMethod }

// eventFields accepts protobuf snake/camel aliases while rejecting two names
// for the same field. RawMessage avoids a float64 round trip for native int64s.
func eventFields(text string, allowed ...string) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := jsondocument.Decode([]byte(text), &raw, inputLimit); err != nil || raw == nil {
		return nil, jsondocument.ErrInvalid
	}
	aliases := map[string]string{}
	for _, name := range allowed {
		aliases[name] = name
		parts := strings.Split(name, "_")
		camel := parts[0]
		for _, part := range parts[1:] {
			camel += strings.ToUpper(part[:1]) + part[1:]
		}
		aliases[camel] = name
	}
	out := map[string]json.RawMessage{}
	for key, value := range raw {
		name, ok := aliases[key]
		if !ok || out[name] != nil || string(value) == "null" {
			return nil, jsondocument.ErrInvalid
		}
		out[name] = value
	}
	return out, nil
}
func eventInt(fields map[string]json.RawMessage, name string, fallback int64) (int64, error) {
	raw := fields[name]
	if raw == nil {
		return fallback, nil
	}
	text := string(raw)
	if raw[0] == '"' && json.Unmarshal(raw, &text) != nil {
		return 0, jsondocument.ErrInvalid
	}
	// Decimal integers only, including quoted protobuf spellings.
	if text == "" || strings.TrimLeft(text, "0123456789") != "" {
		return 0, jsondocument.ErrInvalid
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, jsondocument.ErrInvalid
	}
	return value, nil
}
func eventBool(fields map[string]json.RawMessage, name string) (bool, error) {
	raw := fields[name]
	if raw == nil {
		return false, nil
	}
	if string(raw) != "true" && string(raw) != "false" {
		return false, jsondocument.ErrInvalid
	}
	return string(raw) == "true", nil
}
func eventBytes(fields map[string]json.RawMessage, name string) ([]byte, error) {
	raw := fields[name]
	if raw == nil {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) != nil || len(text) > 5464 {
		return nil, jsondocument.ErrInvalid
	}
	value, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil || len(value) > 4096 || base64.StdEncoding.EncodeToString(value) != text {
		return nil, jsondocument.ErrInvalid
	}
	return value, nil
}

func ParseWatchRequest(text string) (contract.KVWatchRequest, error) {
	var r contract.KVWatchRequest
	f, err := eventFields(text, "key", "range_end", "start_revision", "watch_id", "prev_kv", "progress_notify", "filters", "fragment")
	if err != nil {
		return r, err
	}
	if r.Key, err = eventBytes(f, "key"); err != nil || len(r.Key) == 0 {
		return r, jsondocument.ErrInvalid
	}
	if r.RangeEnd, err = eventBytes(f, "range_end"); err != nil || len(r.RangeEnd) > 0 && !bytes.Equal(r.RangeEnd, []byte{0}) && bytes.Compare(r.RangeEnd, r.Key) <= 0 {
		return r, jsondocument.ErrInvalid
	}
	if r.StartRevision, err = eventInt(f, "start_revision", 0); err != nil {
		return r, err
	}
	if r.WatchID, err = eventInt(f, "watch_id", 1); err != nil || r.WatchID == 0 {
		return r, jsondocument.ErrInvalid
	}
	if r.PrevKV, err = eventBool(f, "prev_kv"); err != nil {
		return r, err
	}
	if r.ProgressNotify, err = eventBool(f, "progress_notify"); err != nil {
		return r, err
	}
	if fragment, e := eventBool(f, "fragment"); e != nil || fragment {
		return r, jsondocument.ErrInvalid
	}
	if raw := f["filters"]; raw != nil {
		var filters []json.RawMessage
		if json.Unmarshal(raw, &filters) != nil || len(filters) > 2 {
			return r, jsondocument.ErrInvalid
		}
		for _, filter := range filters {
			switch string(filter) {
			case `"NOPUT"`, "0":
				if r.NoPut {
					return r, jsondocument.ErrInvalid
				}
				r.NoPut = true
			case `"NODELETE"`, "1":
				if r.NoDelete {
					return r, jsondocument.ErrInvalid
				}
				r.NoDelete = true
			default:
				return r, jsondocument.ErrInvalid
			}
		}
	}
	return r, nil
}
func ParseHistoryRequest(text string) (contract.KVHistoryRequest, error) {
	var r contract.KVHistoryRequest
	f, err := eventFields(text, "key", "start_revision", "end_revision", "limit")
	if err != nil {
		return r, err
	}
	if r.Key, err = eventBytes(f, "key"); err != nil || len(r.Key) == 0 {
		return r, jsondocument.ErrInvalid
	}
	if r.StartRevision, err = eventInt(f, "start_revision", 0); err != nil {
		return r, err
	}
	if r.EndRevision, err = eventInt(f, "end_revision", 0); err != nil || r.EndRevision > 0 && r.StartRevision > r.EndRevision {
		return r, jsondocument.ErrInvalid
	}
	limit, err := eventInt(f, "limit", 100)
	if err != nil || limit < 1 || limit > 500 {
		return r, jsondocument.ErrInvalid
	}
	r.Limit = int(limit)
	return r, nil
}
