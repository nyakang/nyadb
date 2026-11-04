package connectioneditor

import (
	"errors"
	"github.com/nyakang/nyadb/locales"
)

// FieldError carries stable source labels rather than translated positions.
type FieldError struct{ Field, Page, Message string }

func (e *FieldError) Error() string                { return locales.Text(e.Message) }
func fieldError(field, page, message string) error { return &FieldError{field, page, message} }
func (s *State) validationError(err error) {
	s.connectionError = err.Error()
	var field *FieldError
	if errors.As(err, &field) {
		for i, page := range s.connectionTabNames() {
			if page == field.Page {
				s.connectionEditorTab = i
				break
			}
		}
		s.validationField = field.Field
	}
}
