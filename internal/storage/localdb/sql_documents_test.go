package localdb

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/queries"
)

type workspaceTestKey struct {
	key  []byte
	fail bool
}

func (k *workspaceTestKey) Get() ([]byte, bool, error) {
	if k.fail {
		return nil, false, errors.New("vault locked")
	}
	return k.key, len(k.key) > 0, nil
}
func (k *workspaceTestKey) Set(v []byte) error { k.key = append([]byte(nil), v...); return nil }
func TestEncryptedSQLMigrationRoundTripRevisionAndMissingKey(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "config.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	repo := NewSQLRepository(db)
	key := &workspaceTestKey{}
	store := queries.NewStore(repo, key)
	w := queries.Workspace{Revision: 1, ActiveID: 7, Documents: []queries.Document{{ID: 7, ProfileID: 5, Text: "SELECT 'private-token'", SavedText: "", Cursor: 9, Revision: 2}}}
	if e = store.SaveWorkspace(ctx, w); e != nil {
		t.Fatal(e)
	}
	var payload []byte
	if e = db.QueryRow("SELECT payload FROM sql_documents").Scan(&payload); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(payload, []byte("private-token")) {
		t.Fatal("plaintext SQL persisted")
	}
	restored, e := queries.NewStore(repo, key).LoadWorkspace(ctx)
	if e != nil || restored.ActiveID != 7 || restored.Documents[0].Text != w.Documents[0].Text {
		t.Fatalf("round trip: %+v %v", restored, e)
	}
	if e = store.SaveWorkspace(ctx, w); !errors.Is(e, queries.ErrStaleRevision) {
		t.Fatalf("old revision accepted: %v", e)
	}
	lost := &workspaceTestKey{}
	if _, e = queries.NewStore(repo, lost).LoadWorkspace(ctx); !errors.Is(e, queries.ErrKeyUnavailable) {
		t.Fatalf("lost key: %v", e)
	}
	if len(lost.key) != 0 {
		t.Fatal("replaced missing key")
	}
	payload[len(payload)-1] ^= 1
	if _, e = db.Exec("UPDATE sql_documents SET payload=?", payload); e != nil {
		t.Fatal(e)
	}
	corruptStore := queries.NewStore(repo, key)
	if _, e = corruptStore.LoadWorkspace(ctx); e == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	w.Revision++
	if e = corruptStore.SaveWorkspace(ctx, w); e == nil {
		t.Fatal("unreadable record was overwritten")
	}
	var retained []byte
	if e = db.QueryRow("SELECT payload FROM sql_documents").Scan(&retained); e != nil || !bytes.Equal(retained, payload) {
		t.Fatal("original ciphertext not retained")
	}
}
