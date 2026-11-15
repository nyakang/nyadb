package localdb

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

func canonicalRQLiteSavedOptions(raw string) bool {
	if !utf8.ValidString(raw) || !validNativeSavedUnicode(raw) || !uniqueNativeAuthOptionsJSON(raw) {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &envelope) != nil || len(envelope) != 3 {
		return false
	}
	for _, key := range []string{"kind", "version", "rqlite"} {
		if len(envelope[key]) == 0 || bytes.Equal(bytes.TrimSpace(envelope[key]), []byte("null")) {
			return false
		}
	}
	var options map[string]json.RawMessage
	if json.Unmarshal(envelope["rqlite"], &options) != nil {
		return false
	}
	for _, key := range []string{"version", "auth", "base_path", "consistency", "freshness_milliseconds", "connect_timeout_seconds", "request_timeout_seconds", "forward_timeout_seconds", "database_timeout_milliseconds", "linearizable_timeout_milliseconds", "max_requests", "sql_max_connections"} {
		if len(options[key]) == 0 {
			return false
		}
	}
	for key, value := range options {
		switch key {
		case "version", "auth", "base_path", "consistency", "freshness_milliseconds", "freshness_strict", "connect_timeout_seconds", "request_timeout_seconds", "forward_timeout_seconds", "database_timeout_milliseconds", "linearizable_timeout_milliseconds", "max_requests", "sql_max_connections", "batch_transaction", "read_only", "ca_file", "server_name", "client_cert_file", "client_key_file":
		default:
			return false
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}
