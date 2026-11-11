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

func consulEditorTester(s *State) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 520) }, 720, 520)
}
func chooseConsul(t *testing.T, tt *ui.Tester, label, caption string) {
	t.Helper()
	if err := tt.Click(locales.Text(label)); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(locales.Text(caption)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}
func TestConsulEditorOriginalTokenSaveAuthSwitchAndScope(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Consul, ""); err != nil {
		t.Fatal(err)
	}
	tt := consulEditorTester(s.State)
	setEngineOptionInput(t, tt, "连接名称", "original Consul")
	setEngineOptionInput(t, tt, "Consul 数据中心", "dc1")
	setEngineOptionInput(t, tt, "Consul 键前缀", " /original//../ ")
	chooseConsul(t, tt, "Consul 读取一致性", "一致读取")
	chooseConsul(t, tt, "Consul 认证方式", "Token")
	setEngineOptionInput(t, tt, "Consul token", "original-token")
	if err := tt.Click("保存凭据"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("original-token") || s.Title() != "新建连接 (Consul)" {
		t.Fatal("original title changed or token exposed")
	}
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "Consul 连接超时", "12")
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal("original Consul save failed", err, s.Error())
	}
	p := profiles[0]
	if p.Username != "" || p.Database != "" || p.ConsulOptions.Datacenter != "dc1" || p.ConsulOptions.Prefix != " /original//../ " || p.ConsulOptions.Consistency != "consistent" || p.ConsulOptions.ConnectTimeoutSeconds != 12 || strings.Contains(fmt.Sprint(p), "original-token") {
		t.Fatal("original scope changed or token entered profile")
	}
	s.OpenProfile(p)
	if s.connectionSecret != "original-token" || !s.connectionSavePassword {
		t.Fatal("original token was not restored from vault")
	}
	tt.Frame()
	chooseConsul(t, tt, "Consul 认证方式", "无认证")
	if s.connectionSecret != "" || s.connectionSavePassword {
		t.Fatal("no-auth retained original token")
	}
	s.Cancel()
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-token" {
		t.Fatal("cancel changed original vault")
	}
	s.OpenProfile(p)
	tt.Frame()
	chooseConsul(t, tt, "Consul 认证方式", "无认证")
	s.Save()
	if _, found, _ := vault.Get(p.ID, ""); found || s.Error() != "" {
		t.Fatal("explicit no-auth save retained original token", s.Error())
	}
}

func TestConsulEditorVaultFailureRollsBackAndLockedAuthCannotEraseToken(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.Consul)
	p.Name, p.ConsulOptions.Auth = "original", "token"
	p, err := service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{fmt.Sprintf("%d/", p.ID): "original-token"}}
	s.SetCredentialVault(vault)
	s.OpenProfile(p)
	s.connectionDraft.ConsulOptions.Prefix = "foreign/"
	s.connectionSecret, s.connectionSavePassword = "replacement-token", true
	vault.setErr = errors.New("locked")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.ConsulOptions != p.ConsulOptions || s.Error() == "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-token" {
		t.Fatal("failed vault save altered original configuration")
	}
	vault.setErr, vault.getErr = nil, errors.New("locked read")
	s.OpenProfile(p)
	s.setConsulAuth("none")
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.ConsulOptions.Auth != "token" || !strings.Contains(s.Error(), "解锁") {
		t.Fatal("locked original token permitted auth switch")
	}
	s.OpenProfile(p)
	s.connectionDraft.Host = "changed.invalid"
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.Host != "changed.invalid" || s.Error() != "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-token" {
		t.Fatal("ordinary host edit erased unreadable token")
	}
}

func TestConsulEditorCapturedCheckCancellationAndIndependentMTLSFiles(t *testing.T) {
	started, canceled, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, token string) error {
		defer close(finished)
		if p.ConsulOptions.Auth != "token" || token != "original-token" || p.ConsulOptions.Datacenter != "dc1" {
			return errors.New("original captured configuration changed")
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return errors.New("late original-token")
	}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.Consul, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name, s.connectionDraft.ConsulOptions.Datacenter = "original", "dc1"
	s.setConsulAuth("token")
	s.connectionSecret = "original-token"
	tt := consulEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("original check did not start")
	}
	chooseConsul(t, tt, "Consul 认证方式", "无认证")
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("auth switch did not cancel original check")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("original check did not acknowledge cancellation")
	}
	tt.Frame()
	if s.connectionTesting || s.Error() != "" || s.connectionSecret != "" {
		t.Fatal("late original check entered changed draft")
	}
	var picked EngineFile
	var version uint64
	s.SetEngineFilePicker(func(k EngineFile, _ int, v uint64) { picked, version = k, v })
	if err := tt.Click("SSL"); err != nil {
		t.Fatal(err)
	}
	chooseConsul(t, tt, "Consul TLS 模式", "验证服务器证书")
	if err := tt.Click(locales.Text("选择 Consul 客户端证书")); err != nil {
		t.Fatal(err)
	}
	if picked != ConsulCertificateFile {
		t.Fatal("no-token mTLS could not select client certificate")
	}
	s.SelectEngineFile(picked, -1, version, "/owned/client.crt")
	if s.connectionDraft.ConsulOptions.ClientCertFile != "/owned/client.crt" {
		t.Fatal("current certificate completion ignored")
	}
	chooseConsul(t, tt, "Consul TLS 模式", "禁用 TLS")
	s.SelectEngineFile(picked, -1, version, "/stale/client.crt")
	if s.connectionDraft.ConsulOptions.ClientCertFile != "/owned/client.crt" {
		t.Fatal("old file completion entered changed TLS draft")
	}
}

func TestConsulEditorMissingTokenStopsBeforeProviderAndURIErrorKeepsDraft(t *testing.T) {
	called := false
	s := New(Dependencies{TestConnection: func(context.Context, connections.Profile, string) error { called = true; return nil }})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.Consul, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "original"
	s.setConsulAuth("token")
	tt := consulEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	if called || s.Error() == "" {
		t.Fatal("missing original token reached provider")
	}
	s.connectionSecret = "original-token"
	for _, uri := range []string{"consul://:private@localhost?auth=none", "consul://localhost/db"} {
		if err := s.ImportURI(uri); err == nil || s.connectionSecret != "original-token" || s.connectionDraft.Name != "original" {
			t.Fatal("invalid URI altered original draft")
		}
	}
}
