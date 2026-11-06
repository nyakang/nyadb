package connectioneditor

import (
	"context"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"testing"
)

func TestOracleEditorPersistsServiceAndIndependentPoolLimits(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	if err := s.OpenNew(connections.Oracle, ""); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 720, 520) }, 720, 520)
	setEngineOptionInput(t, tt, "连接名称", "Oracle saved")
	setEngineOptionInput(t, tt, "用户名", "tenant")
	setEngineOptionInput(t, tt, "服务名 / SID", "APP_SERVICE")
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "后台连接上限", "3")
	setEngineOptionInput(t, tt, "SQL 会话上限", "5")
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal(err, s.Error())
	}
	p := profiles[0]
	if p.Kind != connections.Oracle || p.Database != "APP_SERVICE" || p.OracleOptions.MaxConnections != 3 || p.OracleOptions.SQLMaxConnections != 5 || p.OracleOptions.DefaultSchema != "TENANT" {
		t.Fatal(p)
	}
	s.OpenProfile(p)
	if s.connectionDraft.OracleOptions != p.OracleOptions || s.sqlMaxConnections != "5" {
		t.Fatal("Oracle options changed when reopened")
	}
}
