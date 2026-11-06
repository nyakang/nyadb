package connectioneditor

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestMongoDBEditorPreservesAuthenticationAndPoolSettings(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	if err := s.OpenNew(connections.MongoDB, ""); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 720, 520) }, 720, 520)
	setEngineOptionInput(t, tt, "连接名称", "MongoDB saved")
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "认证数据库", "auth")
	setEngineOptionInput(t, tt, "副本集名称", "rs")
	setEngineOptionInput(t, tt, "连接池上限", "3")
	setEngineOptionInput(t, tt, "查询会话上限", "4")
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal(err, s.Error())
	}
	p := profiles[0]
	if p.Kind != connections.MongoDB || p.MongoDBOptions.AuthSource != "auth" || p.MongoDBOptions.ReplicaSet != "rs" || p.MongoDBOptions.MaxConnections != 3 || p.MongoDBOptions.QueryMaxConnections != 4 {
		t.Fatal("public MongoDB options lost")
	}
	s.OpenProfile(p)
	if s.connectionDraft.MongoDBOptions != p.MongoDBOptions {
		t.Fatal("reopen lost options")
	}
}
