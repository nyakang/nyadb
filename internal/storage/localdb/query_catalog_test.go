package localdb

import (
	"bytes"
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/services/queries"
	"path/filepath"
	"sync"
	"testing"
)

func TestQueryCatalogAtomicRevisionAndDeletionAcrossStores(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "config.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	key := &workspaceTestKey{}
	repo := NewSQLRepository(db)
	a := queries.NewStore(repo, key)
	n := queries.Named{ID: "q", Name: "Original", Text: "SELECT 'private-sql'", ProfileID: 7, Database: "app", Revision: 1}
	if e = a.SaveNamed(ctx, n); e != nil {
		t.Fatal(e)
	}
	b := queries.NewStore(repo, key)
	if _, e = b.RenameNamed(ctx, 8, n.ID, 1, "wrong"); !errors.Is(e, queries.ErrQueryScope) {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	out := make(chan error, 2)
	for _, store := range []*queries.Store{a, b} {
		wg.Add(1)
		go func(s *queries.Store) {
			defer wg.Done()
			<-start
			_, e := s.RenameNamed(ctx, 7, n.ID, 1, "Renamed")
			out <- e
		}(store)
	}
	close(start)
	wg.Wait()
	close(out)
	success := 0
	for e := range out {
		if e == nil {
			success++
		} else if !errors.Is(e, queries.ErrStaleRevision) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent updates: %d", success)
	}
	if e = b.DeleteNamedRevision(ctx, 7, n.ID, 1); !errors.Is(e, queries.ErrStaleRevision) {
		t.Fatal(e)
	}
	if e = b.DeleteNamedRevision(ctx, 7, n.ID, 2); e != nil {
		t.Fatal(e)
	}
	n.Revision = 999
	if e = a.SaveNamed(ctx, n); !errors.Is(e, queries.ErrQueryMissing) {
		t.Fatalf("deleted query resurrected: %v", e)
	}
	ns, e := a.ListNamed(ctx, 7)
	if e != nil || len(ns) != 0 {
		t.Fatalf("deleted query listed: %v %v", ns, e)
	}
	var kind string
	var raw []byte
	if e = db.QueryRow("SELECT kind,payload FROM sql_documents WHERE id='q'").Scan(&kind, &raw); e != nil || kind != "query_deleted" || bytes.Contains(raw, []byte("private-sql")) {
		t.Fatal("deletion did not replace original SQL")
	}
}
func TestQueryGroupsEncryptedRestartIsolationAndConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.sqlite")
	db, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	key := &workspaceTestKey{}
	store := queries.NewStore(NewSQLRepository(db), key)
	for _, n := range []queries.Named{{ID: "a", Name: "同名", Text: "SELECT 1", ProfileID: 7, Revision: 1}, {ID: "b", Name: "同名", Text: "SELECT 2", ProfileID: 8, Revision: 1}} {
		if e = store.SaveNamed(ctx, n); e != nil {
			t.Fatal(e)
		}
	}
	c := queries.Catalog{ProfileID: 7, Revision: 1, Groups: []queries.Group{{ID: "g", Name: "私有分组"}}, Membership: map[string]string{"a": "g"}}
	if e = store.SaveCatalog(ctx, c); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	store = queries.NewStore(NewSQLRepository(db), key)
	restored, e := store.ListCatalogs(ctx)
	if e != nil || len(restored) != 1 || restored[0].Membership["a"] != "g" || restored[0].Groups[0].Name != "私有分组" {
		t.Fatalf("catalog restore: %+v %v", restored, e)
	}
	if e = store.SaveCatalog(ctx, c); !errors.Is(e, queries.ErrStaleRevision) {
		t.Fatal(e)
	}
	c.Revision++
	c.Membership["b"] = "g"
	if e = store.SaveCatalog(ctx, c); !errors.Is(e, queries.ErrQueryScope) {
		t.Fatal(e)
	}
	delete(c.Membership, "b")
	var payload []byte
	if e = db.QueryRow("SELECT payload FROM sql_documents WHERE kind='query_catalog'").Scan(&payload); e != nil || bytes.Contains(payload, []byte("私有分组")) {
		t.Fatal("group name stored in plaintext")
	}
	lost := queries.NewStore(NewSQLRepository(db), &workspaceTestKey{})
	if _, e = lost.ListCatalogs(ctx); !errors.Is(e, queries.ErrKeyUnavailable) {
		t.Fatal(e)
	}
	payload[len(payload)-1] ^= 1
	if _, e = db.Exec("UPDATE sql_documents SET payload=? WHERE kind='query_catalog'", payload); e != nil {
		t.Fatal(e)
	}
	corrupt := queries.NewStore(NewSQLRepository(db), key)
	if e = corrupt.SaveCatalog(ctx, c); e == nil {
		t.Fatal("corrupt catalog overwritten")
	}
}
