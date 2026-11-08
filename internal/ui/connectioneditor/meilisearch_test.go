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

func meilisearchEditorTester(s *State) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 520) }, 720, 520)
}

func chooseMeilisearchAuth(t *testing.T, tt *ui.Tester, auth string) {
	t.Helper()
	if err := tt.Click("Meilisearch 认证方式"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(meilisearchAuthCaption(auth)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}

func TestMeilisearchEditorContextMaskedCredentialSaveReopenAndAuthSwitch(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Meilisearch, ""); err != nil {
		t.Fatal(err)
	}
	tt := meilisearchEditorTester(s.State)
	setEngineOptionInput(t, tt, "连接名称", "native Meilisearch")
	setEngineOptionInput(t, tt, "Meilisearch 服务路径", "/gateway/meilisearch/")
	chooseMeilisearchAuth(t, tt, "bearer")
	setEngineOptionInput(t, tt, "Meilisearch API key", "owned-token")
	if tt.HasText("owned-token") || s.Title() != "新建连接 (Meilisearch)" {
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
	if p.MeilisearchOptions.ContextPath != "/gateway/meilisearch" || strings.Contains(fmt.Sprint(p), "owned-token") {
		t.Fatal("context lost or credential entered profile")
	}
	s.OpenProfile(p)
	if s.connectionSecret != "owned-token" || !s.connectionSavePassword {
		t.Fatal("credential not restored")
	}
	chooseMeilisearchAuth(t, tt, "none")
	if s.connectionDraft.Username != "" || s.connectionSecret != "" || s.connectionSavePassword {
		t.Fatal("authentication change retained credential")
	}
	if _, ok := tt.Find("Meilisearch API key"); ok {
		t.Fatal("no authentication retained password field")
	}
	s.Cancel()
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != "owned-token" || s.connectionSecret != "" {
		t.Fatal("cancel changed vault or retained secret")
	}
	s.OpenProfile(p)
	chooseMeilisearchAuth(t, tt, "none")
	s.connectionDraft.TLSMode = "disable"
	s.Save()
	if _, found, _ := vault.Get(p.ID, ""); found || s.Error() != "" {
		t.Fatal("no-auth save retained credential", s.Error())
	}
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.MeilisearchOptions.Auth != "none" || got.MeilisearchOptions.ContextPath != "/gateway/meilisearch" {
		t.Fatal("saved context or auth changed", err)
	}
}

func TestMeilisearchEditorVaultFailureRollsBackAndLockedAuthSwitchIsRejected(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.Meilisearch)
	p.Name, p.MeilisearchOptions.Auth = "original", "bearer"
	p, err := service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{fmt.Sprintf("%d/", p.ID): "original-password"}}
	s.SetCredentialVault(vault)
	s.OpenProfile(p)
	s.connectionDraft.MeilisearchOptions.ContextPath = "/changed/meilisearch"
	s.connectionSecret, s.connectionSavePassword = "replacement-password", true
	vault.setErr = errors.New("locked")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.MeilisearchOptions != p.MeilisearchOptions || s.Error() == "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-password" {
		t.Fatal("vault failure did not restore public settings and credential")
	}
	vault.setErr, vault.getErr = nil, errors.New("locked read")
	s.OpenProfile(p)
	s.setMeilisearchAuth("none")
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.MeilisearchOptions.Auth != "bearer" || !strings.Contains(s.Error(), "解锁") {
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

func TestMeilisearchEditorInvalidPathAndVaultFailureNeverReachStorage(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Meilisearch, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "validate before write"
	s.connectionDraft.MeilisearchOptions.ContextPath = "/meilisearch/../other"
	s.Save()
	if profiles, err := service.List(context.Background()); err != nil || len(profiles) != 0 || s.validationErrorField != "Meilisearch 服务路径" {
		t.Fatal("invalid context reached storage")
	}
	s.connectionDraft.MeilisearchOptions.ContextPath = "/meilisearch"
	s.setMeilisearchAuth("bearer")
	s.connectionSecret = "owned-token"
	s.connectionSavePassword = true
	vault.setErr = errors.New("unavailable")
	s.Save()
	if profiles, err := service.List(context.Background()); err != nil || len(profiles) != 0 || s.Error() == "" || len(vault.secrets) != 0 {
		t.Fatal("new profile vault failure left public record")
	}
}

func TestMeilisearchEditorAuthSwitchRevokesCapturedCheckAndInvalidImport(t *testing.T) {
	started, canceled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		defer close(finished)
		if p.MeilisearchOptions.Auth != "bearer" || p.MeilisearchOptions.ContextPath != "/gateway/meilisearch" || secret != "captured-password" {
			return errors.New("incorrect immutable request")
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return errors.New("late captured-password")
	}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.Meilisearch, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "captured check"
	s.setMeilisearchAuth("bearer")
	s.connectionDraft.MeilisearchOptions.ContextPath = "/gateway/meilisearch"
	s.connectionSecret = "captured-password"
	if err := s.ImportURI("meilisearch://reader:private@localhost?auth=api_key"); err == nil || s.connectionSecret != "captured-password" || s.connectionDraft.Name != "captured check" {
		t.Fatal("invalid public import changed parked draft")
	}
	tt := meilisearchEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not start")
	}
	chooseMeilisearchAuth(t, tt, "none")
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
