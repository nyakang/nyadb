package shell

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLNotesNativeTextAreaAndTabSwitch(t *testing.T) {
	s := New()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("备注"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("备注：*") || !tt.HasText("* 更改将应用于所有连接配置文件。") {
		t.Fatal("original note label and footnote are missing")
	}
	if err := tt.Click("连接备注"); err != nil {
		t.Fatal(err)
	}
	const notes = "第一行\nSecond line\n第三行"
	tt.Type(notes)
	if s.connectionDraft.Notes != notes {
		t.Fatalf("multiline note not edited by native TextArea: %q", s.connectionDraft.Notes)
	}
	if err := tt.Click("常规"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("备注"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDraft.Notes != notes {
		t.Fatal("switching tabs discarded the edit draft")
	}
}

func TestMySQLNotesSaveCancelEditAndCopy(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "notes profile"
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)
	if err := tt.Click("备注"); err != nil {
		t.Fatal(err)
	}
	const initial = "用途：测试连接\n负责人：本地"
	s.connectionDraft.Notes = initial
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDialog {
		t.Fatalf("saving notes failed: %q", s.connectionError)
	}
	all, err := service.List(context.Background())
	if err != nil || len(all) != 1 || all[0].Notes != initial {
		t.Fatalf("saved note missing from profiles: %+v %v", all, err)
	}
	s.startEditProfile()
	if s.connectionDraft.Notes != initial {
		t.Fatal("edit did not load saved note")
	}
	s.connectionDraft.Notes = "cancelled draft"
	s.closeConnectionDialog()
	got, err := service.Get(context.Background(), all[0].ID)
	if err != nil || got.Notes != initial {
		t.Fatalf("cancel erroneously persisted note: %+v %v", got, err)
	}
	s.startEditProfile()
	s.connectionDraft.Notes = initial + "\n更新"
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

func TestMySQLNotesValidationInEditor(t *testing.T) {
	s, _, _, _ := profileTestState(t)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "too long notes"
	s.connectionDraft.Notes = strings.Repeat("x", 64*1024+1)
	s.saveConnectionDraft()
	if !s.connectionDialog || s.connectionError == "" {
		t.Fatal("oversize notes should keep editor open and show validation error")
	}
}
