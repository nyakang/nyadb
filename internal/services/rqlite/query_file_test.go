package rqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type queryFileReadFunc func([]byte) (int, error)

func (f queryFileReadFunc) Read(p []byte) (int, error) { return f(p) }

func TestSQLFileReadsCompleteInputBeforeOriginalRequest(t *testing.T) {
	var requests atomic.Int32
	const original = `{"results":[{"rows_affected":1},{"columns":["v"],"types":["integer"],"values":[[9007199254740993]]}],"raft_index":901}`
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var statements []string
		if err := json.NewDecoder(r.Body).Decode(&statements); err != nil || len(statements) != 2 || statements[0] != "INSERT INTO t VALUES(1)" || statements[1] != "SELECT v FROM t" || !r.URL.Query().Has("transaction") {
			t.Error("file statement or transaction ownership changed", err)
		}
		fmt.Fprint(w, original)
	}, func(p *connections.Profile) { p.RQLiteOptions.BatchTransaction = true })
	l := lease(t, s, 21)
	input := strings.NewReader("\uFEFFINSERT INTO t VALUES(1); SELECT v FROM t;")
	reader := queryFileReadFunc(func(p []byte) (int, error) {
		if requests.Load() != 0 {
			t.Error("file prefix was dispatched before the complete input")
		}
		if len(p) > 3 {
			p = p[:3]
		}
		return input.Read(p)
	})
	out, err := l.ExecuteFile(context.Background(), "main", reader)
	if err != nil || requests.Load() != 1 || out.NativeJSON != original || len(out.Steps) != 2 || out.AffectedRows != 1 || out.Sets[0].Rows[0][0].Text != "9007199254740993" {
		t.Fatal("file did not retain its complete original receipt", err)
	}
}

func TestSQLFileInvalidOrIncompleteInputNeverDispatchesPrefix(t *testing.T) {
	var requests atomic.Int32
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, `{"results":[{}]}`)
	}, nil)
	l := lease(t, s, 21)
	readError := errors.New("original file read failed")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name, database string
		ctx            context.Context
		reader         io.Reader
		want           error
	}{
		{"nil", "main", context.Background(), nil, nil},
		{"read error", "main", context.Background(), queryFileReadFunc(func(p []byte) (int, error) { return copy(p, "INSERT INTO t VALUES(1);"), readError }), readError},
		{"cancelled", "main", cancelled, strings.NewReader("INSERT INTO t VALUES(1)"), context.Canceled},
		{"wrong namespace", "other", context.Background(), strings.NewReader("INSERT INTO t VALUES(1)"), connections.ErrInvalid},
		{"truncated quote", "main", context.Background(), strings.NewReader("INSERT INTO t VALUES(1); SELECT 'broken"), connections.ErrInvalid},
		{"invalid utf8", "main", context.Background(), strings.NewReader("INSERT INTO t VALUES(1); SELECT '\xff'"), connections.ErrInvalid},
		{"nul", "main", context.Background(), strings.NewReader("INSERT INTO t VALUES(1); SELECT '\x00'"), connections.ErrInvalid},
		{"transaction state", "main", context.Background(), strings.NewReader("INSERT INTO t VALUES(1); BEGIN;"), contract.ErrUnsupported},
		{"statements", "main", context.Background(), strings.NewReader(strings.Repeat("SELECT 1;", 1025)), connections.ErrInvalid},
		{"file bytes", "main", context.Background(), strings.NewReader("\uFEFF" + strings.Repeat(" ", maxRequestBytes) + "SELECT 1"), connections.ErrInvalid},
		{"json framing bytes", "main", context.Background(), strings.NewReader("SELECT '" + strings.Repeat("\\", maxRequestBytes/2) + "'"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := l.ExecuteFile(tc.ctx, tc.database, tc.reader)
			if err == nil || tc.want != nil && !errors.Is(err, tc.want) || requests.Load() != 0 || out.NativeJSON != "" || out.AffectedRows != 0 || l.CheckQueryLease() != nil {
				t.Fatal("incomplete input dispatched or poisoned the original lease", err)
			}
		})
	}
}

func TestSQLFileCloseDuringReadWaitsForOriginalReaderAndDoesNotDispatch(t *testing.T) {
	started, acknowledge := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-acknowledge:
		default:
			close(acknowledge)
		}
	}()
	var requests atomic.Int32
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }, nil)
	l := lease(t, s, 21)
	result := make(chan error, 1)
	go func() {
		_, err := l.ExecuteFile(context.Background(), "main", queryFileReadFunc(func(p []byte) (int, error) {
			close(started)
			<-acknowledge
			return copy(p, "INSERT INTO t VALUES(1)"), io.EOF
		}))
		result <- err
	}()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-closed:
		t.Fatal("close released original reader before acknowledgement")
	case <-time.After(30 * time.Millisecond):
	}
	close(acknowledge)
	if err := <-result; !errors.Is(err, context.Canceled) && !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed file reader dispatched", err)
	}
	if err := <-closed; err != nil || requests.Load() != 0 {
		t.Fatal("file close dispatched a prefix", err)
	}
}

func TestSQLFileCancellationWaitsForCompleteWriteReceipt(t *testing.T) {
	started, acknowledge := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-acknowledge:
		default:
			close(acknowledge)
		}
	}()
	const original = `{"results":[{"rows_affected":1}],"raft_index":902}`
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-acknowledge
		fmt.Fprint(w, original)
	}, nil)
	l := lease(t, s, 21)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		out, err := l.ExecuteFile(ctx, "main", strings.NewReader("INSERT INTO t VALUES(1)"))
		if err == nil && (out.NativeJSON != original || out.AffectedRows != 1 || out.Steps[0].State != "completed") {
			err = errors.New("original file write receipt discarded")
		}
		result <- err
	}()
	<-started
	cancel()
	select {
	case <-result:
		t.Fatal("file cancellation settled before original write receipt")
	case <-time.After(30 * time.Millisecond):
	}
	close(acknowledge)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestSQLFileLostReceiptRequiresExplicitRecoveryAndNeverReadsReplay(t *testing.T) {
	var requests atomic.Int32
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		c, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = c.Close()
	}, nil)
	l := lease(t, s, 21)
	out, err := l.ExecuteFile(context.Background(), "main", strings.NewReader("INSERT INTO t VALUES(1); SELECT 1"))
	if !errors.Is(err, contract.ErrSQLSessionLost) || len(out.Steps) != 2 || out.Steps[0].State != "uncertain" || out.Steps[1].State != "uncertain" || requests.Load() != 1 {
		t.Fatal("file lost response was settled as failure or success", err)
	}
	_, err = l.ExecuteFile(context.Background(), "main", queryFileReadFunc(func([]byte) (int, error) {
		t.Error("lost lease read a replacement file")
		return 0, io.EOF
	}))
	if !errors.Is(err, contract.ErrSQLSessionLost) || requests.Load() != 1 {
		t.Fatal("lost file request replayed", err)
	}
	replacement, err := l.Replace()
	if err != nil || requests.Load() != 1 {
		t.Fatal("recovery replayed the original file", err)
	}
	replacement.Release()
}
