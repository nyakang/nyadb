package engines

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

// Audited native engines advertise this bounded file operation. Embedded
// queryLease retains the original physical session and replacement lifecycle.
type openGaussQueryLease struct{ *queryLease }

var _ contract.QueryFileLease = (*openGaussQueryLease)(nil)

func (l *openGaussQueryLease) ExecuteFile(ctx context.Context, schema string, reader io.Reader) (contract.SQLResult, error) {
	l.mu.Lock()
	released := l.released
	l.mu.Unlock()
	if released || l.session.isClosed() {
		return contract.SQLResult{}, contract.ErrRevoked
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	defer func() { stop(); cancel() }()
	if err := child.Err(); err != nil {
		return contract.SQLResult{}, err
	}
	if reader == nil {
		return contract.SQLResult{}, errors.New("SQL file reader is absent")
	}
	// Read and lexically validate the complete bounded file before executing any
	// of it. An incomplete body/read error cannot leave earlier statements run.
	data, err := io.ReadAll(io.LimitReader(openGaussScriptReader{child, reader}, (8<<20)+1))
	if err != nil {
		return contract.SQLResult{}, err
	}
	text := strings.TrimPrefix(string(data), "\uFEFF")
	if _, err = sqltext.SplitOpenGauss(text); err != nil {
		return contract.SQLResult{}, err
	}
	return l.Execute(child, schema, text)
}

type openGaussScriptReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r openGaussScriptReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
