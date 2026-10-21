package shell

import (
	"context"
	"strings"
	"testing"
)

func TestMySQLActiveConfigurationIsIndependentOfSelection(t *testing.T) {
	s, tt, service, id := editMySQLProfileTester(t)
	if err := tt.Click("添加连接配置文件"); err != nil {
		t.Fatal(err)
	}
	s.connectionOtherConfigs[0].name = "Staging"
	s.connectionDraft.Host = "staging.example.test"
	s.connectionPort = "3307"
	tt.Frame()
	if err := tt.RightClick("切换配置文件：Staging"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("设置为活动配置文件"); err != nil {
		t.Fatal(err)
	}
	if s.connectionActiveTarget != 1 || !tt.HasText("Staging (Active)") ||
		tt.HasText("主要配置文件 (Active)") {
		t.Fatal("Active marker did not move to staging")
	}
	if err := tt.Click("切换配置文件：主要配置文件"); err != nil {
		t.Fatal(err)
	}
	if s.connectionActiveConfig != 0 || s.connectionActiveTarget != 1 {
		t.Fatal("highlighting main unexpectedly changed Active target")
	}
	got, err := service.ActiveSettings(context.Background(), id)
	if err != nil || got.Host != "localhost" {
		t.Fatal("draft Active state leaked to storage")
	}
	s.closeConnectionDialog()
	s.startEditProfile()
	tt.Frame()
	if !tt.HasText("主要配置文件 (Active)") || len(s.connectionOtherConfigs) != 0 {
		t.Fatal("cancel failed to discard draft Active state")
	}
	if err := tt.Click("添加连接配置文件"); err != nil {
		t.Fatal(err)
	}
	s.connectionOtherConfigs[0].name = "Replica"
	s.connectionDraft.Host = "replica.example.test"
	tt.Frame()
	if err := tt.RightClick("切换配置文件：Replica"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("设置为活动配置文件"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("保存连接"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDialog {
		t.Fatalf("Active selection not saved: %s", s.connectionError)
	}
	p, err := service.Get(context.Background(), id)
	if err != nil || p.ActiveVariant != "Replica" {
		t.Fatalf("Active save: %+v %v", p, err)
	}
	resolved, err := service.ActiveSettings(context.Background(), id)
	if err != nil || resolved.Host != "replica.example.test" ||
		resolved.ConfigurationName != "Replica" {
		t.Fatalf("resolver mismatch: %+v %v", resolved, err)
	}
	s.startEditProfile()
	tt.Frame()
	if s.connectionActiveTarget != 1 || !tt.HasText("Replica (Active)") {
		t.Fatal("saved Active selection not restored on reopen")
	}
	// Selection and Active stay independent across keyboard-accessible buttons.
	if err := tt.Click("切换配置文件：主要配置文件"); err != nil {
		t.Fatal(err)
	}
	if s.connectionActiveTarget != 1 || s.connectionActiveConfig != 0 {
		t.Fatal("selection changed the persisted Active target")
	}
}

func TestMySQLActiveRenameDuplicateAndDelete(t *testing.T) {
	s, tt, service, id := editMySQLProfileTester(t)
	s.addConnectionConfiguration()
	s.connectionOtherConfigs[0].name = "A"
	s.markConnectionConfigurationActive(1)
	s.connectionOtherConfigs[0].name = "Renamed"
	s.duplicateConnectionConfiguration(1)
	if s.connectionActiveTarget != 1 || len(s.connectionOtherConfigs) != 2 {
		t.Fatal("duplicating a configuration changed Active selection")
	}
	s.removeConnectionConfiguration(2)
	if s.connectionActiveTarget != 1 {
		t.Fatal("deleting a non-active configuration changed Active")
	}
	s.addConnectionConfiguration()
	s.removeConnectionConfiguration(1)
	if s.connectionActiveTarget != 0 {
		t.Fatal("deleting the Active configuration did not select primary")
	}
	tt.Frame()
	if !tt.HasText("主要配置文件 (Active)") {
		t.Fatal("primary was not relabeled Active")
	}
	s.saveConnectionDraft()
	p, err := service.Get(context.Background(), id)
	if err != nil || p.ActiveVariant != "" || len(p.Variants) != 1 {
		t.Fatalf("delete did not persist fallback: %+v %v", p, err)
	}
	// Recreate one named Active and verify rename propagates on confirmation.
	s.startEditProfile()
	s.markConnectionConfigurationActive(1)
	s.connectionOtherConfigs[0].name = "Renamed-again"
	s.saveConnectionDraft()
	p, err = service.Get(context.Background(), id)
	if err != nil || p.ActiveVariant != "Renamed-again" ||
		!strings.EqualFold(p.Variants[0].Name, p.ActiveVariant) {
		t.Fatalf("renaming an Active configuration left a dangling reference: %+v %v", p, err)
	}
}

func TestMySQLActiveInvalidSaveIsAtomic(t *testing.T) {
	s, _, service, id := editMySQLProfileTester(t)
	s.addConnectionConfiguration()
	s.markConnectionConfigurationActive(1)
	s.connectionPort = "invalid"
	s.saveConnectionDraft()
	if !s.connectionDialog || s.connectionError == "" {
		t.Fatal("invalid Active config closed window")
	}
	p, err := service.Get(context.Background(), id)
	if err != nil || p.ActiveVariant != "" || len(p.Variants) != 0 {
		t.Fatalf("failed save modified persisted selection: %+v %v", p, err)
	}
	s.ConnectionWindowClosed()
	got, err := service.ActiveSettings(context.Background(), id)
	if err != nil || got.ConfigurationName != "" {
		t.Fatalf("window close changed Active: %+v %v", got, err)
	}
}
