package rqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func retainNativeFileReceipt(t *testing.T, name, raw string) {
	t.Helper()
	if raw == "" || !json.Valid([]byte(raw)) {
		t.Fatal("complete original native file receipt absent")
	}
	if dir := os.Getenv("NYADB_RQLITE_EVIDENCE"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeRQLiteSQLFileTypedTriggerAndAtomicRollback(t *testing.T) {
	p := nativeFixture(t, "")
	p.RQLiteOptions.BatchTransaction = true
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 301)
	file := "\uFEFF-- original file\nCREATE TABLE file_rows(id INTEGER PRIMARY KEY,note TEXT,payload BLOB);" +
		"CREATE TABLE file_audit(note TEXT);" +
		"CREATE TRIGGER [file trigger] AFTER INSERT ON file_rows BEGIN INSERT INTO file_audit VALUES(CASE WHEN new.id=9007199254740993 THEN 'BEGIN; CASE; END;' ELSE 'other' END); INSERT INTO file_audit VALUES('second;'); END;" +
		"INSERT INTO file_rows VALUES(9007199254740993,CAST(X'610062' AS TEXT),X'00FF27');" +
		"SELECT id,note,payload FROM file_rows;SELECT note FROM file_audit ORDER BY rowid;"
	out, err := l.ExecuteFile(context.Background(), "main", strings.NewReader(file))
	if err != nil || len(out.Steps) != 6 || len(out.Sets) != 2 || out.AffectedRows != 1 {
		t.Fatal("native SQL file failed", err)
	}
	row := out.Sets[0].Rows[0]
	if row[0].Kind != contract.Integer || row[0].Text != "9007199254740993" || row[1].Text != "a\x00b" || row[2].Kind != contract.Binary || !bytes.Equal(row[2].Bytes, []byte{0, 255, 39}) || len(out.Sets[1].Rows) != 2 || out.Sets[1].Rows[0][0].Text != "BEGIN; CASE; END;" {
		t.Fatal("file trigger statements or native complete types changed")
	}
	retainNativeFileReceipt(t, "file-typed-trigger", out.NativeJSON)
	out, err = l.ExecuteFile(context.Background(), "main", strings.NewReader("INSERT INTO file_rows VALUES(1,'rolled back',NULL); INSERT INTO file_rows VALUES(1,'duplicate',NULL); INSERT INTO file_rows VALUES(2,'not executed',NULL);"))
	if err == nil || out.AffectedRows != 0 || len(out.Steps) != 3 || out.Steps[0].State != "rolled_back" || out.Steps[1].State != "failed" || out.Steps[2].State != "not_executed" || l.CheckQueryLease() != nil {
		t.Fatal("native file rollback lost its original transaction", err)
	}
	retainNativeFileReceipt(t, "file-atomic-rollback", out.NativeJSON)
	actual := nativeExecute(t, l, "SELECT count(*) FROM file_rows; SELECT count(*) FROM file_audit")
	if actual.Sets[0].Rows[0][0].Text != "1" || actual.Sets[1].Rows[0][0].Text != "2" {
		t.Fatal("native failed file retained prefix rows or trigger effects")
	}
}

func TestNativeRQLiteSQLFileNontransactionalPartialReceiptAndReadOnly(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 302)
	nativeExecute(t, l, "CREATE TABLE partial_file(id INTEGER PRIMARY KEY)")
	out, err := l.ExecuteFile(context.Background(), "main", strings.NewReader("INSERT INTO partial_file VALUES(1);INSERT INTO partial_file VALUES(1);INSERT INTO partial_file VALUES(2);SELECT count(*) FROM partial_file;"))
	if err == nil || len(out.Steps) != 4 || out.Steps[0].State != "completed" || out.Steps[1].State != "failed" || out.Steps[2].State != "completed" || out.AffectedRows != 2 || out.Sets[0].Rows[0][0].Text != "2" {
		t.Fatal("native nontransactional file invented whole-file rollback", err)
	}
	retainNativeFileReceipt(t, "file-nontransactional-partial", out.NativeJSON)
	p.Username, p.RQLiteOptions.ReadOnly = "reader", true
	reader := lease(t, openNative(t, p, "reader-password"), 303)
	out, err = reader.ExecuteFile(context.Background(), "main", strings.NewReader("\uFEFFSELECT id FROM partial_file ORDER BY id; SELECT count(*) FROM partial_file"))
	if err != nil || len(out.Sets) != 2 || len(out.Sets[0].Rows) != 2 {
		t.Fatal("query-only original credentials could not run a SQL file", err)
	}
	retainNativeFileReceipt(t, "file-query-only", out.NativeJSON)
	out, err = reader.ExecuteFile(context.Background(), "main", strings.NewReader("INSERT INTO partial_file VALUES(3)"))
	if err == nil || out.AffectedRows != 0 || reader.CheckQueryLease() != nil {
		t.Fatal("read-only file confirmed a write or lost an acknowledged lease", err)
	}
	if actual := nativeExecute(t, l, "SELECT count(*) FROM partial_file"); actual.Rows[0][0] != "2" {
		t.Fatal("read-only file modified original native rows")
	}
}

func TestNativeRQLiteSQLFileLostAppliedReceiptNeverReplays(t *testing.T) {
	p := nativeFixture(t, "")
	original := openNative(t, p, "original-fixture-password")
	l := lease(t, original, 304)
	nativeExecute(t, l, "CREATE TABLE file_lost(id INTEGER PRIMARY KEY)")
	var writes atomic.Int32
	applied := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		url := original.config.Endpoint + r.URL.RequestURI()
		request, err := http.NewRequest(r.Method, url, bytes.NewReader(body))
		if err != nil {
			t.Error(err)
			return
		}
		request.Header = r.Header.Clone()
		request.GetBody = nil
		response, err := original.client.Do(request)
		if err != nil {
			t.Error(err)
			return
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(string(body), "INSERT INTO file_lost") {
			writes.Add(1)
			applied <- string(raw)
			c, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = c.Close()
			return
		}
		for key, values := range response.Header {
			w.Header()[key] = values
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(proxy.Close)
	proxied := testProfile(t, proxy.URL)
	proxied.Username, proxied.RQLiteOptions.Auth = "fixture", "basic"
	q := lease(t, openNative(t, proxied, "original-fixture-password"), 305)
	out, err := q.ExecuteFile(context.Background(), "main", strings.NewReader("INSERT INTO file_lost VALUES(1); SELECT count(*) FROM file_lost"))
	if !errors.Is(err, contract.ErrSQLSessionLost) || len(out.Steps) != 2 || out.Steps[0].State != "uncertain" || out.Steps[1].State != "uncertain" || writes.Load() != 1 {
		t.Fatal("lost original applied file was declared rolled back or committed", err)
	}
	retainNativeFileReceipt(t, "file-lost-original-applied", <-applied)
	if _, err := q.ExecuteFile(context.Background(), "main", strings.NewReader("INSERT INTO file_lost VALUES(1)")); !errors.Is(err, contract.ErrSQLSessionLost) || writes.Load() != 1 {
		t.Fatal("lost native file was replayed", err)
	}
	replacement, err := q.Replace()
	if err != nil || writes.Load() != 1 {
		t.Fatal("explicit file recovery replayed a write", err)
	}
	if err := replacement.Preconnect(context.Background()); err != nil || writes.Load() != 1 {
		t.Fatal("explicit original-profile recovery changed file ownership", err)
	}
	if actual := nativeExecute(t, l, "SELECT count(*) FROM file_lost"); actual.Rows[0][0] != "1" {
		t.Fatal("original native file was lost or replayed")
	}
}
