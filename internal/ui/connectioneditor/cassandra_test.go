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

func cassandraEditorTester(s *State) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 520) }, 720, 520)
}

func chooseCassandraAuth(t *testing.T, tt *ui.Tester, auth string) {
	t.Helper()
	if err := tt.Click("Cassandra 认证方式"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(cassandraAuthCaption(auth)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}

func TestCassandraEditorMaskedCredentialsSaveReopenAndAuthRemoval(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Cassandra, ""); err != nil {
		t.Fatal(err)
	}
	tt := cassandraEditorTester(s.State)
	setEngineOptionInput(t, tt, "连接名称", "native Cassandra")
	setEngineOptionInput(t, tt, "Cassandra 初始 Keyspace", "MixedCase")
	chooseCassandraAuth(t, tt, "password")
	setEngineOptionInput(t, tt, "用户名", " native 用户 ")
	setEngineOptionInput(t, tt, "密码", " private-password ")
	if tt.HasText(" private-password ") || s.Title() != "新建连接 (Apache Cassandra)" {
		t.Fatal("password exposed or wrong title")
	}
	if _, ok := tt.Find("初始数据库"); ok {
		t.Fatal("Keyspace mislabeled")
	}
	if err := tt.Click("保存凭据"); err != nil {
		t.Fatal(err)
	}
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal("native save failed", s.Error())
	}
	p := profiles[0]
	if p.Database != "MixedCase" || p.Username != " native 用户 " || strings.Contains(fmt.Sprint(p), "private-password") {
		t.Fatal("native identity/secret separation lost")
	}
	s.OpenProfile(p)
	if s.connectionSecret != " private-password " || !s.connectionSavePassword {
		t.Fatal("credential not reopened")
	}
	chooseCassandraAuth(t, tt, "none")
	if s.connectionDraft.Username != "" || s.connectionSecret != "" || s.connectionSavePassword {
		t.Fatal("auth switch retained temporary credential")
	}
	s.Cancel()
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != " private-password " {
		t.Fatal("cancel changed saved credential")
	}
	s.OpenProfile(p)
	chooseCassandraAuth(t, tt, "none")
	s.Save()
	if _, found, _ := vault.Get(p.ID, ""); found || s.Error() != "" {
		t.Fatal("auth removal did not remove credential")
	}
}

func TestCassandraEditorVaultFailureRollbackAndLockedAuthChange(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.Cassandra)
	p.Name = "original"
	p.Username = "reader"
	p.CassandraOptions.Auth = "password"
	p, err := service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{secrets: map[string]string{fmt.Sprintf("%d/", p.ID): "original-password"}}
	s.SetCredentialVault(vault)
	s.OpenProfile(p)
	s.connectionDraft.Database = "changed"
	s.connectionSecret, s.connectionSavePassword = "new-password", true
	vault.setErr = errors.New("locked")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.Database != "" || s.Error() == "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-password" {
		t.Fatal("failed vault write did not restore original profile")
	}
	vault.setErr, vault.getErr = nil, errors.New("locked read")
	s.OpenProfile(p)
	s.setCassandraAuth("none")
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.CassandraOptions.Auth != "password" || !strings.Contains(s.Error(), "解锁") {
		t.Fatal("locked vault allowed auth change")
	}
	s.OpenProfile(p)
	s.connectionDraft.Host = "new-host.invalid"
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.Host != "new-host.invalid" || s.Error() != "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-password" {
		t.Fatal("host edit erased unreadable credential")
	}
	newState, _, newService, _ := profileTestState(t)
	vault.getErr = nil
	vault.setErr = errors.New("unavailable")
	newState.SetCredentialVault(vault)
	_ = newState.OpenNew(connections.Cassandra, "")
	newState.connectionDraft.Name = "new"
	newState.setCassandraAuth("password")
	newState.connectionDraft.Username = "reader"
	newState.connectionSecret = "private-password"
	newState.connectionSavePassword = true
	newState.Save()
	if profiles, err := newService.List(context.Background()); err != nil || len(profiles) != 0 || newState.Error() == "" {
		t.Fatal("failed new vault write left profile")
	}
}

func TestCassandraEditorAuthSwitchRevokesOriginalCheckAndPublicImportValidates(t *testing.T) {
	started, canceled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		defer close(finished)
		if p.CassandraOptions.Auth != "password" || p.Database != "MixedCase" || secret != "captured-password" {
			return errors.New("incorrect immutable request")
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return errors.New("late captured-password")
	}})
	defer s.Close()
	_ = s.OpenNew(connections.Cassandra, "")
	s.connectionDraft.Name = "captured"
	s.setCassandraAuth("password")
	s.connectionDraft.Username = "reader"
	s.connectionDraft.Database = "MixedCase"
	s.connectionSecret = "captured-password"
	if err := s.ImportURI("cassandra://reader:private@localhost/app?unsupported=true"); err == nil || s.connectionSecret != "captured-password" || s.connectionDraft.Name != "captured" {
		t.Fatal("invalid public import changed draft")
	}
	tt := cassandraEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("check did not start")
	}
	chooseCassandraAuth(t, tt, "none")
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("auth switch did not revoke check")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("original check did not finish")
	}
	tt.Frame()
	if s.connectionTesting || s.connectionTestResults != nil || s.Error() != "" || s.connectionSecret != "" {
		t.Fatal("late result entered changed draft")
	}
	if err := s.ImportURI("cassandra://reader:separate@localhost/MixedCase?sslmode=disable"); err != nil || s.Draft().Kind != connections.Cassandra || s.Draft().Database != "MixedCase" || s.connectionSecret != "separate" {
		t.Fatal("public import lost native configuration")
	}
}
