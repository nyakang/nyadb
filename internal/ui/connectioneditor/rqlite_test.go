package connectioneditor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func rqliteEditorTester(s *State) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 900, 850) }, 900, 850)
}

func TestRQLiteEditorPublicURISavesExactAuthAndAllOptions(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.ImportURI("rqlite://%20reader%20:original%3Apassword@localhost:4002/proxy/rqlite?consistency=none&freshness_milliseconds=250&freshness_strict=true&batch_transaction=true&connect_timeout_seconds=12&request_timeout_seconds=120&forward_timeout_seconds=35&database_timeout_milliseconds=40000&linearizable_timeout_milliseconds=20000&max_requests=12&sql_max_connections=7"); err != nil {
		t.Fatal(err)
	}
	tt := rqliteEditorTester(s.State)
	setEngineOptionInput(t, tt, "连接名称", "original RQLite")
	if tt.HasText("original:password") {
		t.Fatal("URI credential exposed")
	}
	if err := tt.Click("保存密码"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "RQLite 请求并发上限", "14")
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal("public RQLite save failed", err, s.Error())
	}
	p := profiles[0]
	o := p.RQLiteOptions
	if p.Username != " reader " || p.Database != "main" || p.Port != 4002 || o.BasePath != "/proxy/rqlite" || o.Auth != "basic" || o.Consistency != "none" || o.FreshnessMilliseconds != 250 || !o.FreshnessStrict || !o.BatchTransaction || o.ConnectTimeoutSeconds != 12 || o.RequestTimeoutSeconds != 120 || o.ForwardTimeoutSeconds != 35 || o.DatabaseTimeoutMilliseconds != 40000 || o.LinearizableTimeoutMilliseconds != 20000 || o.MaxRequests != 14 || o.SQLMaxConnections != 7 {
		t.Fatal("public editor changed exact original settings", o)
	}
	s.OpenProfile(p)
	if s.connectionSecret != "original:password" || !s.connectionSavePassword {
		t.Fatal("original Basic credential not restored")
	}
	tt.Frame()
	chooseConsul(t, tt, "RQLite 认证方式", "无认证")
	if s.connectionSecret != "" || s.connectionDraft.Username != "" || s.connectionSavePassword {
		t.Fatal("no-auth retained original Basic credential")
	}
	s.Cancel()
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original:password" {
		t.Fatal("cancel changed original vault")
	}
	s.OpenProfile(p)
	s.setRQLiteAuth("none")
	s.Save()
	if _, found, err := vault.Get(p.ID, ""); err != nil || found || s.Error() != "" {
		t.Fatal("explicit no-auth save retained credential", err, s.Error())
	}
}

func TestRQLiteEditorLockedAuthAndVaultFailurePreserveOriginalProfile(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.RQLite)
	p.Name, p.Username, p.RQLiteOptions.Auth = "original", "reader", "basic"
	p, err := service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{fmt.Sprintf("%d/", p.ID): "original"}}
	s.SetCredentialVault(vault)
	s.OpenProfile(p)
	s.connectionDraft.Host, s.connectionSecret, s.connectionSavePassword = "changed.invalid", "changed", true
	vault.setErr = errors.New("locked")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.Host != p.Host || s.Error() == "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original" {
		t.Fatal("vault failure changed original profile", err)
	}
	vault.setErr, vault.getErr = nil, errors.New("locked read")
	s.OpenProfile(p)
	s.setRQLiteAuth("none")
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.RQLiteOptions.Auth != "basic" || s.Error() == "" {
		t.Fatal("locked original vault permitted auth change", err)
	}
	s.OpenProfile(p)
	s.connectionDraft.Host = "changed.invalid"
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.Host != "changed.invalid" || s.Error() != "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original" {
		t.Fatal("ordinary edit erased original unreadable credential", err, s.Error())
	}
}

func TestRQLiteEditorRejectsIncompatibleRawOptionsAndKeepsDraftOnURIError(t *testing.T) {
	called := false
	s := New(Dependencies{TestConnection: func(context.Context, connections.Profile, string) error { called = true; return nil }})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.RQLite, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "original"
	for _, change := range []func(){func() { s.rqliteNumbers[7] = "33" }, func() { s.rqliteNumbers[2] = "30" }, func() { s.rqliteNumbers[0] = "100" }, func() { s.rqliteNumbers[4] = "raw" }} {
		s.initializeRQLiteInputs()
		change()
		if _, err := s.draftProfile(); err == nil {
			t.Fatal("incompatible raw settings accepted")
		}
	}
	s.initializeRQLiteInputs()
	s.setRQLiteAuth("basic")
	if _, err := s.draftProfile(); err == nil || called {
		t.Fatal("missing username reached provider")
	}
	s.connectionDraft.Username, s.connectionSecret = " reader ", "original"
	before := s.connectionDraft
	for _, uri := range []string{"rqlite://reader:private@localhost?queue", "rqlite://localhost?retries=1"} {
		if err := s.ImportURI(uri); err == nil || !reflect.DeepEqual(before, s.connectionDraft) || s.connectionSecret != "original" {
			t.Fatal("invalid URI changed original draft")
		}
	}
	// An empty Basic password is valid and must reach the original server.
	s.connectionSecret = ""
	if _, err := s.draftProfile(); err != nil {
		t.Fatal("valid empty Basic password rejected", err)
	}
}

func TestRQLiteEditorAuthCancelsOriginalCheckAndTLSFilesRevokeLatePicker(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		if p.Username != " reader " || secret != "original" || p.RQLiteOptions.BasePath != "/proxy" {
			return errors.New("original check values changed")
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}})
	t.Cleanup(s.Close)
	if err := s.ImportURI("rqlite://%20reader%20:original@localhost/proxy"); err != nil {
		t.Fatal(err)
	}
	tt := rqliteEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("original check did not start")
	}
	chooseConsul(t, tt, "RQLite 认证方式", "无认证")
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("auth change failed to cancel original check")
	}
	tt.Frame()
	if s.connectionTesting || s.connectionSecret != "" {
		t.Fatal("late original check entered changed draft")
	}
	s.setRQLiteTLS("verify-full")
	v := s.EngineFileVersion()
	s.SelectEngineFile(RQLiteCAFile, -1, v, "/owned/ca.pem")
	s.SelectEngineFile(RQLiteCertificateFile, -1, v, "/owned/client.pem")
	s.SelectEngineFile(RQLiteKeyFile, -1, v, "/owned/client.key")
	if s.connectionDraft.RQLiteOptions.CAFile != "/owned/ca.pem" || s.connectionDraft.RQLiteOptions.ClientKeyFile != "/owned/client.key" {
		t.Fatal("original TLS picker completion missing")
	}
	s.setRQLiteTLS("require")
	s.SelectEngineFile(RQLiteCAFile, -1, v, "/stale/ca.pem")
	if s.connectionDraft.RQLiteOptions.CAFile != "" || s.connectionDraft.RQLiteOptions.ClientCertFile != "/owned/client.pem" {
		t.Fatal("TLS mode admitted stale picker or erased independent client identity")
	}
	s.setRQLiteTLS("disable")
	s.SelectEngineFile(RQLiteKeyFile, -1, v, "/stale/client.key")
	if s.connectionDraft.RQLiteOptions.ClientKeyFile != "" {
		t.Fatal("disabled TLS admitted original late picker")
	}
}
