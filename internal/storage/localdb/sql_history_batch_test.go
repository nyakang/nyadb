package localdb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/queries"
)

func TestSQLHistoryBatchAtomicityMetadataAndClearFence(t *testing.T) {
	repo, keys := historyFixture(t)
	store, ctx, now := queries.NewStore(repo, keys), context.Background(), time.Now()
	h := queries.DefaultHistory()
	a, b := historyEntry("a", 7, now), historyEntry("b", 8, now)
	a.Summary, a.Object, a.Generation, a.Source = true, "entries", 42, "data_write"
	a.SQL = "UPDATE entries SET value = ?"
	changes := []queries.HistoryChange{{Kind: "append", Entry: a, Token: h.Token(7)}, {Kind: "append", Entry: b, Token: h.Token(8)}}
	var err error
	h, err = store.ApplyHistoryBatch(ctx, 0, changes, now)
	if err != nil || h.Revision != 1 || len(h.Entries) != 2 {
		t.Fatalf("batch publication failed: %+v %v", h, err)
	}
	h, err = queries.NewStore(repo, keys).LoadHistory(ctx, now)
	var restored queries.HistoryEntry
	for _, entry := range h.Entries {
		if entry.ID == "a" {
			restored = entry
		}
	}
	if err != nil || !restored.Summary || restored.Generation != 42 || restored.Object != "entries" {
		t.Fatal("operation metadata lost on restart")
	}
	before, _ := repo.List(ctx, "history", 0)
	valid, invalid := historyEntry("c", 7, now), historyEntry("bad", 7, now)
	invalid.State = "invalid"
	if _, err = store.ApplyHistoryBatch(ctx, h.Revision, []queries.HistoryChange{{Kind: "append", Entry: valid, Token: h.Token(7)}, {Kind: "append", Entry: invalid, Token: h.Token(7)}}, now); !errors.Is(err, queries.ErrInvalidHistoryEntry) {
		t.Fatalf("invalid batch accepted: %v", err)
	}
	after, _ := repo.List(ctx, "history", 0)
	if before[0].Revision != after[0].Revision || !bytes.Equal(before[0].Payload, after[0].Payload) {
		t.Fatal("invalid batch partially replaced ciphertext")
	}
	if _, err = store.ApplyHistoryBatch(ctx, h.Revision, []queries.HistoryChange{{Kind: "clear", ProfileID: 7}, {Kind: "append", Entry: valid, Token: h.Token(7)}}, now); !errors.Is(err, queries.ErrHistoryChanged) {
		t.Fatal("ordered clear did not revoke old append")
	}
	after, _ = repo.List(ctx, "history", 0)
	if !bytes.Equal(before[0].Payload, after[0].Payload) {
		t.Fatal("revoked batch partially cleared the original")
	}
}

// Measures real encrypted SQLite publication near the 32 MiB retained bound,
// with 64 new executions per publication rather than one full rewrite per row.
func BenchmarkSQLHistoryFullSnapshotBatch(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "config.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	store := queries.NewStore(NewSQLRepository(db), &workspaceTestKey{key: bytes.Repeat([]byte{7}, 32)})
	ctx, now := context.Background(), time.Now()
	h := queries.DefaultHistory()
	for batch := range 4 {
		changes := make([]queries.HistoryChange, 250)
		for i := range changes {
			e := historyEntry(fmt.Sprintf("seed-%d-%d", batch, i), 7, now)
			e.SQL = "SELECT '" + strings.Repeat("s", 32<<10) + "'"
			changes[i] = queries.HistoryChange{Kind: "append", Entry: e, Token: h.Token(7)}
		}
		h, err = store.ApplyHistoryBatch(ctx, h.Revision, changes, now)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := range b.N {
		changes := make([]queries.HistoryChange, 64)
		for i := range changes {
			e := historyEntry(fmt.Sprintf("bench-%d-%d", n, i), 7, now.Add(time.Second))
			changes[i] = queries.HistoryChange{Kind: "append", Entry: e, Token: h.Token(7)}
		}
		h, err = store.ApplyHistoryBatch(ctx, h.Revision, changes, now)
		if err != nil {
			b.Fatal(err)
		}
	}
}
