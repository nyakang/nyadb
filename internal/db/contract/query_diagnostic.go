package contract

import (
	"errors"
	"unicode/utf8"
)

type QueryDiagnostic struct {
	Code, Detail, Hint, SQL string
	Statement               int  // One-based submitted statement number.
	Position                *int // UTF-8 byte offset in SQL, only when provided reliably.
}
type QueryError struct {
	Cause      error
	Diagnostic QueryDiagnostic
}

func (e *QueryError) Error() string { return e.Cause.Error() }
func (e *QueryError) Unwrap() error { return e.Cause }
func QueryDiagnosis(err error) *QueryDiagnostic {
	var e *QueryError
	if errors.As(err, &e) {
		copy := e.Diagnostic
		return &copy
	}
	return nil
}

// Database positions count characters from one. Invalid offsets remain unknown.
func QueryCharacterPosition(source string, position int) *int {
	if position <= 0 || !utf8.ValidString(source) {
		return nil
	}
	count := 1
	for offset := range source {
		if count == position {
			n := offset
			return &n
		}
		count++
	}
	if position == count {
		n := len(source)
		return &n
	}
	return nil
}
