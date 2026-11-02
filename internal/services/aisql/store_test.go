package aisql

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

type memoryRepository struct {
	mu   sync.Mutex
	raw  string
	fail bool
}

func (r *memoryRepository) Load(context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.raw, nil
}
func (r *memoryRepository) CompareAndSwap(_ context.Context, before, after string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return ErrStorage
	}
	if r.raw != before {
		return ErrConflict
	}
	r.raw = after
	return nil
}

type memoryKeys struct {
	mu                  sync.Mutex
	values              map[string]string
	failSet, failDelete bool
}

func (k *memoryKeys) Get(id string) (string, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.values[id]
	return v, ok, nil
}
func (k *memoryKeys) Set(id, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failSet {
		return ErrKey
	}
	if k.values == nil {
		k.values = map[string]string{}
	}
	k.values[id] = value
	return nil
}
func (k *memoryKeys) Delete(id string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failDelete {
		return ErrKey
	}
	delete(k.values, id)
	return nil
}
func configured(t *testing.T, endpoint string, stream bool) (*Store, Snapshot, *memoryRepository, *memoryKeys) {
	t.Helper()
	repo, keys := &memoryRepository{}, &memoryKeys{}
	s := NewStore(repo, keys)
	before, e := s.Load(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	c := Defaults()
	c.Endpoint, c.Model, c.Stream = endpoint, "owned-fixture-model", stream
	r, e := s.Save(context.Background(), before, c, "fixture-api-key", false)
	if e != nil {
		t.Fatal(e)
	}
	return s, r.Snapshot, repo, keys
}
func TestAISettingsImmutableKeysConflictFailureAndRetiredCleanup(t *testing.T) {
	s, original, repo, keys := configured(t, "https://example.invalid/v1", true)
	if strings.Contains(original.Raw, "fixture-api-key") {
		t.Fatal("key entered public preferences")
	}
	key, e := s.Key(original.Config)
	if e != nil || key != "fixture-api-key" {
		t.Fatal("key origin binding failed")
	}
	repo.fail = true
	_, e = s.Save(context.Background(), original, original.Config, "replacement-key", false)
	if !errors.Is(e, ErrStorage) {
		t.Fatal("failed publication not reported")
	}
	if key, e = s.Key(original.Config); e != nil || key != "fixture-api-key" || len(keys.values) != 1 {
		t.Fatal("failed save lost the original or retained its unpublished replacement")
	}
	repo.fail = false
	changed := original.Config.Clone()
	changed.Endpoint = "https://other.invalid/v1"
	if _, e = s.Save(context.Background(), original, changed, "", false); !errors.Is(e, ErrKey) {
		t.Fatal("old key followed another origin")
	}
	keys.failDelete = true
	r, e := s.Save(context.Background(), original, original.Config, "replacement-key", false)
	if e != nil || !r.CleanupPending || len(r.Snapshot.Config.Retired) != 1 {
		t.Fatal("retired key cleanup failure was lost")
	}
	if _, e = s.Save(context.Background(), original, original.Config, "late-key", false); !errors.Is(e, ErrConflict) {
		t.Fatal("stale revision replaced the current key")
	}
	keys.failDelete = false
	r, e = s.Cleanup(context.Background(), r.Snapshot)
	if e != nil || r.CleanupPending || len(keys.values) != 1 {
		t.Fatal("retired key cleanup did not recover")
	}
	delete(keys.values, r.Snapshot.Config.KeyID)
	if _, e = s.Key(r.Snapshot.Config); !errors.Is(e, ErrKey) {
		t.Fatal("lost key generated a substitute")
	}
	loaded, e := s.Load(context.Background())
	if e != nil || loaded.Config.KeyID != r.Snapshot.Config.KeyID {
		t.Fatal("missing key reinitialized settings")
	}
}
func TestAIConfigurationRejectsCredentialsUnknownRecordsAndUnsafeOrigins(t *testing.T) {
	for _, endpoint := range []string{"http://remote.invalid/v1", "https://user:secret@example.invalid/v1", "https://example.invalid/v1?key=secret", "https://example.invalid/v1#secret", "file:///tmp/model"} {
		c := Defaults()
		c.Endpoint, c.Model = endpoint, "model"
		if _, e := Normalize(c); !errors.Is(e, ErrConfig) {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	for _, raw := range []string{`{"version":99}`, `{} trailing`, `{"api_key":"fixture-api-key"}`} {
		if _, e := Decode(raw); e == nil {
			t.Fatal("unsupported or credential-bearing configuration accepted")
		}
	}
}
