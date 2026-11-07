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

func solrEditorTester(s *State) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 520) }, 720, 520)
}

func chooseSolrAuth(t *testing.T, tt *ui.Tester, auth string) {
	t.Helper()
	if err := tt.Click("Solr 认证方式"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(solrAuthCaption(auth)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}

func TestSolrEditorContextMaskedCredentialSaveReopenAndAuthSwitch(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Solr, ""); err != nil {
		t.Fatal(err)
	}
	tt := solrEditorTester(s.State)
	setEngineOptionInput(t, tt, "连接名称", "native Solr")
	setEngineOptionInput(t, tt, "Solr 服务路径", "/gateway/solr/")
	chooseSolrAuth(t, tt, "basic")
	setEngineOptionInput(t, tt, "用户名", "reader")
	setEngineOptionInput(t, tt, "密码", "private-password")
	if tt.HasText("private-password") || s.Title() != "新建连接 (Apache Solr)" {
		t.Fatal("password visible or native title lost")
	}
	if _, ok := tt.Find("初始数据库"); ok {
		t.Fatal("service context exposed as SQL database")
	}
	if err := tt.Click("保存凭据"); err != nil {
		t.Fatal(err)
	}
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal("profile save failed", err, s.Error())
	}
	p := profiles[0]
	if p.SolrOptions.ContextPath != "/gateway/solr" || strings.Contains(fmt.Sprint(p), "private-password") {
		t.Fatal("context lost or credential entered profile")
	}
	s.OpenProfile(p)
	if s.connectionSecret != "private-password" || !s.connectionSavePassword {
		t.Fatal("credential not restored")
	}
	chooseSolrAuth(t, tt, "none")
	if s.connectionDraft.Username != "" || s.connectionSecret != "" || s.connectionSavePassword {
		t.Fatal("authentication change retained credential")
	}
	if _, ok := tt.Find("密码"); ok {
		t.Fatal("no authentication retained password field")
	}
	s.Cancel()
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != "private-password" || s.connectionSecret != "" {
		t.Fatal("cancel changed vault or retained secret")
	}
	s.OpenProfile(p)
	chooseSolrAuth(t, tt, "none")
	s.connectionDraft.TLSMode = "disable"
	s.Save()
	if _, found, _ := vault.Get(p.ID, ""); found || s.Error() != "" {
		t.Fatal("no-auth save retained credential", s.Error())
	}
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.SolrOptions.Auth != "none" || got.SolrOptions.ContextPath != "/gateway/solr" {
		t.Fatal("saved context or auth changed", err)
	}
}

func TestSolrEditorVaultFailureRollsBackAndLockedAuthSwitchIsRejected(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.Solr)
	p.Name, p.Username, p.SolrOptions.Auth = "original", "reader", "basic"
	p, err := service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{fmt.Sprintf("%d/", p.ID): "original-password"}}
	s.SetCredentialVault(vault)
	s.OpenProfile(p)
	s.connectionDraft.SolrOptions.ContextPath = "/changed/solr"
	s.connectionSecret, s.connectionSavePassword = "replacement-password", true
	vault.setErr = errors.New("locked")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.SolrOptions != p.SolrOptions || s.Error() == "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-password" {
		t.Fatal("vault failure did not restore public settings and credential")
	}
	vault.setErr, vault.getErr = nil, errors.New("locked read")
	s.OpenProfile(p)
	s.setSolrAuth("none")
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.SolrOptions.Auth != "basic" || !strings.Contains(s.Error(), "解锁") {
		t.Fatal("locked credential allowed authentication switch")
	}
	s.OpenProfile(p)
	s.connectionDraft.Host = "new-host.invalid"
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.Host != "new-host.invalid" || s.Error() != "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-password" {
		t.Fatal("ordinary edit erased unreadable credential", s.Error())
	}
}

func TestSolrEditorInvalidPathAndPlaintextCredentialNeverReachStorage(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Solr, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "validate before write"
	s.connectionDraft.SolrOptions.ContextPath = "/solr/../other"
	s.Save()
	if profiles, err := service.List(context.Background()); err != nil || len(profiles) != 0 || s.validationErrorField != "Solr 服务路径" {
		t.Fatal("invalid context reached storage")
	}
	s.connectionDraft.SolrOptions.ContextPath = "/solr"
	s.setSolrAuth("basic")
	s.connectionDraft.Username = "reader"
	s.connectionSecret = "private-password"
	s.connectionDraft.TLSMode = "disable"
	s.Save()
	if profiles, err := service.List(context.Background()); err != nil || len(profiles) != 0 || !strings.Contains(s.Error(), "HTTPS") {
		t.Fatal("plaintext credential reached storage")
	}
	s.connectionDraft.TLSMode = "verify-full"
	s.connectionSavePassword = true
	vault.setErr = errors.New("unavailable")
	s.Save()
	if profiles, err := service.List(context.Background()); err != nil || len(profiles) != 0 || s.Error() == "" || len(vault.secrets) != 0 {
		t.Fatal("new profile vault failure left public record")
	}
}

func TestSolrEditorAuthSwitchRevokesCapturedCheckAndPublicImportStaysPending(t *testing.T) {
	started, canceled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		defer close(finished)
		if p.SolrOptions.Auth != "basic" || p.SolrOptions.ContextPath != "/gateway/solr" || secret != "captured-password" {
			return errors.New("incorrect immutable request")
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return errors.New("late captured-password")
	}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.Solr, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "captured check"
	s.setSolrAuth("basic")
	s.connectionDraft.Username, s.connectionDraft.SolrOptions.ContextPath = "reader", "/gateway/solr"
	s.connectionSecret = "captured-password"
	if err := s.ImportURI("solr://reader:private@localhost"); !errors.Is(err, connections.ErrURI) || s.connectionSecret != "captured-password" || s.connectionDraft.Name != "captured check" {
		t.Fatal("public import enabled before provider or changed parked draft")
	}
	tt := solrEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not start")
	}
	chooseSolrAuth(t, tt, "none")
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("authentication switch did not revoke check")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("old callback did not finish")
	}
	tt.Frame()
	if s.connectionTesting || s.connectionTestResults != nil || s.Error() != "" || s.connectionSecret != "" {
		t.Fatal("late result entered new draft")
	}
}
