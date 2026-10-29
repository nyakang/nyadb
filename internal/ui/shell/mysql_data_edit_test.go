package shell

import (
	"context"
	"errors"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestMySQLDataDisconnectAndWorkbenchCloseWaitForDraftDecision(t *testing.T) {
	for _, action := range []string{"disconnect", "workbench"} {
		t.Run(action, func(t *testing.T) {
			s := New()
			page := editableHostPage()
			doc := fixtureDataTab(t, s, page)
			tt := ui.NewTester(s.View, 1000, 650)
			tt.Frame()
			if err := tt.Click("批量编辑"); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click("alpha"); err != nil {
				t.Fatal(err)
			}
			tt.Type("pending close")
			tt.Key(0, ui.KeyEnter)
			closed := false
			if action == "disconnect" {
				s.disconnectMySQLProfile(19)
			} else if !s.RequestWorkbenchClose(func() { closed = true }) {
				t.Fatal("workbench close was not vetoed")
			}
			tt.Frame()
			if !tt.HasText("待处理记录") || s.sessions.Current().Status != "已连接" || len(s.tabs) != 2 || closed {
				t.Fatal("close disconnected or destroyed draft before decision")
			}
			if err := tt.Click("取消"); err != nil {
				t.Fatal(err)
			}
			if !doc.data.HasDraft() || closed || s.sessions.Current().Status != "已连接" {
				t.Fatal("cancel close lost draft/session")
			}
		})
	}
}

func TestSchemaPruneKeepsDraftUntilDecisionAndSaveFailure(t *testing.T) {
	s := New()
	doc := fixtureDataTab(t, s, editableHostPage())
	s.openQuery()
	sqlID := s.currentTab().id
	s.activeTab = 1
	tt := ui.NewTester(s.View, 1000, 650)
	if err := tt.Click("批量编辑"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Type("pending schema removal")
	tt.Key(0, ui.KeyEnter)
	prune := func() { s.pruneMySQLTabsFor(19, map[string]bool{}) }
	prune()
	tt.Frame()
	if !tt.HasText("待处理记录") || len(s.tabs) != 3 {
		t.Fatal("schema pruning discarded an undecided draft")
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if !doc.data.HasDraft() || len(s.tabs) != 3 {
		t.Fatal("cancel schema pruning lost draft")
	}
	s.dataWorkspace.Saver = func(context.Context, int64, string, string, []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		return nil, errors.New("save failed")
	}
	prune()
	tt.Frame()
	tt.Key(ui.Shift, ui.KeyTab)
	tt.Key(ui.Shift, ui.KeyTab)
	tt.Key(0, ui.KeyEnter)
	deadline := time.Now().Add(3 * time.Second)
	for doc.data.Loading() || !tt.HasText("save failed") {
		if time.Now().After(deadline) {
			t.Fatalf("save failure did not settle: texts=%v active=%d loading=%v", tt.Texts(), s.activeTab, doc.data.Loading())
		}
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if len(s.tabs) != 3 || !doc.data.HasDraft() {
		t.Fatal("save failure pruned document")
	}
	prune()
	tt.Frame()
	tt.Key(ui.Shift, ui.KeyTab)
	tt.Key(0, ui.KeyEnter)
	if len(s.tabs) != 2 || s.tabs[1].id != sqlID {
		t.Fatal("pruning did not preserve sibling SQL identity")
	}
}

func stageHostDraft(t *testing.T, tt *ui.Tester, value string) {
	t.Helper()
	if err := tt.Click("批量编辑"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Type(value)
	tt.Key(0, ui.KeyEnter)
}

func chooseHostDraftDecision(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if !tt.HasText("待处理记录") {
		t.Fatal("draft decision dialog is not open")
	}
	// Successive dialogs can retain focus; select by the real focused label
	// instead of assuming the default or clicking an identically named footer.
	for i := 0; i < 4 && !tt.Focused(label); i++ {
		tt.Key(ui.Shift, ui.KeyTab)
	}
	if !tt.Focused(label) {
		t.Fatalf("could not focus draft decision %q", label)
	}
	tt.Key(0, ui.KeyEnter)
}

func TestClosingInactiveDraftTabDefersCompletionAndKeepsActiveIdentity(t *testing.T) {
	for _, decision := range []string{"cancel", "save failure", "save success"} {
		t.Run(decision, func(t *testing.T) {
			s := New()
			t.Cleanup(s.CloseMySQLSessions)
			doc := fixtureDataTab(t, s, editableHostPage())
			tt := ui.NewTester(s.View, 1000, 650)
			stageHostDraft(t, tt, "pending inactive close")
			s.openQuery()
			s.openQuery() // The original active tab differs from the closing tab's next sibling.
			activeID, status := s.currentTab().id, s.status
			tabsBefore := len(s.tabs)
			tt.Frame()
			s.dataWorkspace.Saver = func(_ context.Context, _ int64, _, _ string, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
				if decision == "save failure" {
					return nil, errors.New("save failed")
				}
				result := &mysqlsvc.MySQLDataEditResult{}
				for _, edit := range edits {
					result.Outcomes = append(result.Outcomes, mysqlsvc.MySQLDataEditOutcome{Kind: edit.Kind, Original: edit.Original, Saved: edit.Original})
				}
				return result, nil
			}
			s.closeTab(1)
			tt.Frame()
			if len(s.tabs) != tabsBefore || s.currentTab().id != activeID || s.status != status || !doc.data.HasDraft() {
				t.Fatal("close changed documents/selection/status before the draft decision")
			}
			if decision == "cancel" {
				chooseHostDraftDecision(t, tt, "取消")
				if len(s.tabs) != tabsBefore || s.currentTab().id != activeID || s.status != status || !doc.data.HasDraft() {
					t.Fatal("cancel ran close completion")
				}
				return
			}
			chooseHostDraftDecision(t, tt, "应用更改")
			deadline := time.Now().Add(3 * time.Second)
			for doc.data.Loading() && time.Now().Before(deadline) {
				tt.Frame()
				time.Sleep(time.Millisecond)
			}
			tt.Frame()
			if doc.data.Loading() {
				t.Fatal("save did not settle")
			}
			if decision == "save failure" {
				if len(s.tabs) != tabsBefore || !doc.data.HasDraft() || !tt.HasText("save failed") || s.status == "已关闭页签 "+doc.title {
					t.Fatal("failed save ran close completion or lost the draft")
				}
			} else if len(s.tabs) != tabsBefore-1 || s.currentTab().id != activeID || doc.data.Page() != nil || s.status != "已关闭页签 "+doc.title {
				t.Fatal("successful close did not dispose the draft and restore the original active SQL tab")
			}
		})
	}
}

func TestCloseOthersRechecksEachDraftBeforeRemovingTabs(t *testing.T) {
	s := New()
	t.Cleanup(s.CloseMySQLSessions)
	first := fixtureDataTab(t, s, editableHostPage())
	tt := ui.NewTester(s.View, 1000, 650)
	stageHostDraft(t, tt, "first draft")
	s.dataWorkspace.Loader = func(_ context.Context, _ int64, database, table string, _ int64, _ int) (*mysqlsvc.MySQLDataPage, error) {
		page := editableHostPage()
		page.Database, page.Table = database, table
		return page, nil
	}
	s.openMySQLData(ui.Services{}, "edit_test", "other_records")
	waitMySQLData(t, s, tt)
	second := s.currentTab()
	stageHostDraft(t, tt, "second draft")
	s.openQuery()
	keepID := s.currentTab().id
	tt.Frame()
	s.closeOtherTabs(3)
	tt.Frame()
	// Discarding the first draft must open the next decision, not remove both.
	chooseHostDraftDecision(t, tt, "放弃")
	tt.Frame()
	if len(s.tabs) != 4 || first.data.HasDraft() || !second.data.HasDraft() || !tt.HasText("待处理记录") {
		t.Fatal("close others skipped a remaining draft or removed tabs before all decisions")
	}
	chooseHostDraftDecision(t, tt, "取消")
	if len(s.tabs) != 4 || !second.data.HasDraft() || s.currentTab().id != keepID {
		t.Fatalf("canceling the second decision changed state: tabs=%d draft=%v active=%d keep=%d", len(s.tabs), second.data.HasDraft(), s.currentTab().id, keepID)
	}
	s.closeOtherTabs(3)
	tt.Frame()
	chooseHostDraftDecision(t, tt, "放弃")
	tt.Frame()
	if len(s.tabs) != 2 || s.currentTab().id != keepID || first.data.Page() != nil || second.data.Page() != nil {
		t.Fatalf("close others did not finish: tabs=%d active=%d keep=%d draft=%v modal=%v", len(s.tabs), s.currentTab().id, keepID, second.data.HasDraft(), tt.HasText("待处理记录"))
	}
}
