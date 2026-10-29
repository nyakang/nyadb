package shell

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLNotesSaveCancelEditAndCopy(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionEditor.Draft().Name = "notes profile"
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("备注"); err != nil {
		t.Fatal(err)
	}
	const initial = "用途：测试连接\n负责人：本地"
	s.connectionEditor.Draft().Notes = initial
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDialog {
		t.Fatalf("saving notes failed: %q", s.connectionEditor.Error())
	}
	all, err := service.List(context.Background())
	if err != nil || len(all) != 1 || all[0].Notes != initial {
		t.Fatalf("saved note missing from profiles: %+v %v", all, err)
	}
	s.startEditProfile()
	if s.connectionEditor.Draft().Notes != initial {
		t.Fatal("edit did not load saved note")
	}
	s.connectionEditor.Draft().Notes = "cancelled draft"
	s.closeConnectionDialog()
	got, err := service.Get(context.Background(), all[0].ID)
	if err != nil || got.Notes != initial {
		t.Fatalf("cancel erroneously persisted note: %+v %v", got, err)
	}
	s.startEditProfile()
	s.connectionEditor.Draft().Notes = initial + "\n更新"
	s.saveConnectionDraft()
	got, err = service.Get(context.Background(), all[0].ID)
	if err != nil || !strings.HasSuffix(got.Notes, "更新") {
		t.Fatalf("edited notes not persisted: %+v %v", got, err)
	}
	s.duplicateProfile()
	list, err := service.List(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("duplicate failed: %+v %v", list, err)
	}
	for _, profile := range list {
		if profile.Notes != got.Notes {
			t.Fatalf("copy lost notes: %+v", profile)
		}
	}
}
