package dpi

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const schemaSQL = "SELECT SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID)"

// InitializeSchema selects and verifies the schema of a captured, unpublished
// physical session. DPI attribute 12354 alone does not change the server's
// current schema in the verified clients. Identifiers are double-quoted;
// no caller SQL or values are interpolated as expressions.
func InitializeSchema(ctx context.Context, conn driver.Conn, schema string) error {
	if !validInitialSchema(schema) {
		return errors.New("invalid bounded DM8 initial schema")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	exec, eok := conn.(driver.ExecerContext)
	query, qok := conn.(driver.QueryerContext)
	if !eok || !qok {
		return errors.New("DM8 native connection cannot initialize and verify schema")
	}
	if _, err := exec.ExecContext(ctx, `SET SCHEMA "`+strings.ReplaceAll(schema, `"`, `""`)+`"`, nil); err != nil {
		return err
	}
	rows, err := query.QueryContext(ctx, schemaSQL, nil)
	if err != nil {
		return err
	}
	if len(rows.Columns()) != 1 {
		return errors.Join(errors.New("DM8 initial schema result is invalid"), rows.Close(), ctx.Err())
	}
	values := make([]driver.Value, 1)
	err = rows.Next(values)
	if err == nil {
		var actual string
		switch value := values[0].(type) {
		case string:
			actual = value
		case []byte:
			actual = string(value)
		}
		if actual != schema {
			err = errors.New("DM8 initial schema differs from the explicit configuration")
		} else if next := rows.Next(values); next != io.EOF {
			err = next
			if err == nil {
				err = errors.New("DM8 initial schema result is ambiguous")
			}
		}
	}
	return errors.Join(err, rows.Close(), ctx.Err())
}

func validInitialSchema(schema string) bool {
	return schema != "" && len(schema) <= 128 && utf8.ValidString(schema) && !strings.ContainsAny(schema, "\x00\r\n")
}
