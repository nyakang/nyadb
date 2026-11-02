package aisql

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

type Repository interface {
	Load(context.Context) (string, error)
	CompareAndSwap(context.Context, string, string) error
}
type Keys interface {
	Get(string) (string, bool, error)
	Set(string, string) error
	Delete(string) error
}
type Snapshot struct {
	Config Config
	Raw    string
}
type SaveResult struct {
	Snapshot       Snapshot
	CleanupPending bool
}
type secret struct {
	Origin string `json:"origin"`
	Key    string `json:"key"`
}

// Credentials are immutable records. Publication first writes a fresh key,
// then conditionally updates non-secret settings; failure keeps the old key.
type Store struct {
	mu   sync.Mutex
	repo Repository
	keys Keys
}

func NewStore(repo Repository, keys Keys) *Store { return &Store{repo: repo, keys: keys} }
func (s *Store) Load(ctx context.Context) (Snapshot, error) {
	if s == nil || s.repo == nil {
		return Snapshot{}, ErrStorage
	}
	raw, e := s.repo.Load(ctx)
	if e != nil {
		return Snapshot{}, ErrStorage
	}
	c, e := Decode(raw)
	return Snapshot{Config: c, Raw: raw}, e
}
func (s *Store) Key(c Config) (string, error) {
	if c.Anonymous {
		return "", nil
	}
	if s.keys == nil || c.KeyID == "" {
		return "", ErrKey
	}
	raw, found, e := s.keys.Get(c.KeyID)
	if e != nil || !found {
		return "", ErrKey
	}
	var v secret
	if len(raw) > 8192 || json.Unmarshal([]byte(raw), &v) != nil || v.Origin != Origin(c) || !validKey(v.Key) {
		return "", ErrKey
	}
	return v.Key, nil
}
func validKey(key string) bool {
	return key != "" && len(key) <= 4096 && utf8.ValidString(key) && strings.IndexFunc(key, unicode.IsControl) < 0 && !strings.ContainsAny(key, " \t")
}
func (s *Store) Save(ctx context.Context, before Snapshot, c Config, key string, forget bool) (SaveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, e := s.Load(ctx)
	if e != nil {
		return SaveResult{}, e
	}
	if current.Raw != before.Raw {
		return SaveResult{}, ErrConflict
	}
	before = current
	c.KeyID, c.Retired, c.Revision = before.Config.KeyID, append([]string(nil), before.Config.Retired...), before.Config.Revision+1
	c, e = Normalize(c)
	if e != nil {
		return SaveResult{}, e
	}
	if c.Anonymous || forget {
		c.KeyID = ""
	}
	if key != "" && !c.Anonymous {
		if !validKey(key) || s.keys == nil {
			return SaveResult{}, ErrKey
		}
		id := make([]byte, 16)
		if _, e = rand.Read(id); e != nil {
			return SaveResult{}, ErrKey
		}
		c.KeyID = hex.EncodeToString(id)
	} else if !c.Anonymous && !forget && Origin(c) != Origin(before.Config) {
		return SaveResult{}, ErrKey
	}
	if c.KeyID != before.Config.KeyID && before.Config.KeyID != "" {
		c.Retired = append(c.Retired, before.Config.KeyID)
	}
	if len(c.Retired) > 16 {
		return SaveResult{}, ErrKey
	}
	fresh := c.KeyID != "" && c.KeyID != before.Config.KeyID
	if fresh {
		v, _ := json.Marshal(secret{Origin: Origin(c), Key: key})
		if e = s.keys.Set(c.KeyID, string(v)); e != nil {
			return SaveResult{}, ErrKey
		}
	}
	raw, _ := json.Marshal(c)
	if e = s.repo.CompareAndSwap(ctx, before.Raw, string(raw)); e != nil {
		if fresh {
			_ = s.keys.Delete(c.KeyID)
		}
		return SaveResult{}, e
	}
	result := SaveResult{Snapshot: Snapshot{Config: c, Raw: string(raw)}}
	return s.cleanup(ctx, result), nil
}
func (s *Store) Cleanup(ctx context.Context, before Snapshot) (SaveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, e := s.Load(ctx)
	if e != nil {
		return SaveResult{}, e
	}
	if before.Raw != current.Raw {
		return SaveResult{}, ErrConflict
	}
	return s.cleanup(ctx, SaveResult{Snapshot: before}), nil
}
func (s *Store) cleanup(ctx context.Context, result SaveResult) SaveResult {
	c := result.Snapshot.Config.Clone()
	remaining := []string{}
	for _, id := range c.Retired {
		if s.keys == nil || s.keys.Delete(id) != nil {
			remaining = append(remaining, id)
		}
	}
	result.CleanupPending = len(remaining) > 0
	if len(remaining) != len(c.Retired) {
		c.Retired = remaining
		c.Revision++
		raw, _ := json.Marshal(c)
		if s.repo.CompareAndSwap(ctx, result.Snapshot.Raw, string(raw)) == nil {
			result.Snapshot = Snapshot{Config: c, Raw: string(raw)}
		} else {
			result.CleanupPending = true
		}
	}
	return result
}
