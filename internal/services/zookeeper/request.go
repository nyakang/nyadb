package zookeeper

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

func object(raw []byte, limit int) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if jsondocument.Decode(raw, &fields, limit) != nil || fields == nil {
		return nil, ErrInvalid
	}
	return fields, nil
}

func textField(fields map[string]json.RawMessage, key string, required bool) (string, error) {
	raw, ok := fields[key]
	if !ok && !required {
		return "", nil
	}
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", ErrInvalid
	}
	return s, nil
}

func boolField(fields map[string]json.RawMessage, key string, fallback bool) (bool, error) {
	raw, ok := fields[key]
	if !ok {
		return fallback, nil
	}
	if string(raw) != "true" && string(raw) != "false" {
		return false, ErrInvalid
	}
	return string(raw) == "true", nil
}

func allowed(fields map[string]json.RawMessage, names ...string) bool {
	for key := range fields {
		found := false
		for _, name := range names {
			if key == name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func decodeRequest(r contract.KVRequest) (contract.KVRequest, bool, error) {
	f, err := object([]byte(r.JSON), requestLimit)
	if err != nil {
		return r, false, err
	}
	write := false
	switch r.Method {
	case "connection_info":
		if len(f) != 0 {
			return r, false, ErrInvalid
		}
	case "kv_get", "kv_delete", "kv_put":
		key, err := textField(f, "key", true)
		if err != nil {
			return r, false, err
		}
		key = normalizePath(key)
		if !validPath(key) {
			return r, false, ErrInvalid
		}
		f["key"], _ = json.Marshal(key)
		switch r.Method {
		case "kv_get":
			if !allowed(f, "key") {
				return r, false, ErrInvalid
			}
		case "kv_delete":
			write = true
			if key == "/" || !allowed(f, "key", "recursive") {
				return r, false, ErrInvalid
			}
			if _, err := boolField(f, "recursive", true); err != nil {
				return r, false, err
			}
		case "kv_put":
			write = true
			if key == "/" || !allowed(f, "key", "value", "writeMode", "createMode") {
				return r, false, ErrInvalid
			}
			v, err := object(f["value"], requestLimit)
			if err != nil || !allowed(v, "encoding", "data") {
				return r, false, ErrInvalid
			}
			encoding, err := textField(v, "encoding", false)
			if err != nil {
				return r, false, err
			}
			data, err := textField(v, "data", true)
			if err != nil {
				return r, false, err
			}
			switch encoding {
			case "", "utf8":
				if len(data) > valueLimit {
					return r, false, ErrBounds
				}
			case "base64":
				b, err := base64.StdEncoding.Strict().DecodeString(data)
				if err != nil || base64.StdEncoding.EncodeToString(b) != data {
					return r, false, ErrInvalid
				}
				if len(b) > valueLimit {
					return r, false, ErrBounds
				}
			default:
				return r, false, ErrInvalid
			}
			mode, err := textField(f, "writeMode", false)
			if err != nil {
				return r, false, err
			}
			if mode == "" {
				mode = "upsert"
			}
			if mode != "create" && mode != "update" && mode != "upsert" {
				return r, false, ErrInvalid
			}
			create, err := textField(f, "createMode", false)
			if err != nil {
				return r, false, err
			}
			if create != "" && mode != "create" {
				return r, false, ErrInvalid
			}
			switch create {
			case "", "persistent", "ephemeral", "persistent_sequential", "ephemeral_sequential":
			default:
				return r, false, ErrInvalid
			}
		}
	case "kv_list_prefix":
		if !allowed(f, "prefix", "recursive", "limit", "continuation") {
			return r, false, ErrInvalid
		}
		prefix, err := textField(f, "prefix", false)
		if err != nil {
			return r, false, err
		}
		prefix = normalizePath(prefix)
		if !validPath(prefix) {
			return r, false, ErrInvalid
		}
		f["prefix"], _ = json.Marshal(prefix)
		recursive, err := boolField(f, "recursive", true)
		if err != nil {
			return r, false, err
		}
		if raw, ok := f["limit"]; ok {
			n, err := strconv.ParseInt(string(raw), 10, 32)
			if err != nil || n < 0 || n > 10000 {
				return r, false, ErrInvalid
			}
		}
		cursor, err := textField(f, "continuation", false)
		if err != nil || len(cursor) > 8192 {
			return r, false, ErrInvalid
		}
		if strings.TrimSpace(cursor) != "" {
			raw, err := base64.StdEncoding.Strict().DecodeString(cursor)
			if err != nil {
				return r, false, ErrInvalid
			}
			c, err := object(raw, 8192)
			if err != nil || !allowed(c, "root", "recursive", "offset") {
				return r, false, ErrInvalid
			}
			root, err := textField(c, "root", true)
			if err != nil || root != prefix {
				return r, false, ErrInvalid
			}
			re, err := boolField(c, "recursive", false)
			if err != nil || c["recursive"] == nil || re != recursive {
				return r, false, ErrInvalid
			}
			offset, err := strconv.ParseInt(string(c["offset"]), 10, 32)
			if err != nil || offset < 0 {
				return r, false, ErrInvalid
			}
		}
	default:
		return r, false, ErrInvalid
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return r, false, ErrInvalid
	}
	r.JSON = string(raw)
	return r, write, nil
}
