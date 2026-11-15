package sqlworkspace

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

type scriptTestLease struct {
	testLease
	file func(context.Context, string, io.Reader) (contract.SQLResult, error)
}

type checkedScriptLease struct {
	scriptTestLease
	revoked atomic.Bool
}

func (l *checkedScriptLease) CheckQueryLease() error {
	if l.revoked.Load() {
		return contract.ErrRevoked
	}
	return nil
}

func TestSQLFileCompletionChecksOriginalLeaseAndRetainsConfirmedReceipt(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(map[bool]string{false: "original generation revoked", true: "original lease replaced"}[replaced], func(t *testing.T) {
			s := New(Dependencies{Connected: func(int64) bool { return true }})
			defer s.CloseAll()
			d := s.Open(2, "SELECT unsaved", 8, "original", false)
			l := &checkedScriptLease{scriptTestLease: scriptTestLease{file: func(context.Context, string, io.Reader) (contract.SQLResult, error) {
				return contract.SQLResult{NativeJSON: `{"original":true}`, Steps: []contract.StatementOutcome{{State: "completed", AffectedRows: 1}}, AffectedRows: 1}, nil
			}}}
			d.lease = l
			s.SetPersistence(nil, Files{OpenPath: func() (string, error) { return "original.sql", nil }, OpenReader: func(string) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("INSERT INTO original VALUES(1)")), nil
			}}, nil)
			completed := make(chan struct{}, 1)
			s.Invalidate = func() { completed <- struct{}{} }
			s.RunFile(d)
			awaitSignal(t, completed)
			if replaced {
				d.lease = &scriptTestLease{}
			} else {
				l.revoked.Store(true)
			}
			s.Consume()
			if d.Loading() || !d.NeedsRecovery() || d.Result() == nil || d.Result().NativeJSON != `{"original":true}` || d.Result().AffectedRows != 1 || d.RecoveryProfile() != 8 || d.RecoverySchema() != "original" || d.Editor.Buffer.String() != "SELECT unsaved" {
				t.Fatal("file completion established a replacement session or lost original evidence", d.Error())
			}
			if replaced {
				l.Release()
			}
		})
	}
}

func (l *scriptTestLease) ExecuteFile(ctx context.Context, db string, r io.Reader) (contract.SQLResult, error) {
	l.executions.Add(1)
	return l.file(ctx, db, r)
}
func TestSQLFileUsesOriginalLeaseAndRetainsEditorText(t *testing.T) {
	s := New(Dependencies{Connected: func(int64) bool { return true }})
	defer s.CloseAll()
	d := s.Open(2, "SELECT unsaved", 8, "original", false)
	l := &scriptTestLease{file: func(ctx context.Context, db string, r io.Reader) (contract.SQLResult, error) {
		b, e := io.ReadAll(r)
		if db != "original" || string(b) != "SELECT 'file';" {
			t.Error("file lost original owner")
		}
		return contract.SQLResult{Steps: []contract.StatementOutcome{{State: "executed"}}}, e
	}}
	d.lease = l
	s.SetPersistence(nil, Files{OpenPath: func() (string, error) { return "script.sql", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("SELECT 'file';")), nil }}, nil)
	s.RunFile(d)
	consumeUntil(t, s, func() bool { return !d.loading })
	if l.executions.Load() != 1 || d.Editor.Buffer.String() != "SELECT unsaved" || d.err != "" {
		t.Fatal("SQL file modified text or replayed")
	}
}
func TestSQLFileCancellationWaitsForOriginalExecutorReceipt(t *testing.T) {
	s := New(Dependencies{Connected: func(int64) bool { return true }})
	defer s.CloseAll()
	d := s.Open(2, "original", 8, "db", false)
	cancelled := make(chan struct{})
	ack := make(chan struct{})
	d.lease = &scriptTestLease{file: func(ctx context.Context, _ string, _ io.Reader) (contract.SQLResult, error) {
		<-ctx.Done()
		close(cancelled)
		<-ack
		return contract.SQLResult{Steps: []contract.StatementOutcome{{State: "uncertain"}}}, ctx.Err()
	}}
	s.SetPersistence(nil, Files{OpenPath: func() (string, error) { return "script", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("INSERT")), nil }}, nil)
	s.RunFile(d)
	s.RequestCancel(d)
	<-cancelled
	s.Consume()
	if !d.loading {
		t.Fatal("cancel settled before executor receipt")
	}
	close(ack)
	consumeUntil(t, s, func() bool { return !d.loading })
	if d.result == nil || d.result.Steps[0].State != "uncertain" {
		t.Fatal("uncertain step discarded")
	}
}
