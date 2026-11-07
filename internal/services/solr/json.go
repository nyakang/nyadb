package solr

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// decodeJSON rejects duplicate members instead of silently replacing values.
// RawMessage fields keep integer/decimal spellings and nested document bytes.
func decodeJSON(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > responseLimit || !utf8.Valid(raw) {
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
					key, e := d.Token()
					name, ok := key.(string)
					if e != nil || !ok || seen[name] {
						return ErrProtocol
					}
					seen[name] = true
					if e = unique(depth + 1); e != nil {
						return e
					}
				}
			case '[':
				for d.More() {
					if err = unique(depth + 1); err != nil {
						return err
					}
				}
			default:
				return ErrProtocol
			}
			if _, err = d.Token(); err != nil {
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
