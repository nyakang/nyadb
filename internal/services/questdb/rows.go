package questdb

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type rowFrames interface {
	FieldDescriptions() []pgconn.FieldDescription
	Next() bool
	RawValues() [][]byte
	Close()
	Err() error
	CommandTag() pgconn.CommandTag
}

// QuestDB does not report PostgreSQL's standard_conforming_strings parameter.
// Parameterless SQL is sent unchanged through pgconn's native simple query;
// values use typed extended bindings instead of client-side interpolation.
func openRows(ctx context.Context, c *pgx.Conn, sql string, args []any) (rowFrames, error) {
	if len(args) != 0 {
		params := []any{pgx.QueryExecModeExec, pgx.QueryResultFormats{0}}
		return c.Query(ctx, sql, append(params, args...)...)
	}
	results := c.PgConn().Exec(ctx, sql)
	if !results.NextResult() {
		if err := results.Close(); err != nil {
			return nil, err
		}
		return nil, contract.ErrUnsupported
	}
	return &simpleRows{reader: results.ResultReader(), results: results}, nil
}

type simpleRows struct {
	reader  *pgconn.ResultReader
	results *pgconn.MultiResultReader
	closed  bool
	tag     pgconn.CommandTag
	err     error
}

func (r *simpleRows) FieldDescriptions() []pgconn.FieldDescription {
	return r.reader.FieldDescriptions()
}
func (r *simpleRows) Next() bool                    { return r.reader.NextRow() }
func (r *simpleRows) RawValues() [][]byte           { return r.reader.Values() }
func (r *simpleRows) CommandTag() pgconn.CommandTag { return r.tag }
func (r *simpleRows) Err() error                    { return r.err }
func (r *simpleRows) Close() {
	if r.closed {
		return
	}
	r.closed = true
	r.tag, r.err = r.reader.Close()
	if err := r.results.Close(); r.err == nil {
		r.err = err
	}
}
