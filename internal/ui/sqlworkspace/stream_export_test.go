package sqlworkspace

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqlresult"
	"github.com/nyakang/nyadb/internal/services/queries"
)

type exportTestLease struct {
	testLease
	stream func(context.Context, string, string, sqlresult.Sink) (contract.SQLResult, error)
}

func (l *exportTestLease) ExecuteStream(ctx context.Context, db, sql string, sink sqlresult.Sink) (contract.SQLResult, error) {
	l.executions.Add(1)
	return l.stream(ctx, db, sql, sink)
}

func TestRunExportUsesOriginalLeaseOnceAndWritesAllResultSets(t *testing.T) {
	s := New(Dependencies{Connected: func(int64) bool { return true }})
	t.Cleanup(s.CloseAll)
	d := s.Open(2, "SELECT full", 8, "original", false)
	path := filepath.Join(t.TempDir(), "result.zip")
	s.SetPersistence(nil, Files{SavePath: func(string) (string, error) { return path, nil }, WriteStream: queries.WriteStream}, nil)
	l := &exportTestLease{stream: func(ctx context.Context, db, statement string, sink sqlresult.Sink) (contract.SQLResult, error) {
		if db != "original" || statement != "SELECT full" {
			return contract.SQLResult{}, errors.New("wrong owner")
		}
		for set := 0; set < 2; set++ {
			if e := sink(set, []contract.Column{{Name: "value", DatabaseType: "DECIMAL"}}, nil); e != nil {
				return contract.SQLResult{}, e
			}
			for row := 0; row < 1500; row++ {
				if e := sink(set, nil, []contract.Cell{{Kind: contract.Decimal, Text: "9007199254740993.0001"}}); e != nil {
					return contract.SQLResult{}, e
				}
			}
		}
		return contract.SQLResult{}, nil
	}}
	d.lease = l
	s.RunExport(d)
	consumeUntil(t, s, func() bool { return !d.loading })
	if d.err != "" || l.executions.Load() != 1 {
		t.Fatalf("export failed/replayed: %s", d.err)
	}
	z, e := zip.OpenReader(path)
	if e != nil {
		t.Fatal(e)
	}
	defer z.Close()
	if len(z.File) != 3 {
		t.Fatal("missing result files/manifest")
	}
	for _, f := range z.File {
		if f.Name == "manifest.json" {
			r, _ := f.Open()
			defer r.Close()
			var m struct {
				Version int
				Sets    []struct{ Rows int }
			}
			if e = json.NewDecoder(r).Decode(&m); e != nil || m.Version != 1 || len(m.Sets) != 2 || m.Sets[1].Rows != 1500 {
				t.Fatalf("incomplete export manifest: %+v %v", m, e)
			}
		}
	}
}

func TestRunExportCancellationWaitsForExecutorAndDoesNotPublish(t *testing.T) {
	s := New(Dependencies{Connected: func(int64) bool { return true }})
	t.Cleanup(s.CloseAll)
	d := s.Open(2, "SELECT wait", 8, "original", false)
	path := filepath.Join(t.TempDir(), "result.zip")
	if e := os.WriteFile(path, []byte("previous"), 0600); e != nil {
		t.Fatal(e)
	}
	s.SetPersistence(nil, Files{SavePath: func(string) (string, error) { return path, nil }, WriteStream: queries.WriteStream}, nil)
	started, release := make(chan struct{}), make(chan struct{})
	l := &exportTestLease{stream: func(ctx context.Context, _, _ string, sink sqlresult.Sink) (contract.SQLResult, error) {
		close(started)
		<-ctx.Done()
		<-release
		return contract.SQLResult{}, ctx.Err()
	}}
	d.lease = l
	s.RunExport(d)
	awaitSignal(t, started)
	s.RequestCancel(d)
	s.RunExport(d)
	if !d.loading || l.executions.Load() != 1 {
		t.Fatal("cancellation replayed or stopped before acknowledgement")
	}
	close(release)
	consumeUntil(t, s, func() bool { return !d.loading })
	b, e := os.ReadFile(path)
	if e != nil || string(b) != "previous" {
		t.Fatal("cancelled export replaced existing file")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatal("temporary export was not cleaned")
	}
}
