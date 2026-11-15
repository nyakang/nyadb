package shell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/rqlite"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func rqlitePublicFixture(t *testing.T, auth string, foreignKeys ...bool) string {
	t.Helper()
	binary, version := os.Getenv("NYADB_RQLITE_BINARY"), os.Getenv("NYADB_RQLITE_VERSION")
	artifacts := map[string]string{"10.7.1": "37b0be99b2e1d3ca2950c5d2d7432c444be8f3d7526331b2b85b205b8ee6933c", "8.43.4": "3b6bd8ad0497777bdece2919b0dc0b7702b6121c0b7db8ebb49f664e59087dff"}
	bytes, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal("official public fixture artifact unavailable", err)
	}
	digest := sha256.Sum256(bytes)
	if artifacts[version] == "" || hex.EncodeToString(digest[:]) != artifacts[version] {
		t.Fatal("public fixture requires the pinned official version artifact")
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
	args := []string{"-node-id", filepath.Base(dir), "-http-addr", httpAddr, "-raft-addr", raftAddr}
	if len(foreignKeys) == 0 || foreignKeys[0] {
		args = append(args, "-fk")
	}
	u := &url.URL{Scheme: "rqlite", Host: httpAddr}
	if auth != "none" {
		password := "original:password"
		users := []map[string]any{{"username": " reader ", "password": password, "perms": []string{"all"}}}
		if auth == "roles" {
			users = append(users, map[string]any{"username": "query-only", "password": "query-only-password", "perms": []string{"query"}})
		}
		data, err := json.Marshal(users)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "auth.json")
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-auth", path)
		u.User = url.UserPassword(" reader ", password)
	}
	args = append(args, filepath.Join(dir, "data"))
	logPath := filepath.Join(dir, "server.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	t.Logf("official public RQLite version=%s artifact=%s original_process=%d auth=%s", version, artifacts[version], cmd.Process.Pid, auth)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("original public fixture process did not exit")
		}
		_ = log.Close()
		if output := os.Getenv("NYADB_RQLITE_SHELL_EVIDENCE"); output != "" {
			if err := os.MkdirAll(output, 0700); err != nil {
				t.Error(err)
				return
			}
			data, err := os.ReadFile(logPath)
			if err == nil {
				err = os.WriteFile(filepath.Join(output, fmt.Sprintf("server-%d.log", cmd.Process.Pid)), data, 0600)
			}
			if err != nil {
				t.Error(err)
			}
		}
	})
	parsed, err := connections.ParseURI(u.String())
	if err != nil {
		t.Fatal(err)
	}
	parsed.Profile.ID = 991
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case err := <-done:
			done <- err
			t.Fatal("original process exited before readiness", err)
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		s, err := rqlite.OpenSession(ctx, parsed.Profile, parsed.Password)
		cancel()
		if err == nil {
			_ = s.Close()
			return u.String()
		}
		if time.Now().After(deadline) {
			t.Fatal("original native public fixture did not become ready", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRQLiteNativePublicChooserURIVaultSQLGridStructureAndExplicitRecovery(t *testing.T) {
	if os.Getenv("NYADB_RQLITE_SHELL_IT") != "1" {
		t.Skip("requires the owned pinned native RQLite public workbench fixture")
	}
	for _, auth := range []string{"none", "basic"} {
		t.Run(auth, func(t *testing.T) {
			uri := rqlitePublicFixture(t, auth)
			if output := os.Getenv("NYADB_RQLITE_SHELL_EVIDENCE"); output != "" {
				if err := os.MkdirAll(output, 0700); err != nil {
					t.Fatal(err)
				}
			}
			s, _, profiles, _ := profileTestState(t)
			t.Cleanup(s.CloseMySQLSessions)
			vault := &memoryCredentialVault{secrets: map[string]string{}}
			s.SetCredentialVault(vault)
			s.SetConnectionWindowHost(func() {}, func() {})
			s.openConnectionDialog()
			ct := ui.NewTester(s.ConnectionWindowView, 920, 850)
			input := func(tt *ui.Tester, label, value string) {
				t.Helper()
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
				tt.Command("selectAll")
				tt.Type(value)
				tt.Frame()
			}
			wait := func(tt *ui.Tester, done func() bool) {
				t.Helper()
				deadline := time.Now().Add(30 * time.Second)
				for !done() && time.Now().Before(deadline) {
					tt.Frame()
					time.Sleep(time.Millisecond)
				}
				tt.Frame()
				if !done() {
					t.Fatal("original public workbench request did not settle", s.sessions.Current().Error)
				}
			}
			input(ct, "搜索连接类型", "RQLite")
			if err := ct.Click("选择 RQLite"); err != nil {
				t.Fatal(err)
			}
			if err := ct.Click("下一步"); err != nil {
				t.Fatal(err)
			}
			if s.connectionEditor.Draft().Kind != connections.RQLite {
				t.Fatal("public chooser did not open dedicated RQLite editor")
			}
			if err := s.connectionEditor.ImportURI(uri); err != nil {
				t.Fatal(err)
			}
			input(ct, "连接名称", "public RQLite "+auth)
			if auth != "none" {
				if err := ct.Click("保存密码"); err != nil {
					t.Fatal(err)
				}
			}
			if output := os.Getenv("NYADB_RQLITE_SHELL_EVIDENCE"); output != "" {
				writeDomainCapture(t, output, auth+"-public-editor-general", ct)
				if err := ct.Click("高级"); err != nil {
					t.Fatal(err)
				}
				writeDomainCapture(t, output, auth+"-public-editor-advanced", ct)
				if err := ct.Click("常规"); err != nil {
					t.Fatal(err)
				}
			}
			if err := ct.Click("测试连接"); err != nil {
				t.Fatal(err)
			}
			wait(ct, func() bool { return !s.connectionEditor.Testing() })
			if s.connectionEditor.Error() != "" {
				t.Fatal("public native connection check failed", s.connectionEditor.Error())
			}
			if err := ct.Click("确定"); err != nil {
				t.Fatal(err)
			}
			list, err := profiles.List(context.Background())
			if err != nil || len(list) != 1 {
				t.Fatal("public native configuration was not saved", err)
			}
			p := list[0]
			parsed, _ := connections.ParseURI(uri)
			secret, found, err := vault.Get(p.ID, "")
			if err != nil || auth != "none" && (!found || secret != parsed.Password || p.Username != " reader ") || auth == "none" && found {
				t.Fatal("public vault lost exact original Basic credential", err)
			}
			var services ui.Services
			tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 900)
			s.selectProfileByName(p.Name)
			tt.Frame()
			if err = tt.Click("连接所选数据库"); err != nil {
				t.Fatal(err)
			}
			wait(tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
			if !s.runtime.IsConnected(p.ID) || s.sessions.Current().Kind != connections.RQLite {
				t.Fatal("builtin RQLite failed to publish original session", s.sessions.Current().Error)
			}
			session, err := s.runtime.Session(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range []string{"用户", "备份"} {
				if s.toolbarActionAvailable(action) {
					t.Fatal("unimplemented administration exposed", action)
				}
			}
			query, err := contract.Require[contract.QueryProvider](session)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := query.ReserveQuery(991)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(lease.Release)
			if _, err = lease.Execute(context.Background(), "main", "CREATE TABLE public_records(id INTEGER PRIMARY KEY,note TEXT);INSERT INTO public_records VALUES(9007199254740993,'original')"); err != nil {
				t.Fatal(err)
			}
			generation := session.Generation()
			if err = s.runtime.Test(context.Background(), p, secret); err != nil || s.runtime.Generation(p.ID) != generation {
				t.Fatal("public connection check replaced active original session", err)
			}
			s.sessions.RefreshDatabases(services.Invalidate)
			wait(tt, func() bool { return !s.sessions.Current().Refreshing() })
			s.openMySQLDatabase(services, "main", true)
			wait(tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["main"] != nil })
			s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: "main", Object: "public_records", Category: "表"})
			data := s.currentTab()
			if data.data == nil || data.data.Object().Catalog != "main" || data.data.Object().ProfileID != p.ID {
				t.Fatal("public navigator did not open qualified shared grid")
			}
			wait(tt, func() bool { return !data.data.Busy() })
			if data.data.Error() != "" || data.data.Page() == nil || data.data.Page().Rows[0][0].Text != "9007199254740993" {
				t.Fatal("public native grid lost exact integer", data.data.Error())
			}
			s.dataWorkspace.EditCell(services, data.data, 0, "note", contract.Cell{Kind: contract.Text, Text: "saved via public RQLite"})
			tt.Frame()
			if err = tt.Click("保存数据更改"); err != nil {
				t.Fatal(err)
			}
			wait(tt, func() bool { return !data.data.Busy() })
			if data.data.Error() != "" || data.data.Page().Rows[0][1].Text != "saved via public RQLite" {
				t.Fatal("public native data save failed", data.data.Error())
			}
			if output := os.Getenv("NYADB_RQLITE_SHELL_EVIDENCE"); output != "" {
				writeDomainCapture(t, output, auth+"-public-grid", tt)
			}
			s.openMySQLStructure(services, "main", "public_records")
			tt.Frame()
			structure := s.currentTab()
			if structure.object == nil {
				t.Fatal("public native structure document missing")
			}
			wait(tt, func() bool { return structure.object.Data() != nil })
			if !strings.Contains(structure.object.Data().Definition, "CREATE TABLE") {
				t.Fatal("original complete native definition missing")
			}
			s.openQuery()
			sql := s.currentTab()
			if sql.query == nil || sql.query.ProfileID() != p.ID || sql.query.Database() != "main" {
				t.Fatal("public RQLite did not open original SQL workspace")
			}
			statement := "SELECT id,note FROM public_records"
			sql.query.Editor.Buffer.Set(statement)
			sql.query.Editor.Select(0, len(statement))
			tt.Frame()
			if err = tt.Click("运行当前或选中 SQL"); err != nil {
				t.Fatal(err)
			}
			wait(tt, func() bool { return !sql.query.Loading() })
			result := sql.query.Result()
			if result == nil || result.Sets[0].Rows[0][0].Text != "9007199254740993" || result.Sets[0].Rows[0][1].Text != "saved via public RQLite" || s.sqlCompletionCatalog(p.ID, "main").Dialect != "sqlite" {
				t.Fatal("public SQL result or dialect changed", sql.query.Error())
			}
			if output := os.Getenv("NYADB_RQLITE_SHELL_EVIDENCE"); output != "" {
				writeDomainCapture(t, output, auth+"-public-sql", tt)
			}
			lease.Release()
			s.disconnectMySQLProfile(p.ID)
			tt.Frame()
			if !sql.query.NeedsRecovery() || sql.query.Editor.Buffer.String() != statement || s.runtime.IsConnected(p.ID) {
				t.Fatal("disconnect lost original SQL text or retained runtime")
			}
			for _, tab := range s.tabs {
				if tab.profileID() == p.ID && (tab.data != nil || tab.object != nil) {
					t.Fatal("disconnect retained scoped native documents")
				}
			}
			s.sessions.Connect(p, services.Invalidate)
			wait(tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
			if !s.runtime.IsConnected(p.ID) || s.runtime.Generation(p.ID) == generation {
				t.Fatal("explicit recovery did not publish new original generation")
			}
			if err = tt.Click("建立新物理会话（不执行现有 SQL）"); err != nil {
				t.Fatal(err)
			}
			wait(tt, func() bool { return !sql.query.NeedsRecovery() && !sql.query.Loading() })
			if sql.query.Result() != nil || sql.query.Editor.Buffer.String() != statement {
				t.Fatal("recovery replayed original SQL")
			}
		})
	}
}

func TestRQLiteNativePublicEmptyBasicRejectedWithoutPublishingSession(t *testing.T) {
	if os.Getenv("NYADB_RQLITE_SHELL_IT") != "1" {
		t.Skip("requires the owned pinned native RQLite public workbench fixture")
	}
	uri := rqlitePublicFixture(t, "basic")
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(" reader ", "")
	s, _, _, _ := profileTestState(t)
	t.Cleanup(s.CloseMySQLSessions)
	if err = s.connectionEditor.ImportURI(u.String()); err != nil {
		t.Fatal("valid request parameters rejected before native authentication", err)
	}
	tt := ui.NewTester(func(c *ui.Context) { s.connectionEditor.View(c, 920, 850) }, 920, 850)
	tt.Frame()
	if err = tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for s.connectionEditor.Testing() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if s.connectionEditor.Testing() || s.connectionEditor.Error() == "" || s.runtime.IsConnected(s.connectionEditor.Draft().ID) {
		t.Fatal("native rejected credential was published or check failed to settle")
	}
	if err = s.connectionEditor.ImportURI(uri); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err = tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for s.connectionEditor.Testing() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if s.connectionEditor.Testing() || s.connectionEditor.Error() != "" {
		t.Fatal("explicit valid credential check did not recover", s.connectionEditor.Error())
	}
}

func TestRQLiteNativePublicReadOnlyKeepsMetadataGridAndRejectsWrites(t *testing.T) {
	if os.Getenv("NYADB_RQLITE_SHELL_IT") != "1" {
		t.Skip("requires the owned pinned native RQLite public workbench fixture")
	}
	uri := rqlitePublicFixture(t, "none")
	s, _, profiles, _ := profileTestState(t)
	t.Cleanup(s.CloseMySQLSessions)
	parsed, err := connections.ParseURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	writerProfile := parsed.Profile
	writerProfile.ID = 997
	writer, err := s.runtime.ConnectSession(context.Background(), writerProfile, "")
	if err != nil {
		t.Fatal(err)
	}
	query, err := contract.Require[contract.QueryProvider](writer)
	if err != nil {
		t.Fatal(err)
	}
	wl, err := query.ReserveQuery(997)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wl.Execute(context.Background(), "main", "CREATE TABLE public_readonly(id INTEGER PRIMARY KEY,note TEXT);INSERT INTO public_readonly VALUES(1,'original')"); err != nil {
		t.Fatal(err)
	}
	wl.Release()
	if err = s.runtime.Disconnect(writerProfile.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.connectionEditor.ImportURI(uri + "?read_only=true"); err != nil {
		t.Fatal(err)
	}
	s.connectionEditor.Save()
	list, err := profiles.List(context.Background())
	if err != nil || len(list) != 1 || !list[0].RQLiteOptions.ReadOnly {
		t.Fatal("public read-only option not saved", err, s.connectionEditor.Error())
	}
	p := list[0]
	s.selectProfileByName(p.Name)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 900)
	wait := func(done func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !done() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(time.Millisecond)
		}
		tt.Frame()
		if !done() {
			t.Fatal("original public read-only request did not settle", s.sessions.Current().Error)
		}
	}
	tt.Frame()
	if err = tt.Click("连接所选数据库"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	public, err := s.runtime.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	caps := public.Capabilities()
	if caps.Import || caps.EditableTables || caps.DesignTables || caps.Objects || !caps.Query || !caps.Metadata || !caps.Export || !caps.Count {
		t.Fatal("public read-only capability gates changed")
	}
	if s.objectWorkspace.CanCreateTable(p.ID, "main") || s.objectWorkspace.CanDropTable(p.ID, "main", "public_readonly") || s.objectWorkspace.CanRenameTable(p.ID, "main", "public_readonly") || s.objectWorkspace.CanCopyTable(p.ID, "main", "public_readonly") {
		t.Fatal("public read-only session advertised schema writes")
	}
	schemaProvider, err := s.runtime.SchemaDesignDocuments(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	schemaLease, err := schemaProvider.ReserveSchemaDesignDocument(contract.SchemaDesignDocumentRequest{DocumentID: "readonly-schema", Target: contract.SchemaDesignTarget{Object: s.runtime.Namespace(p.ID, "main").Object("table", "public_readonly"), Generation: public.Generation()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = schemaLease.Close() })
	if ops := schemaLease.Operations(); ops.Snapshot == nil || ops.Plan != nil || ops.Apply != nil || ops.Drop != nil || ops.Rename != nil || ops.Copy != nil || schemaLease.OriginalOutcome() != nil {
		t.Fatal("public read-only schema lease exposed write methods or synthetic receipts")
	}
	s.openMySQLDatabase(services, "main", true)
	wait(func() bool { return s.sessions.Current().Cache["main"] != nil })
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: "main", Object: "public_readonly", Category: "表"})
	d := s.currentTab().data
	if d == nil {
		t.Fatal("public read-only grid did not open")
	}
	wait(func() bool { return !d.Busy() })
	if d.Error() != "" || d.Page() == nil || d.Page().Rows[0][1].Text != "original" {
		t.Fatal("read-only original metadata/grid unavailable", d.Error())
	}
	s.dataWorkspace.EditCell(services, d, 0, "note", contract.Cell{Kind: contract.Text, Text: "must not write"})
	if d.HasDraft() {
		t.Fatal("public read-only grid admitted draft")
	}
	dl, err := s.runtime.Data(p.ID).ReserveDocument(d.Object())
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Close()
	if r := dl.Runtime(); r.StreamImporter != nil || r.Saver != nil {
		t.Fatal("read-only original document published write tools")
	}
	q, err := contract.Require[contract.QueryProvider](public)
	if err != nil {
		t.Fatal(err)
	}
	l, err := q.ReserveQuery(998)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if _, err = l.Execute(context.Background(), "main", "UPDATE public_readonly SET note='must not write'"); err == nil {
		t.Fatal("read-only SQL admitted write")
	}
	out, err := l.Execute(context.Background(), "main", "SELECT note FROM public_readonly")
	if err != nil || out.Rows[0][0] != "original" {
		t.Fatal("rejected write changed native data", err)
	}
}
