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

func elasticsearchEditorTester(s *State) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 520) }, 720, 520)
}

func chooseElasticsearchAuth(t *testing.T, tt *ui.Tester, caption string) {
	t.Helper()
	if err := tt.Click("Elasticsearch 认证方式"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(caption); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}

func TestElasticsearchEditorAuthSelectionMaskedFieldsAndVaultLifecycle(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Elasticsearch, ""); err != nil {
		t.Fatal(err)
	}
	tt := elasticsearchEditorTester(s.State)
	setEngineOptionInput(t, tt, "连接名称", "native Elasticsearch")
	setEngineOptionInput(t, tt, "密码", "private-password")
	if tt.HasText("private-password") || s.Title() != "新建连接 (Elasticsearch)" {
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
	chooseElasticsearchAuth(t, tt, "API key")
	if s.connectionDraft.ElasticsearchOptions.Auth != "api_key" || s.connectionDraft.Username != "" || s.connectionSecret != "" || s.connectionSavePassword {
		t.Fatal("auth change retained incompatible credential")
	}
	if _, ok := tt.Find("用户名"); ok {
		t.Fatal("API key authentication retained username field")
	}
	setEngineOptionInput(t, tt, "Elasticsearch API key", "aWQ6a2V5")
	if tt.HasText("aWQ6a2V5") {
		t.Fatal("API key rendered plainly")
	}
	s.Cancel()
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != "private-password" || s.connectionSecret != "" {
		t.Fatal("cancel changed vault or retained temporary secret")
	}
	s.OpenProfile(p)
	chooseElasticsearchAuth(t, tt, "API key")
	setEngineOptionInput(t, tt, "Elasticsearch API key", "aWQ6a2V5")
	if err := tt.Click("保存凭据"); err != nil {
		t.Fatal(err)
	}
	s.Save()
	p, err = service.Get(context.Background(), p.ID)
	if err != nil || p.ElasticsearchOptions.Auth != "api_key" || s.Error() != "" {
		t.Fatal("API key profile was not saved", err, s.Error())
	}
	s.OpenProfile(p)
	if s.connectionSecret != "aWQ6a2V5" || !s.connectionSavePassword {
		t.Fatal("API key restoration failed")
	}
	chooseElasticsearchAuth(t, tt, "无认证")
	if _, ok := tt.Find("Elasticsearch API key"); ok {
		t.Fatal("no-auth retained secret field")
	}
	s.connectionDraft.TLSMode = "disable"
	s.Save()
	if _, found, _ := vault.Get(p.ID, ""); found || s.Error() != "" {
		t.Fatal("no-auth save retained previous credential", s.Error())
	}
}

func TestElasticsearchEditorVaultFailureRollbackAndLockedAuthChange(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.Elasticsearch)
	p.Name = "original"
	p, err := service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{fmt.Sprintf("%d/", p.ID): "original-password"}}
	s.SetCredentialVault(vault)
	s.OpenProfile(p)
	s.connectionDraft.Host = "changed.invalid"
	s.setElasticsearchAuth("api_key")
	s.connectionSecret, s.connectionSavePassword = "aWQ6a2V5", true
	vault.setErr = errors.New("locked")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.Host != p.Host || got.ElasticsearchOptions.Auth != "basic" || s.Error() == "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-password" {
		t.Fatal("failed vault write changed original settings or secret")
	}
	vault.setErr, vault.getErr = nil, errors.New("locked read")
	s.OpenProfile(p)
	s.setElasticsearchAuth("api_key")
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.ElasticsearchOptions.Auth != "basic" || !strings.Contains(s.Error(), "解锁") {
		t.Fatal("unreadable password could be reused as API key")
	}
	s.OpenProfile(p)
	s.connectionDraft.Host = "new-host.invalid"
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.Host != "new-host.invalid" || s.Error() != "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-password" {
		t.Fatal("ordinary host edit erased unreadable saved credential", s.Error())
	}
}

func TestElasticsearchEditorAuthChangeRevokesCapturedAsyncCheck(t *testing.T) {
	started, canceled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		defer close(finished)
		if p.ElasticsearchOptions.Auth != "basic" || secret != "captured-password" {
			return errors.New("incorrect immutable request")
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return errors.New("late captured-password")
	}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.Elasticsearch, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "async native"
	s.connectionSecret = "captured-password"
	tt := elasticsearchEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("native callback did not start")
	}
	chooseElasticsearchAuth(t, tt, "API key")
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

func TestElasticsearchEditorInvalidCredentialAndHTTPSRejectBeforeSave(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Elasticsearch, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "validate before write"
	tt := elasticsearchEditorTester(s.State)
	chooseElasticsearchAuth(t, tt, "API key")
	setEngineOptionInput(t, tt, "Elasticsearch API key", "malformed-key")
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 0 || s.Error() == "" || len(vault.secrets) != 0 {
		t.Fatal("malformed API key reached persistence")
	}
	setEngineOptionInput(t, tt, "Elasticsearch API key", "aWQ6a2V5")
	if err := tt.Click("SSL"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Elasticsearch TLS 模式"); err != nil {
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
