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

func qdrantEditorTester(s *State) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 520) }, 720, 520)
}

func chooseQdrantAuth(t *testing.T, tt *ui.Tester, auth string) {
	t.Helper()
	if err := tt.Click("Qdrant 认证方式"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(qdrantAuthCaption(auth)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}

func TestQdrantEditorContextMaskedCredentialSaveReopenAndAuthSwitch(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Qdrant, ""); err != nil {
		t.Fatal(err)
	}
	tt := qdrantEditorTester(s.State)
	setEngineOptionInput(t, tt, "连接名称", "native Qdrant")
	setEngineOptionInput(t, tt, "Qdrant 服务路径", "/gateway/qdrant/")
	chooseQdrantAuth(t, tt, "api_key")
	setEngineOptionInput(t, tt, "Qdrant API key", "owned-token")
	if tt.HasText("owned-token") || s.Title() != "新建连接 (Qdrant)" {
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
	if p.QdrantOptions.ContextPath != "/gateway/qdrant" || strings.Contains(fmt.Sprint(p), "owned-token") {
		t.Fatal("context lost or credential entered profile")
	}
	s.OpenProfile(p)
	if s.connectionSecret != "owned-token" || !s.connectionSavePassword {
		t.Fatal("credential not restored")
	}
	chooseQdrantAuth(t, tt, "none")
	if s.connectionDraft.Username != "" || s.connectionSecret != "" || s.connectionSavePassword {
		t.Fatal("authentication change retained credential")
	}
	if _, ok := tt.Find("Qdrant API key"); ok {
		t.Fatal("no authentication retained password field")
	}
	s.Cancel()
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != "owned-token" || s.connectionSecret != "" {
		t.Fatal("cancel changed vault or retained secret")
	}
	s.OpenProfile(p)
	chooseQdrantAuth(t, tt, "none")
	s.connectionDraft.TLSMode = "disable"
	s.Save()
	if _, found, _ := vault.Get(p.ID, ""); found || s.Error() != "" {
		t.Fatal("no-auth save retained credential", s.Error())
	}
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.QdrantOptions.Auth != "none" || got.QdrantOptions.ContextPath != "/gateway/qdrant" {
		t.Fatal("saved context or auth changed", err)
	}
}

func TestQdrantEditorVaultFailureRollsBackAndLockedAuthSwitchIsRejected(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.Qdrant)
	p.Name, p.QdrantOptions.Auth = "original", "api_key"
	p, err := service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{fmt.Sprintf("%d/", p.ID): "original-password"}}
	s.SetCredentialVault(vault)
	s.OpenProfile(p)
	s.connectionDraft.QdrantOptions.ContextPath = "/changed/qdrant"
	s.connectionSecret, s.connectionSavePassword = "replacement-password", true
	vault.setErr = errors.New("locked")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.QdrantOptions != p.QdrantOptions || s.Error() == "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-password" {
		t.Fatal("vault failure did not restore public settings and credential")
	}
	vault.setErr, vault.getErr = nil, errors.New("locked read")
	s.OpenProfile(p)
	s.setQdrantAuth("none")
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.QdrantOptions.Auth != "api_key" || !strings.Contains(s.Error(), "解锁") {
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

func TestQdrantEditorInvalidPathAndVaultFailureNeverReachStorage(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Qdrant, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "validate before write"
	s.connectionDraft.QdrantOptions.ContextPath = "/qdrant/../other"
	s.Save()
	if profiles, err := service.List(context.Background()); err != nil || len(profiles) != 0 || s.validationErrorField != "Qdrant 服务路径" {
		t.Fatal("invalid context reached storage")
	}
	s.connectionDraft.QdrantOptions.ContextPath = "/qdrant"
	s.setQdrantAuth("api_key")
	s.connectionSecret = "owned-token"
	s.connectionSavePassword = true
	vault.setErr = errors.New("unavailable")
	s.Save()
	if profiles, err := service.List(context.Background()); err != nil || len(profiles) != 0 || s.Error() == "" || len(vault.secrets) != 0 {
		t.Fatal("new profile vault failure left public record")
	}
}

func TestQdrantEditorAuthSwitchRevokesCapturedCheckAndInvalidImport(t *testing.T) {
	started, canceled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		defer close(finished)
		if p.QdrantOptions.Auth != "api_key" || p.QdrantOptions.ContextPath != "/gateway/qdrant" || secret != "captured-password" {
			return errors.New("incorrect immutable request")
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return errors.New("late captured-password")
	}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.Qdrant, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "captured check"
	s.setQdrantAuth("api_key")
	s.connectionDraft.QdrantOptions.ContextPath = "/gateway/qdrant"
	s.connectionSecret = "captured-password"
	for _, uri := range []string{"qdrant://reader:private@localhost?auth=api_key", "qdrant://localhost?auth=bearer"} {
		if err := s.ImportURI(uri); err == nil || s.connectionSecret != "captured-password" || s.connectionDraft.Name != "captured check" {
			t.Fatal("invalid public import changed parked draft")
		}
	}
	tt := qdrantEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not start")
	}
	chooseQdrantAuth(t, tt, "none")
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
