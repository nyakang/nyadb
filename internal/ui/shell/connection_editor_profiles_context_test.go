package shell

import (
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLEditorAlternativeContextActions(t *testing.T) {
	s, tt, service, id := editMySQLProfileTester(t)
	if err := tt.Click("添加连接配置文件"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("配置文件名称"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type("Staging")
	if s.connectionOtherConfigs[0].name != "Staging" {
		t.Fatalf("native alternative name edit failed: %q", s.connectionOtherConfigs[0].name)
	}
	if err := tt.RightClick("切换配置文件：Staging"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("复制配置文件"); err != nil {
		t.Fatal(err)
	}
	if len(s.connectionOtherConfigs) != 2 || s.connectionOtherConfigs[1].name != "Staging 副本" {
		t.Fatalf("context copy failed: %+v", s.connectionOtherConfigs)
	}
	if err := tt.RightClick("切换配置文件：Staging 副本"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("删除配置文件"); err != nil {
		t.Fatal(err)
	}
	if len(s.connectionOtherConfigs) != 1 || s.connectionActiveConfig != 0 {
		t.Fatalf("context delete failed: %+v selected=%d", s.connectionOtherConfigs, s.connectionActiveConfig)
	}
	// The changes above are drafts until the user explicitly confirms.
	got, err := service.Get(context.Background(), id)
	if err != nil || len(got.Variants) != 0 {
		t.Fatal("context action persisted before Confirm")
	}
	s.saveConnectionDraft()
	got, err = service.Get(context.Background(), id)
	if err != nil || len(got.Variants) != 1 || got.Variants[0].Name != "Staging" {
		t.Fatalf("confirmed context operations were not saved: %+v %v", got.Variants, err)
	}
	if got.Kind != connections.MySQL {
		t.Fatal("context actions changed parent type")
	}
}
