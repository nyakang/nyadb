package localdb

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

// New CouchDB profiles require explicit canonical public fields. In particular,
// an omitted/null auth must not fall back to anonymous with a saved password.
// An empty context_path is valid for the native server root, but must be present.
func canonicalCouchDBSavedOptions(raw string) bool {
	if !utf8.ValidString(raw) || !validCouchDBSavedUnicode(raw) {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &envelope) != nil || len(envelope) != 3 {
		return false
	}
	for _, key := range []string{"version", "kind", "couchdb"} {
		if len(envelope[key]) == 0 || bytes.Equal(bytes.TrimSpace(envelope[key]), []byte("null")) {
			return false
		}
	}
	var options map[string]json.RawMessage
	if json.Unmarshal(envelope["couchdb"], &options) != nil {
		return false
	}
	for _, key := range []string{"version", "auth", "context_path"} {
		if len(options[key]) == 0 {
			return false
		}
	}
	for key, value := range options {
		switch key {
		case "version", "auth", "context_path", "ca_file", "server_name":
		default:
			return false
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

// encoding/json silently replaces unpaired escaped UTF-16 surrogates. New
// CouchDB routing/certificate paths must not change while reading saved JSON.
func validCouchDBSavedUnicode(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		code, err := strconv.ParseUint(raw[i+1:i+5], 16, 16)
		if err != nil || code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		i += 4
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1:i+3] != `\u` {
				return false
			}
			low, err := strconv.ParseUint(raw[i+3:i+7], 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
