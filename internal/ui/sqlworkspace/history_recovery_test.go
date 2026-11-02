package sqlworkspace

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

type historyOutageRepository struct {
	*memoryQueries
	fail          atomic.Bool
	gate, started chan struct{}
	once          atomic.Bool
}

func (r *historyOutageRepository) List(ctx context.Context, kind string, profile int64) ([]queries.Record, error) {
	if kind == "history" && r.gate != nil && r.once.CompareAndSwap(false, true) {
		close(r.started)
		<-r.gate
	}
	return r.memoryQueries.List(ctx, kind, profile)
}
func (r *historyOutageRepository) Put(ctx context.Context, v queries.Record) error {
	if v.Kind == "history" && r.fail.Load() {
		return errors.New("fixture history storage unavailable")
	}
	return r.memoryQueries.Put(ctx, v)
}
func waitHistoryError(t *testing.T, s *State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.Consume()
		if s.history.err != "" && s.history.pending == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("history failure did not settle")
}
func TestSQLHistoryFirstExecutionWaitsForInitialTokensAndRespectsDisabledSetting(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			_, base := documentStore()
			keys := &memoryWorkspaceKey{}
			seed := queries.NewStore(base, keys)
			h := queries.DefaultHistory()
			h.Settings.Enabled = enabled
			if _, err := seed.ApplyHistory(context.Background(), 0, queries.HistoryChange{Kind: "settings", Settings: h.Settings}, time.Now()); err != nil {
				t.Fatal(err)
			}
			repo := &historyOutageRepository{memoryQueries: base, gate: make(chan struct{}), started: make(chan struct{})}
			s := New(Dependencies{Connected: func(int64) bool { return true }})
			t.Cleanup(s.CloseAll)
			s.SetPersistence(queries.NewStore(repo, keys), Files{}, nil)
			awaitSignal(t, repo.started)
			d := s.Open(42, "SELECT first", 7, "original", false)
			s.Executor = func(context.Context, int64, string, string) (contract.SQLResult, error) {
				return contract.SQLResult{}, nil
			}
			s.Run(nil, d)
			if len(s.history.receipts) != 1 {
				t.Fatal("execution before loading was silently dropped")
			}
			close(repo.gate)
			waitHistory(t, s, true)
			if got := len(s.HistorySnapshot().Entries); enabled && got != 1 || !enabled && got != 0 {
				t.Fatalf("startup setting ignored: enabled=%v entries=%d", enabled, got)
			}
		})
	}
}
func TestSQLHistoryOutageBoundsMemoryRetriesAndExplicitDiscardRevokesReceipts(t *testing.T) {
	_, base := documentStore()
	repo := &historyOutageRepository{memoryQueries: base}
	store := queries.NewStore(repo, &memoryWorkspaceKey{})
	s := New(Dependencies{})
	t.Cleanup(s.CloseAll)
	s.SetPersistence(store, Files{}, nil)
	waitHistory(t, s, false)
	request := contract.ActivityRequest{ProfileID: 7, Generation: 3, Database: "original", Source: "data_write", SQL: "UPDATE sample SET secret = ?", Summary: true}
	complete := s.BeginActivity(request)
	complete(contract.ActivityResult{State: "success"})
	waitHistory(t, s, true)
	original := s.HistorySnapshot()
	repo.fail.Store(true)
	late := s.BeginActivity(request)
	for range 100 {
		request.SQL = strings.Repeat("x", 512<<10)
		if complete := s.BeginActivity(request); complete != nil {
			complete(contract.ActivityResult{State: "success"})
		}
		s.Consume()
	}
	waitHistoryError(t, s)
	count, bytes := s.historyBufferedSize()
	if bytes > queries.HistoryMaxBytes || count > historyPendingLimit || s.history.dropped == 0 || len(s.history.queue) == 0 {
		t.Fatalf("unbounded/absent pending records: %d bytes %d records dropped %d", bytes, count, s.history.dropped)
	}
	if len(s.HistorySnapshot().Entries) != len(original.Entries) {
		t.Fatal("failed writes replaced the original history")
	}
	repo.fail.Store(false)
	s.loadHistory()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		s.Consume()
		if s.history.err != "" && !s.history.loading {
			t.Fatal(s.history.err)
		}
		if s.history.loaded && !s.HistoryPending() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if s.HistoryPending() {
		t.Fatal("bounded backlog did not settle after recovery")
	}
	if len(s.HistorySnapshot().Entries) <= 1 {
		t.Fatal("retry did not retain valid pending operations")
	}
	// The running original ticket is revoked on explicit discard, even if its
	// driver acknowledges after a later recording has started.
	if !s.DiscardPendingHistory() {
		t.Fatal("settled queue could not be discarded")
	}
	late(contract.ActivityResult{State: "cancelled"})
	s.Consume()
	if s.HistoryPending() || len(s.history.receipts) != 0 {
		t.Fatal("discarded receipt recreated the queue")
	}
}
func TestSQLHistoryInvalidOversizeReceiptDoesNotBlockNextValidExecution(t *testing.T) {
	s, _ := historyTestState(t)
	d := s.Open(2, "SELECT normal", 7, "app", false)
	file := s.beginHistory(d, "file", "")
	completeHistory(file, contract.SQLResult{Steps: []contract.StatementOutcome{{SQL: strings.Repeat("x", (8<<20)+1), State: "executed"}, {SQL: "SELECT valid", State: "executed"}}}, nil, true)
	waitHistory(t, s, true)
	if s.history.dropped != 1 || len(s.HistorySnapshot().Entries) != 1 || s.HistorySnapshot().Entries[0].SQL != "SELECT valid" {
		t.Fatal("invalid script step blocked valid history")
	}
}
func TestSQLHistoryFailedExitRequiresDecisionAndKeepsPersistedHistory(t *testing.T) {
	s, _ := historyTestState(t)
	d := s.Open(2, "SELECT kept", 7, "app", false)
	job := s.beginHistory(d, "query", "SELECT kept")
	completeHistory(job, contract.SQLResult{}, nil, true)
	waitHistory(t, s, true)
	late := s.BeginActivity(contract.ActivityRequest{ProfileID: 7, Source: "data_read", SQL: "SELECT pending", Summary: true})
	s.history.queue = append(s.history.queue, queries.HistoryChange{Kind: "append", Entry: queries.HistoryEntry{ID: "unsaved"}})
	s.history.err = "fixture unavailable"
	closed := 0
	if !s.GuardWorkspaceClose(func(ui.Services) { closed++ }) {
		t.Fatal("failed pending logs were silently dropped on exit")
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Modals(c) }, 700, 500)
	tt.Frame()
	if err := tt.Click("放弃日志并继续"); err != nil {
		t.Fatal(err)
	}
	late(contract.ActivityResult{State: "success"})
	s.Consume()
	if closed != 1 || s.HistoryPending() || len(s.HistorySnapshot().Entries) != 1 || d.Editor.Buffer.String() != "SELECT kept" {
		t.Fatal("explicit discard altered persisted history, SQL, or resurrected a late receipt")
	}
}

func TestGlobalActivityClearDisableAndSummaryOpenUseOriginalTokens(t *testing.T) {
	s, _ := historyTestState(t)
	request := contract.ActivityRequest{ProfileID: 7, Generation: 31, Database: "original", Object: "entries", Source: "data_write", SQL: "UPDATE entries SET value = ?", Summary: true}
	old := s.BeginActivity(request)
	s.ClearHistory(7)
	waitHistory(t, s, false)
	old(contract.ActivityResult{State: "success"})
	waitHistory(t, s, true)
	if len(s.HistorySnapshot().Entries) != 0 {
		t.Fatal("global data receipt recreated cleared history")
	}
	late := s.BeginActivity(request)
	settings := s.HistorySnapshot().Settings
	settings.Enabled = false
	s.SaveHistorySettings(settings)
	waitHistory(t, s, false)
	settings.Enabled = true
	s.SaveHistorySettings(settings)
	waitHistory(t, s, false)
	late(contract.ActivityResult{State: "success"})
	waitHistory(t, s, true)
	if len(s.HistorySnapshot().Entries) != 0 {
		t.Fatal("re-enable granted old operation new logging permission")
	}
	current := s.BeginActivity(request)
	current(contract.ActivityResult{State: "pending"})
	waitHistory(t, s, true)
	opened := 0
	s.OpenHistory(0, func(queries.HistoryEntry) { opened++ })
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Modals(c) }, 1100, 800)
	tt.Frame()
	if err := tt.Click(request.SQL); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnter)
	if err := tt.Click("打开历史 SQL"); err != nil {
		t.Fatal(err)
	}
	if opened != 0 || !s.history.open {
		t.Fatal("parameterized activity became an executable document")
	}
	s.history.browsers[0].source = "data_write"
	if entries := s.historyEntries(s.history.browsers[0]); len(entries) != 1 || entries[0].State != "pending" {
		t.Fatal("global source/outcome filter lost pending transaction")
	}
}

func TestHistoryReloadCannotApplyStaleClearToNewerRecords(t *testing.T) {
	s, store := historyTestState(t)
	h := s.HistorySnapshot()
	entry := queries.HistoryEntry{ID: "other-process", ProfileID: 7, Database: "new", SQL: "SELECT preserved", State: "success", Started: time.Now()}
	if _, err := store.ApplyHistory(context.Background(), h.Revision, queries.HistoryChange{Kind: "append", Entry: entry, Token: h.Token(7)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.ClearHistory(7)
	waitHistoryError(t, s)
	s.loadHistory()
	waitHistoryError(t, s)
	if !s.history.controlConflict || len(s.history.queue) != 1 || len(s.HistorySnapshot().Entries) != 1 {
		t.Fatal("reload overwrote a newer process's history")
	}
	s.loadHistory()
	waitHistoryError(t, s)
	if !s.history.controlConflict || !s.DiscardPendingHistory() {
		t.Fatal("conflict was automatically rebased on repeated reload")
	}
	s.loadHistory()
	waitHistory(t, s, false)
	if len(s.HistorySnapshot().Entries) != 1 {
		t.Fatal("discarding stale control cleared saved history")
	}
}
