package couchdb

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf16"
	"unicode/utf8"
)

// decodeJSON rejects duplicate names, excessive nesting and invalid Unicode.
// Number spellings remain native bytes in RawMessage and json.Number values.
func decodeJSON(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > responseLimit || !utf8.Valid(raw) || !validEscapedUnicode(raw) {
		return ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var unique func(int) error
	unique = func(depth int) error {
		if depth > 64 {
			return ErrBounds
		}
		token, err := d.Token()
		if err != nil {
			return ErrProtocol
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, err := d.Token()
					name, ok := key.(string)
					if err != nil || !ok || seen[name] {
						return ErrProtocol
					}
					seen[name] = true
					if err := unique(depth + 1); err != nil {
						return err
					}
				}
			case '[':
				for d.More() {
					if err := unique(depth + 1); err != nil {
						return err
					}
				}
			default:
				return ErrProtocol
			}
			if _, err := d.Token(); err != nil {
				return ErrProtocol
			}
		}
		return nil
	}
	if err := unique(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(target) != nil {
		return ErrProtocol
	}
	return nil
}

// encoding/json replaces unpaired escaped UTF-16 surrogates with U+FFFD.
// Reject those spellings instead of silently changing a document or its ID.
func validEscapedUnicode(raw []byte) bool {
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
		code, ok := hex4(raw[i+1:])
		if !ok {
			return false
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, ok := hex4(raw[i+3:])
			if !ok || utf16.DecodeRune(rune(code), rune(low)) == utf8.RuneError {
				return false
			}
			i += 6
		}
	}
	return true
}

func hex4(raw []byte) (uint16, bool) {
	if len(raw) < 4 {
		return 0, false
	}
	var value uint16
	for _, ch := range raw[:4] {
		value <<= 4
		switch {
		case ch >= '0' && ch <= '9':
			value += uint16(ch - '0')
		case ch >= 'a' && ch <= 'f':
			value += uint16(ch-'a') + 10
		case ch >= 'A' && ch <= 'F':
			value += uint16(ch-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
