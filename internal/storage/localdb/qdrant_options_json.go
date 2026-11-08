package localdb

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

// Explicit auth and deadlines prevent damaged records from silently switching
// to anonymous handshakes or default timeout values.
func canonicalQdrantSavedOptions(raw string) bool {
	if !utf8.ValidString(raw) || !validNativeSavedUnicode(raw) {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &envelope) != nil || len(envelope) != 3 {
		return false
	}
	for _, key := range []string{"version", "kind", "qdrant"} {
		if len(envelope[key]) == 0 || bytes.Equal(bytes.TrimSpace(envelope[key]), []byte("null")) {
			return false
		}
	}
	var options map[string]json.RawMessage
	if json.Unmarshal(envelope["qdrant"], &options) != nil {
		return false
	}
	for _, key := range []string{"version", "auth", "context_path", "connect_timeout_seconds", "request_timeout_seconds"} {
		if len(options[key]) == 0 {
			return false
		}
	}
	for key, value := range options {
		switch key {
		case "version", "auth", "context_path", "ca_file", "server_name", "client_cert_file", "client_key_file", "connect_timeout_seconds", "request_timeout_seconds":
		default:
			return false
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
		if key == "connect_timeout_seconds" || key == "request_timeout_seconds" {
			n, err := strconv.ParseUint(string(value), 10, 16)
			if err != nil || n == 0 || strconv.FormatUint(n, 10) != string(value) {
				return false
			}
		}
	}
	return true
}
