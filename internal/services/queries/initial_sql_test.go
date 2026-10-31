package queries

import (
	"bytes"
	"context"
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
