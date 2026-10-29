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
