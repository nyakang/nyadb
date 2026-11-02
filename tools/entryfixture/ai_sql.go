package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/services/aisql"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/storage/localdb"
)

const originalAISQL = "SELECT 'original-ai-draft' AS marker;"
const proposedAISQL = "SELECT 'ai-native-fixture' AS marker;"

func seedAISQL(root string) {
	config := filepath.Join(root, "config/NyaDB/config.sqlite")
	_, e := os.Stat(config)
	require(os.IsNotExist(e), "AI fixture requires empty owned config")
	require(os.Getenv("NYADB_AI_NATIVE_OWNED") == "1" && len(os.Getenv("NYADB_AI_NATIVE_KEY")) == 64, "owned AI fixture required")
	db, e := localdb.Open(config)
	check(e)
	defer db.Close()
	ctx := context.Background()
	p, e := connections.Defaults(connections.SQLite)
	check(e)
	p.Name, p.Database = "AI SQLite 验收", filepath.Join(root, "ai-fixture.sqlite")
	p, e = localdb.NewProfileStore(db).Create(ctx, p)
	check(e)
	fixture, e := sql.Open("sqlite", p.Database)
	check(e)
	_, e = fixture.Exec("CREATE TABLE fixture(id INTEGER PRIMARY KEY,marker TEXT); INSERT INTO fixture VALUES(1,'fixture-table-value')")
	check(e)
	check(fixture.Close())
	store := aisql.NewStore(localdb.NewAISQLRepository(db), credentials.AISQLKeys{})
	before, e := store.Load(ctx)
	check(e)
	c := aisql.Defaults()
	c.Endpoint, c.Model = os.Getenv("NYADB_AI_NATIVE_ENDPOINT"), "owned-native-model"
	_, e = store.Save(ctx, before, c, os.Getenv("NYADB_AI_NATIVE_KEY"), false)
	check(e)
	q := queries.NewStore(localdb.NewSQLRepository(db), credentials.WorkspaceKey{})
	check(q.SaveNamed(ctx, queries.Named{ID: "ai-native-query", ProfileID: p.ID, Database: "main", Name: "AI SQL 验收", Text: originalAISQL, Revision: 1}))
	check(q.SaveWorkspace(ctx, queries.Workspace{ActiveID: 7, Revision: 1, Documents: []queries.Document{{ID: 7, QueryID: "ai-native-query", Name: "AI SQL 验收", ProfileID: p.ID, Database: "main", Text: originalAISQL, SavedText: originalAISQL, Revision: 1, NamedRevision: 1}}}))
	check(localdb.SetPreference(db, "appearance", "dark"))
}
func nativeAISQL(root string) {
	db, e := localdb.Open(filepath.Join(root, "config/NyaDB/config.sqlite"))
	check(e)
	defer db.Close()
	ctx := context.Background()
	store := aisql.NewStore(localdb.NewAISQLRepository(db), credentials.AISQLKeys{})
	c, e := store.Load(ctx)
	check(e)
	key, e := store.Key(c.Config)
	check(e)
	require(key == os.Getenv("NYADB_AI_NATIVE_KEY") && key != "", "real AI keyring recovery failed")
	q := queries.NewStore(localdb.NewSQLRepository(db), credentials.WorkspaceKey{})
	w, e := q.LoadWorkspace(ctx)
	check(e)
	found := false
	for _, d := range w.Documents {
		if d.QueryID == "ai-native-query" {
			found = d.ProfileID > 0 && d.Database == "main" && d.SavedText == originalAISQL && strings.HasPrefix(d.Text, proposedAISQL) && strings.Contains(d.Text, originalAISQL)
		}
	}
	require(found, "native AI insertion lost original draft or scope")
	names, e := q.ListNamed(ctx, 0)
	check(e)
	require(len(names) == 1 && names[0].Text == originalAISQL, "AI changed saved query without saving")
	h, e := q.LoadHistory(ctx, time.Now())
	check(e)
	require(len(h.Entries) == 0, "AI workflow executed SQL")
}

// Exercise the real dedicated keyring namespace in a fresh fixture process.
// Restore its original encrypted record even when a verification fails.
func nativeAIKeyFailures(root string) {
	db, e := localdb.Open(filepath.Join(root, "config/NyaDB/config.sqlite"))
	check(e)
	defer db.Close()
	ctx := context.Background()
	keys := credentials.AISQLKeys{}
	store := aisql.NewStore(localdb.NewAISQLRepository(db), keys)
	before, e := store.Load(ctx)
	check(e)
	secret, found, e := keys.Get(before.Config.KeyID)
	check(e)
	require(found, "original AI keyring record missing")
	original, e := store.Key(before.Config)
	check(e)
	require(original != "", "AI keyring did not recover its valid original key")
	defer func() { check(keys.Set(before.Config.KeyID, secret)) }()
	for _, phase := range []string{"missing", "wrong"} {
		if phase == "missing" {
			check(keys.Delete(before.Config.KeyID))
		} else {
			check(keys.Set(before.Config.KeyID, `{"origin":"https://wrong.invalid","key":"wrong-fixture-key"}`))
		}
		_, e = store.Key(before.Config)
		require(errors.Is(e, aisql.ErrKey), "unavailable AI key was accepted")
		after, e := store.Load(ctx)
		check(e)
		require(before.Raw == after.Raw, "key failure rewrote AI settings")
	}
}
