package schemaddl

import (
	"errors"
	"strings"
)

var ErrOpenGaussNotNull = errors.New("native column-store tables do not support changing existing nullability")

// New columns can declare nullability; native column-store ALTER cannot
// change it on an existing physical column. The designer and planner share
// this rule without borrowing PostgreSQL constraint/version semantics.
func OpenGaussNotNullEditable(original Snapshot, id string) error {
	if original.Engine != "opengauss" || original.Table.OpenGauss == nil {
		return ErrConflict
	}
	if !original.Exists {
		return nil
	}
	retained := false
	for _, column := range original.Table.Columns() {
		retained = retained || column.ID == id
	}
	if !retained {
		return nil
	}
	for _, option := range original.Table.OpenGauss.Parameters {
		if strings.EqualFold(strings.ReplaceAll(option, " ", ""), "orientation=column") {
			return ErrOpenGaussNotNull
		}
	}
	return nil
}
