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
)

func easysearchEditorTester(s *State) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 520) }, 720, 520)
}

func chooseEasysearchAuth(t *testing.T, tt *ui.Tester, caption string) {
	t.Helper()
	if err := tt.Click("Easysearch 认证方式"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(caption); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}

func TestEasysearchEditorAuthSelectionMaskedFieldsAndVaultLifecycle(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Easysearch, ""); err != nil {
		t.Fatal(err)
	}
	tt := easysearchEditorTester(s.State)
	setEngineOptionInput(t, tt, "连接名称", "native Easysearch")
	setEngineOptionInput(t, tt, "密码", "private-password")
	if tt.HasText("private-password") || s.Title() != "新建连接 (Easysearch)" {
		t.Fatal("password rendered plainly or title lost identity")
	}
	if _, ok := tt.Find("初始数据库"); ok {
		t.Fatal("REST profile exposed SQL database field")
	}
	if err := tt.Click("保存凭据"); err != nil {
		t.Fatal(err)
	}
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal("native profile save failed", err, s.Error())
	}
	p := profiles[0]
	if strings.Contains(fmt.Sprint(p), "private-password") {
		t.Fatal("secret entered public profile")
	}
	s.OpenProfile(p)
	if s.connectionSecret != "private-password" || !s.connectionSavePassword {
		t.Fatal("reopen failed to restore saved credential")
	}
	chooseEasysearchAuth(t, tt, "访问令牌")
	if s.connectionDraft.EasysearchOptions.Auth != "access_token" || s.connectionDraft.Username != "" || s.connectionSecret != "" || s.connectionSavePassword {
		t.Fatal("auth change retained incompatible credential")
	}
	if _, ok := tt.Find("用户名"); ok {
		t.Fatal("访问令牌 authentication retained username field")
	}
	setEngineOptionInput(t, tt, "Easysearch 访问令牌", "native-opaque-token")
	if tt.HasText("native-opaque-token") {
		t.Fatal("访问令牌 rendered plainly")
	}
	s.Cancel()
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != "private-password" || s.connectionSecret != "" {
		t.Fatal("cancel changed vault or retained temporary secret")
	}
	s.OpenProfile(p)
	chooseEasysearchAuth(t, tt, "访问令牌")
	setEngineOptionInput(t, tt, "Easysearch 访问令牌", "native-opaque-token")
	if err := tt.Click("保存凭据"); err != nil {
		t.Fatal(err)
	}
	s.Save()
	p, err = service.Get(context.Background(), p.ID)
	if err != nil || p.EasysearchOptions.Auth != "access_token" || s.Error() != "" {
		t.Fatal("访问令牌 profile was not saved", err, s.Error())
	}
	s.OpenProfile(p)
	if s.connectionSecret != "native-opaque-token" || !s.connectionSavePassword {
		t.Fatal("访问令牌 restoration failed")
	}
	chooseEasysearchAuth(t, tt, "无认证")
	if _, ok := tt.Find("Easysearch 访问令牌"); ok {
		t.Fatal("no-auth retained secret field")
	}
	s.connectionDraft.TLSMode = "disable"
	s.Save()
	if _, found, _ := vault.Get(p.ID, ""); found || s.Error() != "" {
		t.Fatal("no-auth save retained previous credential", s.Error())
	}
}

func TestEasysearchEditorVaultFailureRollbackAndLockedAuthChange(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.Easysearch)
	p.Name = "original"
	p, err := service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{fmt.Sprintf("%d/", p.ID): "original-password"}}
	s.SetCredentialVault(vault)
	s.OpenProfile(p)
	s.connectionDraft.Host = "changed.invalid"
	s.setEasysearchAuth("access_token")
	s.connectionSecret, s.connectionSavePassword = "native-opaque-token", true
	vault.setErr = errors.New("locked")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.Host != p.Host || got.EasysearchOptions.Auth != "basic" || s.Error() == "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-password" {
		t.Fatal("failed vault write changed original settings or secret")
	}
	vault.setErr, vault.getErr = nil, errors.New("locked read")
	s.OpenProfile(p)
	s.setEasysearchAuth("access_token")
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.EasysearchOptions.Auth != "basic" || !strings.Contains(s.Error(), "解锁") {
		t.Fatal("unreadable password could be reused as 访问令牌")
	}
	s.OpenProfile(p)
	s.connectionDraft.Host = "new-host.invalid"
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.Host != "new-host.invalid" || s.Error() != "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-password" {
		t.Fatal("ordinary host edit erased unreadable saved credential", s.Error())
	}
}

func TestEasysearchEditorAuthChangeRevokesCapturedAsyncCheck(t *testing.T) {
	started, canceled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		defer close(finished)
		if p.EasysearchOptions.Auth != "basic" || secret != "captured-password" {
			return errors.New("incorrect immutable request")
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return errors.New("late captured-password")
	}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.Easysearch, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "async native"
	s.connectionSecret = "captured-password"
	tt := easysearchEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("native callback did not start")
	}
	chooseEasysearchAuth(t, tt, "访问令牌")
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("authentication change did not revoke old check")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("old callback did not finish")
	}
	tt.Frame()
	if s.connectionTesting || s.connectionTestResults != nil || s.Error() != "" || s.connectionSecret != "" {
		t.Fatal("late result entered new authentication draft")
	}
}

func TestEasysearchEditorInvalidCredentialAndHTTPSRejectBeforeSave(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Easysearch, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "validate before write"
	tt := easysearchEditorTester(s.State)
	chooseEasysearchAuth(t, tt, "访问令牌")
	setEngineOptionInput(t, tt, "Easysearch 访问令牌", "native token with spaces")
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 0 || s.Error() == "" || len(vault.secrets) != 0 {
		t.Fatal("malformed native access token reached persistence")
	}
	setEngineOptionInput(t, tt, "Easysearch 访问令牌", "native-opaque-token")
	if err := tt.Click("SSL"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Easysearch TLS 模式"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("disable"); err != nil {
		t.Fatal(err)
	}
	s.Save()
	profiles, err = service.List(context.Background())
	if err != nil || len(profiles) != 0 || !strings.Contains(s.Error(), "HTTPS") {
		t.Fatal("credential over HTTP reached persistence")
	}
	s.connectionDraft.TLSMode = "verify-full"
	s.connectionSavePassword = true
	vault.setErr = errors.New("write unavailable")
	s.Save()
	profiles, err = service.List(context.Background())
	if err != nil || len(profiles) != 0 || s.Error() == "" || len(vault.secrets) != 0 {
		t.Fatal("new profile vault failure left a saved profile")
	}
}
