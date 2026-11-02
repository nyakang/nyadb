package localdb

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/queries"
)

func historyFixture(t *testing.T) (*SQLRepository, *workspaceTestKey) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "config.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewSQLRepository(db), &workspaceTestKey{key: bytes.Repeat([]byte{3}, 32)}
}
func historyEntry(id string, profile int64, at time.Time) queries.HistoryEntry {
	return queries.HistoryEntry{ID: id, ProfileID: profile, Database: "app", Source: "query", SQL: "SELECT 'history-private-value'", State: "success", Started: at}
}
func TestSQLHistoryEncryptedRestartProfileClearAndLateExecution(t *testing.T) {
	repo, keys := historyFixture(t)
	store, ctx, now := queries.NewStore(repo, keys), context.Background(), time.Now()
	h := queries.DefaultHistory()
	appendEntry := func(entry queries.HistoryEntry, token queries.HistoryToken) {
		t.Helper()
		var err error
		h, err = store.ApplyHistory(ctx, h.Revision, queries.HistoryChange{Kind: "append", Entry: entry, Token: token}, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	oldA, oldB := h.Token(7), h.Token(8)
	appendEntry(historyEntry("a", 7, now), oldA)
	appendEntry(historyEntry("b", 8, now.Add(time.Second)), oldB)
	records, err := repo.List(ctx, "history", 0)
	if err != nil || len(records) != 1 || bytes.Contains(records[0].Payload, []byte("history-private-value")) || records[0].ProfileID != 0 {
		t.Fatal("history was not encrypted as a global record")
	}
	h, err = queries.NewStore(repo, keys).LoadHistory(ctx, now)
	if err != nil || len(h.Entries) != 2 || h.Entries[0].ID != "b" {
		t.Fatalf("history restart failed: %v", err)
	}
	h, err = store.ApplyHistory(ctx, h.Revision, queries.HistoryChange{Kind: "clear", ProfileID: 7}, now)
	if err != nil || len(h.Entries) != 1 || h.Entries[0].ProfileID != 8 {
		t.Fatal("profile clear affected another connection")
	}
	if _, err = store.ApplyHistory(ctx, h.Revision, queries.HistoryChange{Kind: "append", Entry: historyEntry("late-a", 7, now), Token: oldA}, now); !errors.Is(err, queries.ErrHistoryChanged) {
		t.Fatalf("late execution recreated cleared history: %v", err)
	}
	appendEntry(historyEntry("still-b", 8, now), oldB)
	appendEntry(historyEntry("new-a", 7, now), h.Token(7))
	h, err = store.ApplyHistory(ctx, h.Revision, queries.HistoryChange{Kind: "clear"}, now)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := queries.NewStore(repo, keys).LoadHistory(ctx, now)
	if err != nil || len(restored.Entries) != 0 || restored.Token(8) == oldB {
		t.Fatal("clear fence did not survive restart")
	}
}
func TestSQLHistoryRetentionDisableAndReenableRevokeOldExecution(t *testing.T) {
	repo, keys := historyFixture(t)
	store, ctx, now := queries.NewStore(repo, keys), context.Background(), time.Now()
	h := queries.DefaultHistory()
	var err error
	for _, entry := range []queries.HistoryEntry{historyEntry("expired", 7, now.AddDate(0, 0, -31)), historyEntry("recent", 7, now)} {
		h, err = store.ApplyHistory(ctx, h.Revision, queries.HistoryChange{Kind: "append", Entry: entry, Token: h.Token(7)}, now.AddDate(0, 0, -2))
		if err != nil {
			t.Fatal(err)
		}
	}
	h, err = queries.NewStore(repo, keys).LoadHistory(ctx, now)
	if err != nil || len(h.Entries) != 1 || h.Entries[0].ID != "recent" {
		t.Fatal("expired history retained")
	}
	// Pruning must remove old SQL from ciphertext too, not just hide a row.
	persisted, err := queries.NewStore(repo, keys).LoadHistory(ctx, now.AddDate(0, 0, -2))
	if err != nil || len(persisted.Entries) != 1 {
		t.Fatal("retention only hid old records")
	}
	token := h.Token(7)
	settings := h.Settings
	settings.Enabled = false
	h, err = store.ApplyHistory(ctx, h.Revision, queries.HistoryChange{Kind: "settings", Settings: settings}, now)
	if err != nil {
		t.Fatal(err)
	}
	settings.Enabled = true
	h, err = store.ApplyHistory(ctx, h.Revision, queries.HistoryChange{Kind: "settings", Settings: settings}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApplyHistory(ctx, h.Revision, queries.HistoryChange{Kind: "append", Entry: historyEntry("late", 7, now), Token: token}, now); !errors.Is(err, queries.ErrHistoryChanged) {
		t.Fatal("disable/enable restored an old execution's recording permission")
	}
}
func TestSQLHistoryWrongOrMissingKeyCannotClearOrChangeSettings(t *testing.T) {
	repo, keys := historyFixture(t)
	ctx, now := context.Background(), time.Now()
	h := queries.DefaultHistory()
	h, err := queries.NewStore(repo, keys).ApplyHistory(ctx, 0, queries.HistoryChange{Kind: "append", Entry: historyEntry("private", 7, now), Token: h.Token(7)}, now)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := repo.List(ctx, "history", 0)
	for _, key := range []*workspaceTestKey{{}, {key: bytes.Repeat([]byte{4}, 32)}, {fail: true}} {
		bad := queries.NewStore(repo, key)
		if _, err = bad.ApplyHistory(ctx, h.Revision, queries.HistoryChange{Kind: "clear"}, now); err == nil {
			t.Fatal("unavailable/wrong key cleared history")
		}
		if _, err = bad.ApplyHistory(ctx, h.Revision, queries.HistoryChange{Kind: "settings", Settings: h.Settings}, now); err == nil {
			t.Fatal("unavailable/wrong key replaced settings")
		}
		after, _ := repo.List(ctx, "history", 0)
		if !bytes.Equal(before[0].Payload, after[0].Payload) || before[0].Revision != after[0].Revision {
			t.Fatal("original ciphertext changed")
		}
	}
}
func TestSQLHistoryConcurrentStoresPublishOnlyOneRevision(t *testing.T) {
	repo, keys := historyFixture(t)
	ctx, now, start := context.Background(), time.Now(), make(chan struct{})
	out := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		go func(id string) {
			<-start
			h := queries.DefaultHistory()
			_, err := queries.NewStore(repo, keys).ApplyHistory(ctx, 0, queries.HistoryChange{Kind: "append", Entry: historyEntry(id, 7, now), Token: h.Token(7)}, now)
			out <- err
		}(id)
	}
	close(start)
	succeeded, stale := 0, 0
	for range 2 {
		err := <-out
		if err == nil {
			succeeded++
		} else if errors.Is(err, queries.ErrStaleRevision) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || stale != 1 {
		t.Fatal("conditional history publication was not atomic")
	}
}
