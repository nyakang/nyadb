package localdb

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

func canonicalEtcdSavedOptions(raw string) bool {
	if !utf8.ValidString(raw) || !validNativeSavedUnicode(raw) || !uniqueNativeAuthOptionsJSON(raw) {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &envelope) != nil || len(envelope) != 3 {
		return false
	}
	for _, k := range []string{"version", "kind", "etcd"} {
		if len(envelope[k]) == 0 || bytes.Equal(bytes.TrimSpace(envelope[k]), []byte("null")) {
			return false
		}
	}
	var options map[string]json.RawMessage
	if json.Unmarshal(envelope["etcd"], &options) != nil {
		return false
	}
	for _, k := range []string{"version", "auth", "prefix_encoding", "prefix", "connect_timeout_seconds", "request_timeout_seconds"} {
		if len(options[k]) == 0 {
			return false
		}
	}
	for k, v := range options {
		switch k {
		case "version", "auth", "prefix_encoding", "prefix", "ca_file", "server_name", "client_cert_file", "client_key_file", "connect_timeout_seconds", "request_timeout_seconds":
		default:
			return false
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return false
		}
		if k == "connect_timeout_seconds" || k == "request_timeout_seconds" {
			n, err := strconv.ParseUint(string(v), 10, 16)
			if err != nil || n == 0 || strconv.FormatUint(n, 10) != string(v) {
				return false
			}
		}
	}
	return true
}
