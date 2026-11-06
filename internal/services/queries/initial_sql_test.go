package queries

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type initialRepository struct{ records map[string]Record }

func (r *initialRepository) List(_ context.Context, kind string, profile int64) ([]Record, error) {
	var out []Record
	for _, v := range r.records {
		if v.Kind == kind && (profile == 0 || profile == v.ProfileID) {
			v.Payload = bytes.Clone(v.Payload)
			out = append(out, v)
		}
	}
	return out, nil
}
func (r *initialRepository) Put(_ context.Context, v Record) error {
	v.Payload = bytes.Clone(v.Payload)
	r.records[v.ID] = v
	return nil
}
func (r *initialRepository) Delete(_ context.Context, id string) error {
	delete(r.records, id)
	return nil
}
func (r *initialRepository) DeleteInitialSQL(_ context.Context, id string) error {
	if r.records[id].Kind == "initial_sql" {
		delete(r.records, id)
	}
	return nil
}
func (r *initialRepository) HasRecords(context.Context) (bool, error) { return len(r.records) > 0, nil }

type initialKeys struct {
	key    []byte
	writes int
}

func (k *initialKeys) Get() ([]byte, bool, error) { return bytes.Clone(k.key), len(k.key) > 0, nil }
func (k *initialKeys) Set(b []byte) error         { k.key = bytes.Clone(b); k.writes++; return nil }
func TestInitialSQLCiphertextAndIdentityAuthentication(t *testing.T) {
	ctx := context.Background()
	repo := &initialRepository{records: map[string]Record{}}
	keys := &initialKeys{}
	store := NewStore(repo, keys)
	v := InitialSQL{ID: "opaque-initial-id", ProfileID: 8, Revision: 1, Text: "SET @secret='private-token'"}
	if e := store.SaveInitialSQL(ctx, v); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(repo.records[v.ID].Payload, []byte("private-token")) {
		t.Fatal("plaintext reached persistence")
	}
	got, e := NewStore(repo, keys).LoadInitialSQL(ctx, v.ID)
	if e != nil || got != v {
		t.Fatalf("encrypted restore failed: %+v %v", got, e)
	}
	record := repo.records[v.ID]
	record.ProfileID++
	repo.records[v.ID] = record
	tampered := NewStore(repo, keys)
	if _, e = tampered.LoadInitialSQL(ctx, v.ID); e == nil {
		t.Fatal("identity tampering must fail authentication")
	}
	v.ID = "new-id"
	if e = tampered.SaveInitialSQL(ctx, v); e == nil {
		t.Fatal("a store with unreadable ciphertext must refuse overwrites")
	}
}
func TestInitialSQLMissingKeyNeverReplacesExistingKey(t *testing.T) {
	ctx := context.Background()
	repo := &initialRepository{records: map[string]Record{}}
	keys := &initialKeys{}
	v := InitialSQL{ID: "initial-id", ProfileID: 9, Revision: 1, Text: "SET @v=1"}
	if e := NewStore(repo, keys).SaveInitialSQL(ctx, v); e != nil {
		t.Fatal(e)
	}
	before := bytes.Clone(repo.records[v.ID].Payload)
	missing := &initialKeys{}
	store := NewStore(repo, missing)
	if _, e := store.LoadInitialSQL(ctx, v.ID); e == nil {
		t.Fatal("missing key must fail")
	}
	v.Revision++
	if e := store.SaveInitialSQL(ctx, v); e == nil {
		t.Fatal("missing-key save must fail")
	}
	if missing.writes != 0 || !bytes.Equal(before, repo.records[v.ID].Payload) {
		t.Fatal("missing key replaced original key or ciphertext")
	}
}

func TestInitialSQLDeletionAuthenticatesAndPreservesOtherKinds(t *testing.T) {
	ctx := context.Background()
	repo := &initialRepository{records: map[string]Record{}}
	key := &initialKeys{}
	store := NewStore(repo, key)
	v := InitialSQL{ID: "initial", ProfileID: 8, Revision: 1, Text: "SET @v=1"}
	if err := store.SaveInitialSQL(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNamed(ctx, Named{ID: "named", Name: "kept", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(repo.records[v.ID].Payload)
	wrong := NewStore(repo, &initialKeys{key: bytes.Repeat([]byte{99}, 32)})
	if err := wrong.DeleteInitialSQL(ctx, v.ID); err == nil || !bytes.Equal(before, repo.records[v.ID].Payload) {
		t.Fatal("wrong key deleted record")
	}
	if err := store.DeleteInitialSQL(ctx, "named"); err != nil {
		t.Fatal(err)
	}
	if repo.records["named"].Kind != "query" {
		t.Fatal("initial cleanup deleted query")
	}
	if err := store.DeleteInitialSQL(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, exists := repo.records[v.ID]; exists {
		t.Fatal("initial record retained")
	}
	if err := store.DeleteInitialSQL(ctx, v.ID); err != nil {
		t.Fatal("delete is not idempotent", err)
	}
}

func TestInitialSQLSaveChecksScopeAndRevision(t *testing.T) {
	ctx := context.Background()
	repo := &initialRepository{records: map[string]Record{}}
	store := NewStore(repo, &initialKeys{})
	v := InitialSQL{ID: "initial", ProfileID: 8, Revision: 1, Text: "SET @v=1"}
	if err := store.SaveInitialSQL(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveInitialSQL(ctx, v); !errors.Is(err, ErrStaleRevision) {
		t.Fatal("stale revision accepted", err)
	}
	v.ProfileID = 9
	v.Revision = 2
	if err := store.SaveInitialSQL(ctx, v); !errors.Is(err, ErrQueryScope) {
		t.Fatal("wrong scope accepted", err)
	}
	v.ProfileID = 8
	if err := store.SaveInitialSQL(ctx, v); err != nil {
		t.Fatal(err)
	}
}

type lookupInitialRepository struct {
	*initialRepository
	lists, lookups int
}

func (r *lookupInitialRepository) List(ctx context.Context, kind string, profile int64) ([]Record, error) {
	r.lists++
	return r.initialRepository.List(ctx, kind, profile)
}
func (r *lookupInitialRepository) GetRecord(_ context.Context, kind, id string) (Record, bool, error) {
	r.lookups++
	v, ok := r.records[id]
	return v, ok && v.Kind == kind, nil
}

func TestInitialSQLLoadUsesDirectLookupAndChecksPayloadIdentity(t *testing.T) {
	ctx := context.Background()
	r := &lookupInitialRepository{initialRepository: &initialRepository{records: map[string]Record{}}}
	s := NewStore(r, &initialKeys{})
	v := InitialSQL{ID: "initial", ProfileID: 8, Revision: 1, Text: "SELECT 1"}
	if err := s.SaveInitialSQL(ctx, v); err != nil {
		t.Fatal(err)
	}
	r.lists, r.lookups = 0, 0
	if got, err := s.LoadInitialSQL(ctx, v.ID); err != nil || got != v || r.lists != 0 || r.lookups != 1 {
		t.Fatal("load fetched unrelated payloads", err)
	}
	s.mu.Lock()
	raw, err := s.seal(ctx, Record{ID: v.ID, Kind: "initial_sql", ProfileID: 8, Revision: 1}, InitialSQL{ID: "different", ProfileID: 8, Revision: 1})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	r.records[v.ID] = raw
	if _, err := s.LoadInitialSQL(ctx, v.ID); err == nil {
		t.Fatal("mismatched payload accepted")
	}
	if err := s.DeleteInitialSQL(ctx, v.ID); err == nil {
		t.Fatal("mismatched payload deleted")
	}
}
