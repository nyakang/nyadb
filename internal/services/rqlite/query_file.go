package rqlite

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// ExecuteFile keeps the original lease locked while reading the complete file
// and settling its single original HTTP request. No prefix is dispatched on a
// read error, cancellation, invalid SQL or an exceeded native request bound.
func (l *queryLease) ExecuteFile(ctx context.Context, database string, reader io.Reader) (contract.SQLResult, error) {
	l.ops.Lock()
	defer l.ops.Unlock()
	child, done, err := l.admission(ctx)
	if err != nil {
		return contract.SQLResult{}, err
	}
	defer done()
	if database != "" && database != "main" {
		return contract.SQLResult{}, connections.ErrInvalid
	}
	if err := child.Err(); err != nil {
		return contract.SQLResult{}, err
	}
	if reader == nil {
		return contract.SQLResult{}, errors.New("SQL file reader is absent")
	}
	// A UTF-8 BOM is a file marker, not SQLite statement text. The SQL and its
	// JSON framing still use the same bounds as interactive native execution.
	data, err := io.ReadAll(io.LimitReader(queryFileReader{child, reader}, maxRequestBytes+4))
	if err != nil {
		return contract.SQLResult{}, err
	}
	if err := child.Err(); err != nil {
		return contract.SQLResult{}, err
	}
	return l.execute(child, database, strings.TrimPrefix(string(data), "\uFEFF"), nil)
}

type queryFileReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r queryFileReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

var _ contract.QueryFileLease = (*queryLease)(nil)
