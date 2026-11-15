package sqlworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/rqlite"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// This fixture injects the unpublished adapter into the existing SQL workspace.
// Native fixture/UI proof does not establish public product registration.
func rqliteSQLFixture(t *testing.T) (contract.Session, string) {
	t.Helper()
	if os.Getenv("NYADB_RQLITE_SQL_IT") != "1" {
		t.Skip("requires the owned pinned native RQLite SQL-workspace fixture")
	}
	binary, version := os.Getenv("NYADB_RQLITE_BINARY"), os.Getenv("NYADB_RQLITE_VERSION")
	artifacts := map[string]string{
		"8.43.4": "3b6bd8ad0497777bdece2919b0dc0b7702b6121c0b7db8ebb49f664e59087dff",
		"10.7.1": "37b0be99b2e1d3ca2950c5d2d7432c444be8f3d7526331b2b85b205b8ee6933c",
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if artifacts[version] == "" || hex.EncodeToString(digest[:]) != artifacts[version] {
		t.Fatal("SQL workspace fixture requires its explicitly pinned original official artifact")
	}
	dir := t.TempDir()
	port := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().String()
	}
	httpAddr, raftAddr := port(), port()
	logPath := filepath.Join(dir, "server.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-node-id", filepath.Base(dir), "-http-addr", httpAddr, "-raft-addr", raftAddr, filepath.Join(dir, "data"))
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	t.Logf("owned native RQLite SQL workspace original process pid=%d version=%s artifact=%s", cmd.Process.Pid, version, artifacts[version])
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("original SQL workspace fixture process did not exit")
		}
		_ = log.Close()
		if dir := os.Getenv("NYADB_RQLITE_SQL_EVIDENCE"); dir != "" {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Error(err)
				return
			}
			raw, err := os.ReadFile(logPath)
			if err == nil {
				err = os.WriteFile(filepath.Join(dir, fmt.Sprintf("server-%d.log", cmd.Process.Pid)), raw, 0600)
			}
			if err != nil {
				t.Error(err)
			}
		}
	})
	endpoint := "http://" + httpAddr
	p := rqliteSQLProfile(t, endpoint)
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case err := <-done:
			done <- err
			t.Fatal("owned original SQL fixture exited before readiness", err)
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		s, err := rqlite.OpenSession(ctx, p, "")
		cancel()
		if err == nil {
			actual, _ := s.(*rqlite.Session).ServerVersions()
			if strings.TrimPrefix(actual, "v") != version {
				_ = s.Close()
				t.Fatal("native SQL fixture product version changed")
			}
			t.Cleanup(func() { _ = s.Close() })
			return s, endpoint
		}
		if time.Now().After(deadline) {
			t.Fatal("owned SQL fixture did not become ready", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func rqliteSQLProfile(t *testing.T, endpoint string) connections.Profile {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.RQLite)
	p.ID, p.Name, p.Host, p.Port = 911, "original native SQL file", u.Hostname(), uint16(port)
	p.RQLiteOptions.BatchTransaction = true
	return p
}

func rqliteSQLState(t *testing.T, raw contract.Session) (*State, *Document) {
	t.Helper()
	runtime := EngineRuntime(func(id int64) (contract.Session, error) {
		if id != raw.ProfileID() || !raw.(*rqlite.Session).Alive() {
			return nil, contract.ErrRevoked
		}
		return raw, nil
	})
	s := New(Dependencies{Runtime: func() Runtime { return runtime }, Connected: runtime.IsConnected, QueryDialect: func(int64) string { return "sqlite" }})
	t.Cleanup(s.CloseAll)
	d := s.Open(501, "SELECT 'unsaved editor';", raw.ProfileID(), "main", false)
	s.Prepare(d)
	consumeUntil(t, s, func() bool { return d.prepareResults == nil })
	return s, d
}

func TestRQLiteNativeSQLWorkspaceFileKeepsOriginalTextAndCompleteReceipt(t *testing.T) {
	raw, _ := rqliteSQLFixture(t)
	s, d := rqliteSQLState(t, raw)
	const file = "\uFEFFCREATE TABLE workspace_file(id INTEGER PRIMARY KEY,note TEXT,payload BLOB);INSERT INTO workspace_file VALUES(9007199254740993,CAST(X'610062' AS TEXT),X'00FF27');SELECT * FROM workspace_file;"
	s.SetPersistence(nil, Files{OpenPath: func() (string, error) { return "original.sql", nil }, OpenReader: func(path string) (io.ReadCloser, error) {
		if path != "original.sql" {
			t.Error("original file path changed")
		}
		return io.NopCloser(strings.NewReader(file)), nil
	}}, nil)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(); s.View(c, d); s.Modals(c) }, 1280, 800)
	if err := tt.Click("执行 SQL 文件"); err != nil {
		t.Fatal(err)
	}
	waitSQLUI(t, tt, d)
	if d.Error() != "" || d.Editor.Buffer.String() != "SELECT 'unsaved editor';" || d.Result() == nil || len(d.Result().Steps) != 3 || d.Result().AffectedRows != 1 || d.NeedsRecovery() {
		t.Fatal("native file completion lost original document or editor", d.Error())
	}
	result := d.Result()
	row := result.Sets[0].Rows[0]
	if row[0].Text != "9007199254740993" || row[1].Text != "a\x00b" || !bytes.Equal(row[2].Bytes, []byte{0, 255, 39}) || result.NativeJSON == "" {
		t.Fatal("SQL workspace file lost complete original typed values/receipt")
	}
	if dir := os.Getenv("NYADB_RQLITE_SQL_EVIDENCE"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "workspace-file-original.json"), []byte(result.NativeJSON), 0600); err != nil {
			t.Fatal(err)
		}
		var picture bytes.Buffer
		if err := png.Encode(&picture, tt.Image()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "workspace-file.png"), picture.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s.DisconnectProfile(raw.ProfileID())
	if !d.NeedsRecovery() || d.RecoveryProfile() != raw.ProfileID() || d.RecoverySchema() != "main" || d.Editor.Buffer.String() != "SELECT 'unsaved editor';" {
		t.Fatal("file disconnect lost original profile or unsaved editor")
	}
}

func TestRQLiteNativeSQLWorkspaceCancelledLostFileRetainsReceiptUntilExplicitRecovery(t *testing.T) {
	raw, endpoint := rqliteSQLFixture(t)
	lease, err := raw.(contract.QueryProvider).ReserveQuery(502)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
	if _, err := lease.Execute(context.Background(), "main", "CREATE TABLE workspace_lost(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	started, acknowledge := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-acknowledge:
		default:
			close(acknowledge)
		}
	}()
	var writes atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		request, err := http.NewRequest(r.Method, endpoint+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			t.Error(err)
			return
		}
		request.Header = r.Header.Clone()
		request.GetBody = nil
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Error(err)
			return
		}
		defer response.Body.Close()
		original, err := io.ReadAll(response.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(string(body), "INSERT INTO workspace_lost") {
			writes.Add(1)
			close(started)
			<-acknowledge
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
		_, _ = w.Write(original)
	}))
	t.Cleanup(proxy.Close)
	proxied, err := rqlite.OpenSession(context.Background(), rqliteSQLProfile(t, proxy.URL), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxied.Close() })
	s, d := rqliteSQLState(t, proxied)
	s.SetPersistence(nil, Files{OpenPath: func() (string, error) { return "lost.sql", nil }, OpenReader: func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("INSERT INTO workspace_lost VALUES(1); SELECT count(*) FROM workspace_lost")), nil
	}}, nil)
	s.RunFile(d)
	awaitSignal(t, started)
	s.RequestCancel(d)
	s.Consume()
	if !d.Loading() || writes.Load() != 1 || d.Result() != nil {
		t.Fatal("native file cancellation settled before original executor")
	}
	close(acknowledge)
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if !d.NeedsRecovery() || d.Result() == nil || len(d.Result().Steps) != 2 || d.Result().Steps[0].State != "uncertain" || d.Result().Steps[1].State != "uncertain" {
		t.Fatal("lost native file receipt was discarded or settled", d.Error())
	}
	s.RunFile(d)
	s.Prepare(d)
	if d.Loading() || writes.Load() != 1 {
		t.Fatal("lost native file was automatically replayed")
	}
	s.Recover(d)
	consumeUntil(t, s, func() bool { return d.prepareResults == nil })
	if d.NeedsRecovery() || writes.Load() != 1 || d.Editor.Buffer.String() != "SELECT 'unsaved editor';" || d.ProfileID() != raw.ProfileID() || d.Database() != "main" {
		t.Fatal("explicit recovery replayed file or changed original identity", d.Error())
	}
	actual, err := lease.Execute(context.Background(), "main", "SELECT count(*) FROM workspace_lost")
	if err != nil || actual.Rows[0][0] != "1" {
		t.Fatal("original file write was lost or replayed", err)
	}
}
