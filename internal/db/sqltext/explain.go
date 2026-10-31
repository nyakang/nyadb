package sqltext

import (
	"errors"
	"io"
	"strings"
)

// SingleMySQLStatement validates the preview before adding EXPLAIN. The
// workspace has no physical-session SQL mode snapshot, so ambiguous quoting
// must be safe under both backslash modes. Client directives and executable
// comments are unsuitable for an EXPLAIN preview.
func SingleMySQLStatement(source string) error {
	tokens, err := Tokens(source)
	if err != nil {
		return err
	}
	for _, token := range tokens {
		if Word(token, "DELIMITER") || token.Comment && strings.HasPrefix(token.Text, "/*!") {
			return errors.New("EXPLAIN does not accept client directives or executable comments")
		}
	}
	for _, escapes := range []bool{true, false} {
		scanner := NewScriptScanner(strings.NewReader(source))
		scanner.BackslashEscapes = escapes
		if _, err = scanner.Next(); err != nil {
			return errors.New("EXPLAIN requires one complete, unambiguous SQL statement")
		}
		if _, err = scanner.Next(); err != io.EOF {
			return errors.New("EXPLAIN requires exactly one SQL statement")
		}
	}
	return nil
}

func SinglePortableStatement(source string) error {
	parts, err := SplitPortable(source)
	if err != nil {
		return err
	}
	if len(parts) != 1 {
		return errors.New("EXPLAIN requires exactly one SQL statement")
	}
	return nil
}
