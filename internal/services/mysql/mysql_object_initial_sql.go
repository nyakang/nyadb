package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"strings"

	"github.com/nyakang/nyadb/internal/db/sqltext"
)

type initialSQLConnector struct {
	driver.Connector
	text string
}

// WrapMySQLInitialSQL initializes each newly authenticated physical session.
// It never retries initialization, including SQL that may already have run.
func WrapMySQLInitialSQL(c driver.Connector, text string) driver.Connector {
	if text == "" {
		return c
	}
	return &initialSQLConnector{Connector: c, text: text}
}
func (c *initialSQLConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, e := c.Connector.Connect(ctx)
	if e != nil {
		return nil, e
	}
	fail := func() (driver.Conn, error) {
		conn.Close()
		return nil, errors.New("physical-session initial SQL failed; completed steps are not replayed")
	}
	scanner := sqltext.NewScriptScanner(strings.NewReader(c.text))
	readMode := func() error {
		q, ok := conn.(driver.QueryerContext)
		if !ok {
			return errors.New("initial SQL mode unavailable")
		}
		rows, e := q.QueryContext(ctx, "SELECT @@SESSION.sql_mode", nil)
		if e != nil {
			return e
		}
		defer rows.Close()
		values := make([]driver.Value, len(rows.Columns()))
		if e = rows.Next(values); e != nil {
			return e
		}
		if len(values) != 1 {
			return errors.New("unexpected SQL mode")
		}
		var mode string
		switch v := values[0].(type) {
		case string:
			mode = v
		case []byte:
			mode = string(v)
		default:
			return errors.New("invalid SQL mode")
		}
		scanner.BackslashEscapes = !strings.Contains(strings.ToUpper(mode), "NO_BACKSLASH_ESCAPES")
		return nil
	}
	if e = readMode(); e != nil {
		return fail()
	}
	for {
		stmt, e := scanner.Next()
		if e == io.EOF {
			return conn, nil
		}
		if e != nil {
			return fail()
		}
		if ctx.Err() != nil {
			return fail()
		}
		if sqlReturnsRows(stmt) {
			q, ok := conn.(driver.QueryerContext)
			if !ok {
				return fail()
			}
			rows, e := q.QueryContext(ctx, stmt, nil)
			if e != nil {
				return fail()
			}
			values := make([]driver.Value, len(rows.Columns()))
			for {
				e = rows.Next(values)
				if e == io.EOF {
					if next, ok := rows.(driver.RowsNextResultSet); ok && next.HasNextResultSet() {
						if e = next.NextResultSet(); e != nil {
							rows.Close()
							return fail()
						}
						values = make([]driver.Value, len(rows.Columns()))
						continue
					}
					break
				}
				if e != nil {
					rows.Close()
					return fail()
				}
			}
			if e = rows.Close(); e != nil {
				return fail()
			}
		} else {
			x, ok := conn.(driver.ExecerContext)
			if !ok {
				return fail()
			}
			if _, e = x.ExecContext(ctx, stmt, nil); e != nil {
				return fail()
			}
		}
		if strings.Contains(strings.ToUpper(stmt), "SQL_MODE") {
			if e = readMode(); e != nil {
				return fail()
			}
		}
	}
}
