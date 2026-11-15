package connectioneditor

import (
	"context"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestTDengineEditorURIVaultAndRawOptions(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.ImportURI("tdengine://tdengine:original%3Asecret@localhost:6042/metrics?query_timeout=120&sql_max_connections=3"); err != nil {
		t.Fatal(err)
	}
	tt := rqliteEditorTester(s.State)
	setEngineOptionInput(t, tt, "连接名称", "native TDengine")
	if tt.HasText("original:secret") {
		t.Fatal("credential exposed")
	}
	if err := tt.Click("保存密码"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "后台连接上限", "7")
	if err := tt.Click("只读模式"); err != nil {
		t.Fatal(err)
	}
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal("save failed", err, s.Error())
	}
	p := profiles[0]
	if p.Kind != connections.TDengine || p.TDengineOptions.QueryTimeoutSeconds != 120 || p.TDengineOptions.MaxConnections != 7 || p.TDengineOptions.SQLMaxConnections != 3 || !p.TDengineOptions.ReadOnly {
		t.Fatal("raw options changed")
	}
	s.OpenProfile(p)
	if s.connectionSecret != "original:secret" {
		t.Fatal("original vault credential not restored")
	}
	s.engineNumbers[2] = "0"
	if _, err := s.draftProfile(); err == nil {
		t.Fatal("invalid raw limit accepted")
	}
	before := s.connectionDraft
	if err := s.ImportURI("tdengine://host?password=changed"); err == nil || !reflect.DeepEqual(s.connectionDraft, before) {
		t.Fatal("bad URI changed draft")
	}
}
