package localdb

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/queries"
)

func TestNativeSQLReceiptsRoundTripEncryptedHistoryAndLegacyEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipts.sqlite")
	db, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	key := &workspaceTestKey{}
	store := queries.NewStore(NewSQLRepository(db), key)
	ctx := context.Background()
	now := time.Now()
	h, e := store.LoadHistory(ctx, now)
	if e != nil {
		t.Fatal(e)
	}
	original := `{"State":"uncertain","SQL":"INSERT INTO original VALUES(1)","Dispatched":true,"Acknowledged":false,"Error":"` + strings.Repeat("完整原始错误", 180) + `"}`
	for _, entry := range []queries.HistoryEntry{{ID: "legacy", ProfileID: 73, SQL: "SELECT original", State: "success", Started: now}, {ID: "native", ProfileID: 73, SQL: "INSERT INTO original VALUES(1)", State: "uncertain", AffectedRows: -1, NativeJSON: original, Started: now}} {
		h, e = store.ApplyHistory(ctx, h.Revision, queries.HistoryChange{Kind: "append", Entry: entry, Token: h.Token(73)}, now)
		if e != nil {
			t.Fatal(e)
		}
	}
	var payload []byte
	if e = db.QueryRow("SELECT payload FROM sql_documents WHERE id='sql-history'").Scan(&payload); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(payload, []byte("INSERT INTO original")) || bytes.Contains(payload, []byte("NativeJSON")) || bytes.Contains(payload, []byte("完整原始错误")) {
		t.Fatal("receipt stored in plaintext")
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	restored, e := queries.NewStore(NewSQLRepository(db), key).LoadHistory(ctx, now)
	if e != nil || len(restored.Entries) != 2 {
		t.Fatal(restored, e)
	}
	for _, entry := range restored.Entries {
		if entry.ID == "legacy" && entry.NativeJSON != "" {
			t.Fatal("legacy history changed")
		}
		if entry.ID == "native" && (entry.NativeJSON != original || entry.AffectedRows != -1 || entry.State != "uncertain") {
			t.Fatal("original native receipt changed")
		}
	}
	with := queries.HistoryEntry{NativeJSON: original}
	without := with
	without.NativeJSON = ""
	if queries.HistoryEntrySize(with)-queries.HistoryEntrySize(without) != len(original) {
		t.Fatal("receipt bypassed history retention budget")
	}
}
