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

func couchdbEditorTester(s *State) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 520) }, 720, 520)
}

func chooseCouchDBAuth(t *testing.T, tt *ui.Tester, auth string) {
	t.Helper()
	if err := tt.Click("CouchDB 认证方式"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(couchdbAuthCaption(auth)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}

func TestCouchDBEditorContextMaskedCredentialSaveReopenAndAuthSwitch(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.CouchDB, ""); err != nil {
		t.Fatal(err)
	}
	tt := couchdbEditorTester(s.State)
	setEngineOptionInput(t, tt, "连接名称", "native CouchDB")
	setEngineOptionInput(t, tt, "CouchDB 服务路径", "/gateway/couchdb/")
	chooseCouchDBAuth(t, tt, "basic")
	setEngineOptionInput(t, tt, "用户名", " 原生用户🐱 ")
	setEngineOptionInput(t, tt, "密码", " 密码🐱 ")
	if tt.HasText(" 密码🐱 ") || s.Title() != "新建连接 (Apache CouchDB)" {
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
	if p.CouchDBOptions.ContextPath != "/gateway/couchdb" || strings.Contains(fmt.Sprint(p), " 密码🐱 ") {
		t.Fatal("context lost or credential entered profile")
	}
	s.OpenProfile(p)
	if s.connectionSecret != " 密码🐱 " || !s.connectionSavePassword {
		t.Fatal("credential not restored")
	}
	chooseCouchDBAuth(t, tt, "none")
	if s.connectionDraft.Username != "" || s.connectionSecret != "" || s.connectionSavePassword {
		t.Fatal("authentication change retained credential")
	}
	if _, ok := tt.Find("密码"); ok {
		t.Fatal("no authentication retained password field")
	}
	s.Cancel()
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != " 密码🐱 " || s.connectionSecret != "" {
		t.Fatal("cancel changed vault or retained secret")
	}
	s.OpenProfile(p)
	chooseCouchDBAuth(t, tt, "none")
	s.connectionDraft.TLSMode = "disable"
	s.Save()
	if _, found, _ := vault.Get(p.ID, ""); found || s.Error() != "" {
		t.Fatal("no-auth save retained credential", s.Error())
	}
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.CouchDBOptions.Auth != "none" || got.CouchDBOptions.ContextPath != "/gateway/couchdb" {
		t.Fatal("saved context or auth changed", err)
	}
}

func TestCouchDBEditorVaultFailureRollsBackAndLockedAuthSwitchIsRejected(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.CouchDB)
	p.Name, p.Username, p.CouchDBOptions.Auth = "original", "reader", "basic"
	p, err := service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{fmt.Sprintf("%d/", p.ID): "original-password"}}
	s.SetCredentialVault(vault)
	s.OpenProfile(p)
	s.connectionDraft.CouchDBOptions.ContextPath = "/changed/couchdb"
	s.connectionSecret, s.connectionSavePassword = "replacement-password", true
	vault.setErr = errors.New("locked")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.CouchDBOptions != p.CouchDBOptions || s.Error() == "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-password" {
		t.Fatal("vault failure did not restore public settings and credential")
	}
	vault.setErr, vault.getErr = nil, errors.New("locked read")
	s.OpenProfile(p)
	s.setCouchDBAuth("none")
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.CouchDBOptions.Auth != "basic" || !strings.Contains(s.Error(), "解锁") {
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

func TestCouchDBEditorInvalidPathAndVaultFailureNeverReachStorage(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.CouchDB, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "validate before write"
	s.connectionDraft.CouchDBOptions.ContextPath = "/couchdb/../other"
	s.Save()
	if profiles, err := service.List(context.Background()); err != nil || len(profiles) != 0 || s.validationErrorField != "CouchDB 服务路径" {
		t.Fatal("invalid context reached storage")
	}
	s.connectionDraft.CouchDBOptions.ContextPath = "/couchdb"
	s.setCouchDBAuth("basic")
	s.connectionDraft.Username = "reader"
	s.connectionSecret = " 密码🐱 "
	s.connectionSavePassword = true
	vault.setErr = errors.New("unavailable")
	s.Save()
	if profiles, err := service.List(context.Background()); err != nil || len(profiles) != 0 || s.Error() == "" || len(vault.secrets) != 0 {
		t.Fatal("new profile vault failure left public record")
	}
}

func TestCouchDBEditorAuthSwitchRevokesCapturedCheckAndInvalidImport(t *testing.T) {
	started, canceled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		defer close(finished)
		if p.CouchDBOptions.Auth != "basic" || p.CouchDBOptions.ContextPath != "/gateway/couchdb" || secret != "captured-password" {
			return errors.New("incorrect immutable request")
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return errors.New("late captured-password")
	}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.CouchDB, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "captured check"
	s.setCouchDBAuth("basic")
	s.connectionDraft.Username, s.connectionDraft.CouchDBOptions.ContextPath = "reader", "/gateway/couchdb"
	s.connectionSecret = "captured-password"
	if err := s.ImportURI("couchdb://reader:private@localhost?auth=api_key"); err == nil || s.connectionSecret != "captured-password" || s.connectionDraft.Name != "captured check" {
		t.Fatal("invalid public import changed parked draft")
	}
	tt := couchdbEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not start")
	}
	chooseCouchDBAuth(t, tt, "none")
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
