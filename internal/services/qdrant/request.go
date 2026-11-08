package qdrant

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validateRequest(r Request) error {
	switch r.Method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
	default:
		return ErrInvalid
	}
	if len(r.Body) > requestLimit || (r.Method == "GET" || r.Method == "HEAD") && len(r.Body) != 0 || len(r.Path) == 0 || len(r.Path) > 8192 || !utf8.ValidString(r.Path) || !strings.HasPrefix(r.Path, "/") || strings.HasPrefix(r.Path, "//") || strings.ContainsAny(r.Path, "\\#") || strings.IndexFunc(r.Path, func(ch rune) bool { return unicode.IsSpace(ch) || unicode.IsControl(ch) }) >= 0 {
		return ErrInvalid
	}
	u, err := url.Parse(r.Path)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || !utf8.ValidString(u.Path) || strings.Contains(u.Path, "//") {
		return ErrInvalid
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." || strings.ContainsAny(part, "\\%") || strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return ErrInvalid
		}
	}
	if _, err := url.ParseQuery(u.RawQuery); err != nil {
		return ErrInvalid
	}
	if r.ContentType != "" && r.ContentType != "application/json" {
		return ErrInvalid
	}
	if len(r.Body) != 0 {
		var raw json.RawMessage
		if decodeJSON(r.Body, &raw) != nil {
			return ErrInvalid
		}
	}
	return nil
}

// ParseRequest parses one endpoint-relative METHOD/path and optional exact JSON.
// Requests are never split as SQL, and no inline credentials or headers exist.
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
	}
	if err := validateRequest(r); err != nil {
		return r, err
	}
	return r, nil
}
