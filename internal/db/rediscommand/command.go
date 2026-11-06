// Package rediscommand parses one binary-safe native command without a driver dependency.
package rediscommand

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

var ErrCommand = errors.New("Redis requires one command as an argument array or a quoted command line")

// Parse accepts JSON arrays of strings or {"base64":"..."} byte arguments,
// and CLI arguments with single/double quotes and backslash escapes.
func Parse(text string) ([]string, error) {
	if len(text) > 1<<20 || !utf8.ValidString(text) {
		return nil, ErrCommand
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "[") {
		if ValidateJSON(text) != nil {
			return nil, ErrCommand
		}
		d := json.NewDecoder(strings.NewReader(text))
		var raw []json.RawMessage
		if d.Decode(&raw) != nil || d.Decode(new(any)) != io.EOF || len(raw) == 0 || len(raw) > 4096 {
			return nil, ErrCommand
		}
		out := make([]string, len(raw))
		for i, v := range raw {
			if len(v) > 0 && v[0] == '"' && json.Unmarshal(v, &out[i]) == nil {
				continue
			}
			var members map[string]json.RawMessage
			if json.Unmarshal(v, &members) != nil || len(members) != 1 || len(members["base64"]) == 0 || members["base64"][0] != '"' {
				return nil, ErrCommand
			}
			var b struct {
				Base64 string `json:"base64"`
			}
			dec := json.NewDecoder(strings.NewReader(string(v)))
			dec.DisallowUnknownFields()
			if dec.Decode(&b) != nil || !strings.HasPrefix(string(v), "{") {
				return nil, ErrCommand
			}
			bytes, e := base64.StdEncoding.DecodeString(b.Base64)
			if e != nil {
				return nil, ErrCommand
			}
			out[i] = string(bytes)
		}
		if out[0] == "" {
			return nil, ErrCommand
		}
		return out, nil
	}
	var out []string
	for i := 0; i < len(text); {
		for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
			i++
		}
		if i == len(text) {
			break
		}
		var b strings.Builder
		quote := byte(0)
		started := false
		for i < len(text) {
			ch := text[i]
			i++
			if ch == '\n' || ch == '\r' || ch == 0 {
				return nil, ErrCommand
			}
			if quote == 0 && (ch == ' ' || ch == '\t') {
				break
			}
			if ch == '\'' || ch == '"' {
				if quote == 0 {
					quote = ch
					started = true
					continue
				}
				if quote == ch {
					quote = 0
					continue
				}
			}
			if ch == '\\' && quote != '\'' {
				if i == len(text) {
					return nil, ErrCommand
				}
				ch = text[i]
				i++
				switch ch {
				case 'n':
					ch = '\n'
				case 'r':
					ch = '\r'
				case 't':
					ch = '\t'
				case 'x':
					if i+2 > len(text) {
						return nil, ErrCommand
					}
					n, e := strconv.ParseUint(text[i:i+2], 16, 8)
					if e != nil {
						return nil, ErrCommand
					}
					ch = byte(n)
					i += 2
				}
			}
			b.WriteByte(ch)
			started = true
		}
		if quote != 0 || !started {
			return nil, ErrCommand
		}
		out = append(out, b.String())
		if len(out) > 4096 {
			return nil, ErrCommand
		}
	}
	if len(out) == 0 || out[0] == "" {
		return nil, ErrCommand
	}
	return out, nil
}
func StatementAt(text string, cursor int) string {
	if strings.HasPrefix(strings.TrimSpace(text), "[") {
		return strings.TrimSpace(text)
	}
	cursor = max(0, min(cursor, len(text)))
	start := strings.LastIndex(text[:cursor], "\n") + 1
	end := len(text)
	if n := strings.IndexByte(text[cursor:], '\n'); n >= 0 {
		end = cursor + n
	}
	return strings.TrimSpace(text[start:end])
}

// ValidateJSON rejects duplicate keys and excessive nesting before decoding.
func ValidateJSON(text string) error {
	d := json.NewDecoder(strings.NewReader(text))
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return ErrCommand
		}
		token, e := d.Token()
		if e != nil {
			return ErrCommand
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, e := d.Token()
					if e != nil {
						return ErrCommand
					}
					k, ok := key.(string)
					if !ok || seen[k] {
						return ErrCommand
					}
					seen[k] = true
					if e = value(depth + 1); e != nil {
						return e
					}
				}
			case '[':
				for d.More() {
					if e = value(depth + 1); e != nil {
						return e
					}
				}
			default:
				return ErrCommand
			}
			_, e = d.Token()
			if e != nil {
				return ErrCommand
			}
		}
		return nil
	}
	if value(0) != nil {
		return ErrCommand
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrCommand
	}
	return nil
}

// Format produces an argument array while preserving non-UTF-8 bytes.
func Format(text string) (string, error) {
	args, e := Parse(text)
	if e != nil {
		return "", e
	}
	values := make([]any, len(args))
	for i, a := range args {
		if utf8.ValidString(a) {
			values[i] = a
		} else {
			values[i] = map[string]string{"base64": base64.StdEncoding.EncodeToString([]byte(a))}
		}
	}
	raw, e := json.Marshal(values)
	return string(raw), e
}
