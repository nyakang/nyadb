package schemaddl

import "strings"

// OpenGaussCastBodySignature compares a known native SQL forwarder without
// submitting its body to the server for parsing or constant folding. Only
// whitespace and unquoted word case are normalized; literals, qualification,
// parameter numbers and punctuation remain part of the signature.
func OpenGaussCastBodySignature(raw string) (string, error) {
	if len(raw) > 64<<10 {
		return "", ErrUnsupported
	}
	ts, err := postgresTokens(raw, true)
	if err != nil || len(ts) == 0 || !ts[0].word("SELECT") {
		return "", ErrUnsupported
	}
	parts := make([]string, 0, len(ts))
	end := 0
	for _, t := range ts {
		if strings.TrimSpace(raw[end:t.start]) != "" {
			return "", ErrUnsupported // Comments are not formatting changes.
		}
		end = t.end
		value := t.text
		if t.kind == 'w' {
			value = strings.ToLower(value)
		} else if t.kind == 's' || t.kind == 'i' {
			value = raw[t.start:t.end]
		}
		parts = append(parts, Signature(struct {
			Kind byte
			Text string
		}{t.kind, value}))
	}
	if strings.TrimSpace(raw[end:]) != "" {
		return "", ErrUnsupported
	}
	return Signature(parts), nil
}
