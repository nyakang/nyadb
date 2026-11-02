package localdb

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/services/aisql"
	"path/filepath"
	"testing"
)

func TestAISQLPreferencesRestartAndConditionalPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.sqlite")
	db, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	r := NewAISQLRepository(db)
	ctx := context.Background()
	c := aisql.Defaults()
	c.Endpoint, c.Model, c.Anonymous = "https://example.invalid/v1", "configured-model", true
	s := aisql.NewStore(r, nil)
	before, e := s.Load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	saved, e := s.Save(ctx, before, c, "", false)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.CompareAndSwap(ctx, before.Raw, "{}"); !errors.Is(e, aisql.ErrConflict) {
		t.Fatal("stale settings overwrote current settings")
	}
	db.Close()
	db, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	loaded, e := aisql.NewStore(NewAISQLRepository(db), nil).Load(ctx)
	if e != nil || loaded.Raw != saved.Snapshot.Raw || loaded.Config.Model != "configured-model" {
		t.Fatal("settings did not recover without schema changes")
	}
}
