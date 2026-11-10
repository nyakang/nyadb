package connectioneditor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func etcdEditorTester(s *State) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 520) }, 720, 520)
}
func chooseEtcd(t *testing.T, tt *ui.Tester, label, caption string) {
	t.Helper()
	if e := tt.Click(locales.Text(label)); e != nil {
		t.Fatal(e)
	}
	if e := tt.Click(locales.Text(caption)); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
}

func TestEtcdEditorOriginalCredentialsSaveAndAuthSwitch(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if e := s.OpenNew(connections.Etcd, ""); e != nil {
		t.Fatal(e)
	}
	tt := etcdEditorTester(s.State)
	setEngineOptionInput(t, tt, "连接名称", "native etcd")
	chooseEtcd(t, tt, "etcd 认证方式", "用户名和密码")
	setEngineOptionInput(t, tt, "用户名", " exact user ")
	setEngineOptionInput(t, tt, "密码", " 原始密码🐱\t ")
	if e := tt.Click("保存密码"); e != nil {
		t.Fatal(e)
	}
	if tt.HasText(" 原始密码🐱\t ") || s.Title() != "新建连接 (etcd)" {
		t.Fatal("native title lost or password visible")
	}
	if e := tt.Click("高级"); e != nil {
		t.Fatal(e)
	}
	setEngineOptionInput(t, tt, "etcd 连接超时", "12")
	s.Save()
	profiles, e := service.List(context.Background())
	if e != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal("native save failed", e, s.Error())
	}
	p := profiles[0]
	if p.Username != " exact user " || p.EtcdOptions.ConnectTimeoutSeconds != 12 || strings.Contains(fmt.Sprint(p), "原始密码") {
		t.Fatal("native identity narrowed or secret entered profile")
	}
	s.OpenProfile(p)
	if s.connectionSecret != " 原始密码🐱\t " || !s.connectionSavePassword {
		t.Fatal("original vault credential not restored")
	}
	tt.Frame()
	chooseEtcd(t, tt, "etcd 认证方式", "无认证")
	if s.connectionSecret != "" || s.connectionDraft.Username != "" || s.connectionSavePassword {
		t.Fatal("no-auth retained original credentials")
	}
	s.Cancel()
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != " 原始密码🐱\t " {
		t.Fatal("cancel changed original vault")
	}
	s.OpenProfile(p)
	tt.Frame()
	chooseEtcd(t, tt, "etcd 认证方式", "无认证")
	s.Save()
	if _, found, _ := vault.Get(p.ID, ""); found || s.Error() != "" {
		t.Fatal("no-auth save retained original secret", s.Error())
	}
	got, e := service.Get(context.Background(), p.ID)
	if e != nil || got.EtcdOptions.Auth != "none" || got.EtcdOptions.ConnectTimeoutSeconds != 12 {
		t.Fatal("saved native auth/routing changed", e)
	}
}

func TestEtcdEditorVaultFailureRollbackLockedAuthAndDeferredPassword(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.Etcd)
	p.Name = "original"
	p.EtcdOptions.Auth, p.Username = "basic", "exact"
	p, e := service.Create(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{fmt.Sprintf("%d/", p.ID): "original-password"}}
	s.SetCredentialVault(vault)
	s.OpenProfile(p)
	s.connectionDraft.EtcdOptions.Prefix = " /changed/ "
	s.connectionSecret, s.connectionSavePassword = "replacement-password", true
	vault.setErr = errors.New("locked")
	s.Save()
	got, e := service.Get(context.Background(), p.ID)
	if e != nil || got.EtcdOptions.Prefix != p.EtcdOptions.Prefix || s.Error() == "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-password" {
		t.Fatal("vault failure changed original profile/secret")
	}
	vault.setErr, vault.getErr = nil, errors.New("locked read")
	s.OpenProfile(p)
	s.setEtcdAuth("none")
	s.Save()
	got, e = service.Get(context.Background(), p.ID)
	if e != nil || got.EtcdOptions.Auth != "basic" || !strings.Contains(s.Error(), "解锁") {
		t.Fatal("locked credentials allowed auth change")
	}
	s.OpenProfile(p)
	s.connectionDraft.Host = "new-host.invalid"
	s.Save()
	got, e = service.Get(context.Background(), p.ID)
	if e != nil || got.Host != "new-host.invalid" || s.Error() != "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-password" {
		t.Fatal("ordinary edit erased unreadable secret")
	}
	vault.getErr = nil
	if e := s.OpenNew(connections.Etcd, ""); e != nil {
		t.Fatal(e)
	}
	s.connectionDraft.Name = "deferred password"
	s.Save()
	if profiles, e := service.List(context.Background()); e != nil || len(profiles) != 2 || s.Error() != "" {
		t.Fatal("empty draft password cannot be deferred", e, s.Error())
	}
	if e := s.OpenNew(connections.Etcd, ""); e != nil {
		t.Fatal(e)
	}
	s.connectionDraft.Name = "vault write failure"
	s.setEtcdAuth("basic")
	s.connectionDraft.Username = "exact"
	s.connectionSecret, s.connectionSavePassword = "new secret", true
	vault.setErr = errors.New("unavailable")
	s.Save()
	if profiles, e := service.List(context.Background()); e != nil || len(profiles) != 2 || s.Error() == "" {
		t.Fatal("new profile survived credential save failure")
	}
}

func TestEtcdEditorCapturedCheckAuthCancelAndPrivateFiles(t *testing.T) {
	started, canceled, ack := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finished := make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		defer close(finished)
		if p.EtcdOptions.Auth != "basic" || p.Username != " exact " || secret != "captured-password" {
			return errors.New("captured values changed")
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		<-ack
		return errors.New("late captured-password")
	}})
	t.Cleanup(s.Close)
	if e := s.OpenNew(connections.Etcd, ""); e != nil {
		t.Fatal(e)
	}
	s.connectionDraft.Name = "captured"
	s.setEtcdAuth("basic")
	s.connectionDraft.Username = " exact "
	s.connectionSecret = "captured-password"
	for _, uri := range []string{"etcd://user:private@localhost?auth=none", "etcd://localhost/db"} {
		if e := s.ImportURI(uri); e == nil || s.connectionSecret != "captured-password" || s.connectionDraft.Name != "captured" {
			t.Fatal("invalid import changed original draft")
		}
	}
	tt := etcdEditorTester(s)
	if e := tt.Click("测试连接"); e != nil {
		t.Fatal(e)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("original check did not start")
	}
	setEngineOptionInput(t, tt, "etcd 键前缀", " /changed/ ")
	chooseEtcd(t, tt, "etcd 认证方式", "无认证")
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("auth change did not cancel original check")
	}
	close(ack)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("original check did not acknowledge")
	}
	tt.Frame()
	if s.connectionTesting || s.connectionTestResults != nil || s.Error() != "" || s.connectionSecret != "" {
		t.Fatal("late check entered changed draft")
	}
	var picked EngineFile
	var version uint64
	s.SetEngineFilePicker(func(k EngineFile, _ int, v uint64) { picked, version = k, v })
	if e := tt.Click("SSL"); e != nil {
		t.Fatal(e)
	}
	chooseEtcd(t, tt, "etcd TLS 模式", "验证服务器证书")
	if e := tt.Click(locales.Text("选择 etcd CA 证书")); e != nil {
		t.Fatal(e)
	}
	if picked != EtcdCAFile {
		t.Fatal("wrong native file request")
	}
	s.SelectEngineFile(picked, -1, version, "/owned/ca.crt")
	if s.connectionDraft.EtcdOptions.CAFile != "/owned/ca.crt" {
		t.Fatal("current file completion ignored")
	}
	chooseEtcd(t, tt, "etcd TLS 模式", "禁用 TLS")
	s.SelectEngineFile(picked, -1, version, "/stale/ca.crt")
	if s.connectionDraft.EtcdOptions.CAFile != "/owned/ca.crt" {
		t.Fatal("old file completion entered changed TLS draft")
	}
	s.Close()
	s.SelectEngineFile(EtcdKeyFile, -1, s.EngineFileVersion(), "/stale/key")
	if s.connectionDraft.EtcdOptions.ClientKeyFile != "" {
		t.Fatal("closed editor accepted file completion")
	}
}

func TestEtcdEditorMissingPasswordPromptsBeforeNativeCheck(t *testing.T) {
	called := false
	s := New(Dependencies{TestConnection: func(context.Context, connections.Profile, string) error { called = true; return nil }})
	t.Cleanup(s.Close)
	if e := s.OpenNew(connections.Etcd, ""); e != nil {
		t.Fatal(e)
	}
	s.connectionDraft.Name = "deferred secret"
	s.setEtcdAuth("basic")
	s.connectionDraft.Username = "exact"
	tt := etcdEditorTester(s)
	if e := tt.Click("测试连接"); e != nil {
		t.Fatal(e)
	}
	tt.Frame()
	if called || s.connectionTesting || s.validationErrorField != "密码" || !strings.Contains(s.Error(), "请输入") {
		t.Fatal("missing password dispatched a handshake or did not prompt")
	}
}

func TestEtcdEditorPrefixRepresentationAndCertificateIdentity(t *testing.T) {
	s := New(Dependencies{})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.Etcd, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "prefix"
	s.connectionDraft.EtcdOptions.Prefix = " /Case/键 "
	original := s.connectionDraft.EtcdOptions.Prefix
	s.setEtcdPrefixEncoding("base64")
	s.setEtcdPrefixEncoding("text")
	if s.connectionDraft.EtcdOptions.Prefix != original {
		t.Fatal("encoding switch changed bytes")
	}
	s.connectionDraft.EtcdOptions.PrefixEncoding = "base64"
	s.connectionDraft.EtcdOptions.Prefix = "AP8="
	s.setEtcdPrefixEncoding("text")
	if s.connectionDraft.EtcdOptions.PrefixEncoding != "base64" || s.Error() == "" {
		t.Fatal("binary prefix silently decoded into invalid text")
	}
	s.connectionDraft.EtcdOptions.Prefix = ""
	s.setEtcdAuth("certificate")
	s.connectionDraft.TLSMode = "verify-full"
	s.connectionDraft.EtcdOptions.ClientCertFile = "/client.crt"
	s.connectionDraft.EtcdOptions.ClientKeyFile = "/client.key"
	if _, err := s.draftProfile(); err != nil {
		t.Fatal("certificate draft", err)
	}
	version := s.EngineFileVersion()
	s.setEtcdAuth("none")
	s.SelectEngineFile(EtcdCertificateFile, -1, version, "/stale/client.crt")
	if s.connectionDraft.EtcdOptions.ClientCertFile != "" || s.connectionDraft.EtcdOptions.ClientKeyFile != "" {
		t.Fatal("old certificate entered changed identity")
	}
	if SQLiteAttachedFile != 0 || Neo4jKeyFile != 13 || EtcdCAFile != 14 {
		t.Fatal("existing native picker identities changed")
	}
}
