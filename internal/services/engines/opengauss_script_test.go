package engines

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type openGaussRecordedQueries struct{ queries []string }

func (q *openGaussRecordedQueries) ExecContext(_ context.Context, text string, _ ...any) (sql.Result, error) {
	q.queries = append(q.queries, text)
	return openGaussScriptResult{}, nil
}
func (*openGaussRecordedQueries) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("unexpected result-producing query")
}

type openGaussScriptResult struct{}

func (openGaussScriptResult) RowsAffected() (int64, error) { return 0, nil }
func (openGaussScriptResult) LastInsertId() (int64, error) { return 0, nil }

func TestOpenGaussExecutorSendsNativeBodyAsOneRequest(t *testing.T) {
	q := &openGaussRecordedQueries{}
	body := "CREATE PROCEDURE p() AS BEGIN INSERT INTO t VALUES('one;two'); NULL; END;"
	out, err := execute(context.Background(), q, connections.OpenGauss, body+"\n/\nINSERT INTO t VALUES('after');", nil)
	if err != nil || len(q.queries) != 2 || q.queries[0] != body || len(out.Steps) != 2 {
		t.Fatal("routine was fragmented or slash was sent", err, q.queries, out.Steps)
	}
	q.queries = nil
	_, err = execute(context.Background(), q, connections.OpenGauss, "INSERT INTO t VALUES('before'); CREATE PROCEDURE p() AS BEGIN NULL;", nil)
	if err == nil || len(q.queries) != 0 {
		t.Fatal("malformed later body allowed earlier write", err, q.queries)
	}
}

type openGaussFilePhysicalLease struct{ text, schema string }

func (l *openGaussFilePhysicalLease) SetSchema(_ context.Context, schema string) error {
	l.schema = schema
	return nil
}
func (l *openGaussFilePhysicalLease) Execute(_ context.Context, text string) (contract.SQLResult, error) {
	l.text = text
	return contract.SQLResult{}, nil
}
func (*openGaussFilePhysicalLease) Stream(context.Context, string, contract.StreamSink) (contract.SQLResult, error) {
	return contract.SQLResult{}, errors.New("unexpected streaming")
}
func (*openGaussFilePhysicalLease) Close() error { return nil }

type openGaussFailedFileReader struct{}

func (openGaussFailedFileReader) Read(p []byte) (int, error) {
	return copy(p, "INSERT INTO t VALUES(1);"), io.ErrUnexpectedEOF
}

func TestOpenGaussFilesValidateAllInputAndKeepOriginalPhysicalLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	physical := &openGaussFilePhysicalLease{}
	l := &openGaussQueryLease{&queryLease{session: &Session{ctx: ctx, kind: connections.OpenGauss}, ctx: ctx, cancel: func() {}, lease: physical}}
	for _, r := range []io.Reader{
		strings.NewReader("INSERT INTO t VALUES(1); CREATE PROCEDURE p() AS BEGIN NULL;"),
		openGaussFailedFileReader{}, strings.NewReader(strings.Repeat("x", (8<<20)+1)), nil,
	} {
		if _, err := l.ExecuteFile(ctx, "original", r); err == nil || physical.text != "" || physical.schema != "" {
			t.Fatal("failed file initialized/executed a session", err)
		}
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, err := l.ExecuteFile(cancelled, "original", strings.NewReader("SELECT 1")); !errors.Is(err, context.Canceled) || physical.text != "" {
		t.Fatal("pre-cancelled file executed", err)
	}
	text := "CREATE PROCEDURE p() AS BEGIN NULL; END;\n/\nSELECT 2;"
	if _, err := l.ExecuteFile(ctx, "original", strings.NewReader("\uFEFF"+text)); err != nil || physical.text != text || physical.schema != "original" || l.lease != physical {
		t.Fatal("file lost original session or BOM handling", err)
	}
	l.Release()
	if _, err := l.ExecuteFile(ctx, "original", strings.NewReader(text)); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("released file lease still executed", err)
	}
}

func TestOpenGaussFileFeatureDoesNotChangeOtherEngineLeases(t *testing.T) {
	for _, kind := range []connections.Kind{connections.OpenGauss, connections.PostgreSQL, connections.CockroachDB, connections.SQLite} {
		s := &Session{ctx: context.Background(), kind: kind}
		l, err := s.ReserveQuery(1)
		if err != nil {
			t.Fatal(err)
		}
		_, files := l.(contract.QueryFileLease)
		l.Release()
		if files != (kind == connections.OpenGauss) {
			t.Fatal("wrong engine advertised SQL file support", kind)
		}
	}
}
