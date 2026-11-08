package meilisearch

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// ParseRequest reads one METHOD /endpoint-relative-path and optional JSON.
// The body retains its original bytes. Multiple requests and custom headers
// require separate explicit operations, rather than SQL script inference.
func ParseRequest(source string) (Request, error) {
	var r Request
	if len(source) == 0 || len(source) > requestLimit || !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return r, ErrInvalid
	}
	for {
		line, remainder, found := strings.Cut(source, "\n")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			if !found {
				return r, ErrInvalid
			}
			source = remainder
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return r, ErrInvalid
		}
		r.Method, r.Path, r.ContentType = strings.ToUpper(parts[0]), parts[1], "application/json"
		r.Body = []byte(remainder)
		break
	}
	if len(bytes.TrimSpace(r.Body)) == 0 {
		r.Body = nil
	} else {
		var v json.RawMessage
		if decodeJSON(r.Body, &v) != nil {
			return r, ErrInvalid
		}
	}
	if err := validateRequest(r); err != nil {
		return r, err
	}
	return r, nil
}
