package connectioneditor

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestURIEditorMasksInputRetainsFailuresAndSavesOnlyThroughVault(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := service.CreateGroup(context.Background(), "group"); err != nil {
		t.Fatal(err)
	}
	s.BeginURI("group")
	imported := false
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.URIView(c, 700, 420, func() { imported = true }) }, 700, 420)
	tt.Frame()
	if err := tt.Click("连接 URI"); err != nil {
		t.Fatal(err)
	}
	tt.Type("mysql://alice:private-password@localhost:3307/original?unsupported=1")
	if err := tt.Click("导入到连接表单"); err != nil {
		t.Fatal(err)
	}
	if imported || !s.URIActive() || s.uriText == "" || strings.Contains(s.uriError, "private-password") || s.Active() {
		t.Fatal("invalid URI altered editor or leaked input")
	}
	s.uriText = "mysql://alice:private-password@localhost:3307/original"
	tt.Frame()
	if err := tt.Click("导入到连接表单"); err != nil {
		t.Fatal(err)
	}
	if !imported || s.uriText != "" || s.URIActive() || s.connectionSecret != "private-password" || s.Draft().GroupName != "group" || s.connectionSavePassword {
		t.Fatal("URI import lost temporary secret or silently opted into saving it")
	}
	s.connectionSavePassword = true
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 {
		t.Fatal("URI profile did not save:", s.Error(), err)
	}
	p := profiles[0]
	password, found, err := vault.Get(p.ID, "")
	if err != nil || !found || password != "private-password" || p.Host != "localhost" || p.Database != "original" || p.Port != 3307 || s.connectionSecret != "" {
		t.Fatal("URI secret did not use the existing vault lifecycle")
	}
}
func TestURIEditorCancelClearsMaskedInputsAndCreatesNoProfile(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	s.BeginURI("")
	s.uriText = "postgres://alice:private-password@localhost/original"
	s.Cancel()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 0 || s.uriText != "" || s.connectionSecret != "" || s.URIActive() {
		t.Fatal("canceled URI retained credentials or saved a profile")
	}
}
