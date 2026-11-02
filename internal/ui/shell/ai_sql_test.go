package shell

import (
	"context"
	"github.com/nyakang/nyadb/internal/services/aisql"
	"testing"
)

type emptyAIRepository struct{}

func (emptyAIRepository) Load(context.Context) (string, error)                 { return "", nil }
func (emptyAIRepository) CompareAndSwap(context.Context, string, string) error { return nil }
func TestAISQLToolMenusRejectSwitchedTabsAndGeneration(t *testing.T) {
	s, tt, _, _ := profileTestState(t)
	s.SetAISQL(aisql.NewStore(emptyAIRepository{}, nil), aisql.Client{})
	s.openQuery()
	tt.Frame()
	if e := tt.Click("工具"); e != nil {
		t.Fatal(e)
	}
	s.openQuery()
	tt.Frame()
	if e := tt.ChooseMenuItem("AI SQL 助手…"); e != nil {
		t.Fatal(e)
	}
	if s.sqlWorkspace.AIVisible() {
		t.Fatal("old AI menu targeted another tab")
	}
	if e := tt.Click("工具"); e != nil {
		t.Fatal(e)
	}
	s.sessions.Current().Generation++
	tt.Frame()
	if e := tt.ChooseMenuItem("AI SQL 助手…"); e != nil {
		t.Fatal(e)
	}
	if s.sqlWorkspace.AIVisible() {
		t.Fatal("old AI menu survived generation change")
	}
	if e := tt.Click("工具"); e != nil {
		t.Fatal(e)
	}
	if e := tt.ChooseMenuItem("AI SQL 助手…"); e != nil {
		t.Fatal(e)
	}
	if !s.sqlWorkspace.AIVisible() {
		t.Fatal("AI menu did not open current document")
	}
	s.closeMySQLSessions()
	if s.sqlWorkspace.AIVisible() {
		t.Fatal("terminal shell close retained AI requests")
	}
}
func TestAISQLNewDraftPreservesOriginalScopeWithoutExecuting(t *testing.T) {
	s, _, _, _ := profileTestState(t)
	s.openAIProposal(44, "original.namespace", "SELECT proposed;")
	d := s.currentTab().query
	if d == nil || d.Editor.Buffer.String() != "SELECT proposed;" || d.ProfileID() != 0 || d.RecoveryProfile() != 44 || d.RecoverySchema() != "original.namespace" || !d.NeedsRecovery() || d.Result() != nil || d.Loading() {
		t.Fatal("AI draft retargeted or executed proposed SQL")
	}
}
