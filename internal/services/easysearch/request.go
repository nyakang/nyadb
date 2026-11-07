package easysearch

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"
)

// ParseRequest accepts one native METHOD /path request and its JSON or NDJSON
// body. It does not split a script into requests or infer SQL dialect behavior.
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
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return r, ErrInvalid
		}
		r.Method, r.Path = strings.ToUpper(fields[0]), fields[1]
		r.Body = []byte(remainder)
		break
	}
	r.ContentType = "application/json"
	u, err := url.Parse(r.Path)
	if err != nil {
		return r, ErrInvalid
	}
	ndjson := strings.HasSuffix(u.Path, "/_bulk") || strings.HasSuffix(u.Path, "/_msearch") || strings.HasSuffix(u.Path, "/_msearch/template")
	if len(bytes.TrimSpace(r.Body)) == 0 {
		r.Body = nil
	} else if ndjson {
		r.ContentType = "application/x-ndjson"
		if r.Body[len(r.Body)-1] != '\n' {
			return r, ErrInvalid
		}
		for _, line := range bytes.Split(bytes.TrimSpace(r.Body), []byte("\n")) {
			var object map[string]json.RawMessage
			if decodeJSON(line, &object) != nil || object == nil {
				return r, ErrInvalid
			}
		}
	} else {
		var body json.RawMessage
		if decodeJSON(r.Body, &body) != nil {
			return r, ErrInvalid
		}
	}
	if err = validateRequest(r); err != nil {
		return r, err
	}
	return r, nil
}
