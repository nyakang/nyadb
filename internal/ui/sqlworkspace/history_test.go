package sqlworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func historyTestState(t *testing.T) (*State, *queries.Store) {
	t.Helper()
	store, _ := documentStore()
	s := New(Dependencies{Connected: func(id int64) bool { return id == 7 || id == 8 }})
	t.Cleanup(s.CloseAll)
	s.SetPersistence(store, Files{}, nil)
	waitDocumentIO(t, s)
	waitHistory(t, s, false)
	return s, store
}
func waitHistory(t *testing.T, s *State, receipts bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.ConsumeDocuments(ui.Services{})
		s.Consume()
		if s.history.err != "" && !s.history.loading {
			t.Fatal(s.history.err)
		}
		if s.history.loaded && !s.HistoryPending() && (!receipts || len(s.history.receipts) == 0) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("history worker did not settle")
}
func TestSQLHistoryUsesCapturedScopeAndRecordsClosedTabReceipt(t *testing.T) {
	s, store := historyTestState(t)
	d := s.Open(2, "SELECT original", 7, "original_db", false)
	started, release := make(chan struct{}), make(chan struct{})
	s.Executor = func(ctx context.Context, id int64, db, sql string) (contract.SQLResult, error) {
		if id != 7 || db != "original_db" || sql != "SELECT original" {
			return contract.SQLResult{}, errors.New("changed original scope")
		}
		close(started)
		<-release
		return contract.SQLResult{}, ctx.Err()
	}
	s.Run(nil, d)
	awaitSignal(t, started)
	s.Close(d)
	s.Open(3, "SELECT other", 8, "other_db", false)
	close(release)
	waitHistory(t, s, true)
	h, err := store.LoadHistory(context.Background(), time.Now())
	if err != nil || len(h.Entries) != 1 || h.Entries[0].ProfileID != 7 || h.Entries[0].Database != "original_db" || h.Entries[0].State != "cancelled" {
		t.Fatalf("closed receipt changed scope/outcome: %+v %v", h, err)
	}
	if s.documents[3].Editor.Buffer.String() != "SELECT other" || s.documents[3].Error() != "" {
		t.Fatal("late receipt changed another document")
	}
}
func TestSQLHistoryClearRevokesOnlyOlderOriginalProfileExecutions(t *testing.T) {
	s, _ := historyTestState(t)
	a, b := s.Open(2, "SELECT a", 7, "a", false), s.Open(3, "SELECT b", 8, "b", false)
	started, release := make(chan struct{}, 2), make(chan struct{})
	s.Executor = func(context.Context, int64, string, string) (contract.SQLResult, error) {
		started <- struct{}{}
		<-release
		return contract.SQLResult{}, nil
	}
	s.Run(nil, a)
	s.Run(nil, b)
	awaitSignal(t, started)
	awaitSignal(t, started)
	s.ClearHistory(7)
	waitHistory(t, s, false)
	close(release)
	waitHistory(t, s, true)
	h := s.HistorySnapshot()
	if len(h.Entries) != 1 || h.Entries[0].ProfileID != 8 {
		t.Fatal("clear either resurrected old SQL or removed another profile's receipt")
	}
	s.Run(nil, a)
	waitHistory(t, s, true)
	if len(s.HistorySnapshot().Entries) != 2 {
		t.Fatal("post-clear execution was incorrectly revoked")
	}
}
func TestSQLHistoryFailureAndRetryPreserveRecordsAndDocument(t *testing.T) {
	s, store := historyTestState(t)
	d := s.Open(2, "SELECT 1", 7, "app", false)
	s.Executor = func(context.Context, int64, string, string) (contract.SQLResult, error) {
		return contract.SQLResult{}, errors.New("fixture execution failed")
	}
	s.Run(nil, d)
	waitHistory(t, s, true)
	old := s.HistorySnapshot()
	if len(old.Entries) != 1 || old.Entries[0].State != "error" {
		t.Fatal("failed execution not recorded")
	}
	// A different process's clear invalidates our revision and old ticket.
	if _, err := store.ApplyHistory(context.Background(), old.Revision, queries.HistoryChange{Kind: "clear"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Run(nil, d)
	deadline := time.Now().Add(time.Second)
	for s.history.err == "" && time.Now().Before(deadline) {
		s.Consume()
		time.Sleep(time.Millisecond)
	}
	if s.history.err == "" || len(s.history.snapshot.Entries) != 1 || d.Editor.Buffer.String() != "SELECT 1" {
		t.Fatal("revision conflict discarded original UI state")
	}
	s.loadHistory()
	waitHistory(t, s, true)
	if len(s.HistorySnapshot().Entries) != 0 {
		t.Fatal("reload silently overwrote another process's clear")
	}
}
func TestSQLHistoryUIOfflineSearchFiltersAndEnterOnlyOpenText(t *testing.T) {
	s, store := historyTestState(t)
	h := s.HistorySnapshot()
	for _, entry := range []queries.HistoryEntry{{ID: "a", ProfileID: 7, Database: "alpha", Source: "query", SQL: "SELECT alpha", State: "success", Started: time.Now()}, {ID: "b", ProfileID: 8, Database: "beta", Source: "query", SQL: "DELETE FROM beta", State: "error", Started: time.Now()}} {
		var err error
		h, err = store.ApplyHistory(context.Background(), h.Revision, queries.HistoryChange{Kind: "append", Entry: entry, Token: h.Token(entry.ProfileID)}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
	s.loadHistory()
	waitHistory(t, s, false)
	calls, opened := 0, queries.HistoryEntry{}
	s.Executor = func(context.Context, int64, string, string) (contract.SQLResult, error) {
		calls++
		return contract.SQLResult{}, nil
	}
	s.OpenHistory(0, func(entry queries.HistoryEntry) { opened = entry })
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(); s.Modals(c) }, 1100, 800)
	tt.Frame()
	if err := tt.Click("搜索历史 SQL、数据库或错误"); err != nil {
		t.Fatal(err)
	}
	tt.Type("alpha")
	if got := s.historyEntries(s.history.browsers[0]); len(got) != 1 || got[0].ID != "a" {
		t.Fatal("history search did not filter")
	}
	if err := tt.Click("SELECT alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnter)
	if opened.ID != "a" || opened.ProfileID != 7 || calls != 0 {
		t.Fatal("history opened current scope or executed SQL")
	}
	s.OpenHistory(8, nil)
	tt.Frame()
	if got := s.historyEntries(s.history.browsers[8]); len(got) != 1 || got[0].ID != "b" {
		t.Fatal("original connection filter not retained")
	}
	s.OpenHistory(0, nil)
	if s.history.browsers[0].search != "alpha" {
		t.Fatal("history scope search state lost")
	}
	s.CloseAll()
	tt.Frame()
	if s.history.open || s.history.openEntry != nil || s.history.pending != nil {
		t.Fatal("terminal close retained history callbacks")
	}
}

func TestHistoryConnectionNamesKeepStableIdentity(t *testing.T) {
	s := New(Dependencies{Profiles: func() []ProfileChoice {
		return []ProfileChoice{{ID: 1, Name: "Production"}, {ID: 2, Name: "Production"}}
	}})
	one, two := s.historyProfileLabel(1), s.historyProfileLabel(2)
	if one == two || !strings.Contains(one, "Production") || !strings.Contains(one, "#1") || !strings.Contains(two, "#2") {
		t.Fatalf("ambiguous history identity: %q %q", one, two)
	}
	if label := s.historyProfileLabel(3); !strings.Contains(label, "已删除连接") || !strings.Contains(label, "#3") {
		t.Fatalf("missing deleted profile label: %q", label)
	}
}
