package localdb

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

// All identity/auth/deadline fields are explicit in saved native profiles;
// damaged records must not change routing, authentication or timeout policy.
func canonicalNeo4jSavedOptions(raw string) bool {
	if !utf8.ValidString(raw) || !validNativeSavedUnicode(raw) {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &envelope) != nil || len(envelope) != 3 {
		return false
	}
	for _, key := range []string{"version", "kind", "neo4j"} {
		if len(envelope[key]) == 0 || bytes.Equal(bytes.TrimSpace(envelope[key]), []byte("null")) {
			return false
		}
	}
	var options map[string]json.RawMessage
	if json.Unmarshal(envelope["neo4j"], &options) != nil {
		return false
	}
	for _, key := range []string{"version", "protocol", "auth", "routing_context", "connect_timeout_seconds", "request_timeout_seconds"} {
		if len(options[key]) == 0 {
			return false
		}
	}
	for key, value := range options {
		switch key {
		case "version", "protocol", "auth", "routing_context", "ca_file", "client_cert_file", "client_key_file", "connect_timeout_seconds", "request_timeout_seconds":
		default:
			return false
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
		if key == "connect_timeout_seconds" || key == "request_timeout_seconds" {
			n, e := strconv.ParseUint(string(value), 10, 16)
			if e != nil || n == 0 || strconv.FormatUint(n, 10) != string(value) {
				return false
			}
		}
	}
	return true
}
