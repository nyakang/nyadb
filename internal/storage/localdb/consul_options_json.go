package localdb

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

func canonicalConsulSavedOptions(raw string) bool {
	if !utf8.ValidString(raw) || !validNativeSavedUnicode(raw) || !uniqueNativeAuthOptionsJSON(raw) {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &envelope) != nil || len(envelope) != 3 {
		return false
	}
	for _, key := range []string{"version", "kind", "consul"} {
		if len(envelope[key]) == 0 || bytes.Equal(bytes.TrimSpace(envelope[key]), []byte("null")) {
			return false
		}
	}
	var options map[string]json.RawMessage
	if json.Unmarshal(envelope["consul"], &options) != nil {
		return false
	}
	for _, key := range []string{"version", "auth", "datacenter", "prefix", "consistency", "connect_timeout_seconds", "request_timeout_seconds"} {
		if len(options[key]) == 0 {
			return false
		}
	}
	for key, value := range options {
		switch key {
		case "version", "auth", "datacenter", "prefix", "consistency", "ca_file", "server_name", "client_cert_file", "client_key_file", "connect_timeout_seconds", "request_timeout_seconds":
		default:
			return false
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
		if key == "version" || key == "connect_timeout_seconds" || key == "request_timeout_seconds" {
			n, err := strconv.ParseUint(string(value), 10, 16)
			if err != nil || n == 0 || strconv.FormatUint(n, 10) != string(value) {
				return false
			}
		}
	}
	return true
}
