package connectioneditor

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestRedisEditorPreservesAuthenticationAndPoolSettings(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	if err := s.OpenNew(connections.Redis, ""); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 720, 520) }, 720, 520)
	setEngineOptionInput(t, tt, "连接名称", "Redis saved")
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "连接超时（秒）", "12")
	setEngineOptionInput(t, tt, "连接池上限", "3")
	setEngineOptionInput(t, tt, "查询会话上限", "4")
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal(err, s.Error())
	}
	p := profiles[0]
	if p.Kind != connections.Redis || p.RedisOptions.ConnectTimeoutSeconds != 12 || p.RedisOptions.MaxConnections != 3 || p.RedisOptions.QueryMaxConnections != 4 {
		t.Fatal("public Redis options lost")
	}
	s.OpenProfile(p)
	if s.connectionDraft.RedisOptions != p.RedisOptions {
		t.Fatal("reopen lost options")
	}
}
