// Package queries persists encrypted SQL documents through injected storage.
package queries

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

var ErrKeyUnavailable = errors.New("SQL workspace encryption key unavailable; encrypted records retained")
var ErrStaleRevision = errors.New("SQL workspace revision is stale")

type Record struct {
	ID, Kind  string
	ProfileID int64
	Version   int
	Revision  uint64
	Payload   []byte
}
type Repository interface {
	List(context.Context, string, int64) ([]Record, error)
	Put(context.Context, Record) error
	Delete(context.Context, string) error
	HasRecords(context.Context) (bool, error)
}
type KeyProvider interface {
	Get() ([]byte, bool, error)
	Set([]byte) error
}
type Document struct {
	NamedRevision                        uint64
	ID                                   uint64
	QueryID, Name, Path, Text, SavedText string
	ProfileID                            int64
	Database                             string
	Cursor, Anchor                       int
	Revision                             uint64
}
type Workspace struct {
	Documents []Document
	ActiveID  uint64
	Revision  uint64
}
type Named struct {
	ID, Name, Text string
	ProfileID      int64
	Database       string
	Revision       uint64
}

type Store struct {
	repository Repository
	keys       KeyProvider
	mu         sync.Mutex
	aead       cipher.AEAD
	blocked    error
}

func NewStore(repository Repository, keys KeyProvider) *Store {
	return &Store{repository: repository, keys: keys}
}
func NewID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func (s *Store) crypt(ctx context.Context) (cipher.AEAD, error) {
	if s.blocked != nil {
		return nil, s.blocked
	}
	if s.aead != nil {
		return s.aead, nil
	}
	if s.keys == nil {
		return nil, ErrKeyUnavailable
	}
	key, found, err := s.keys.Get()
	if err != nil {
		return nil, ErrKeyUnavailable
	}
	if !found {
		exists, e := s.repository.HasRecords(ctx)
		if e != nil {
			return nil, e
		}
		if exists {
			return nil, ErrKeyUnavailable
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		if err = s.keys.Set(key); err != nil {
			return nil, ErrKeyUnavailable
		}
	}
	if len(key) != 32 {
		return nil, ErrKeyUnavailable
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	s.aead, e = cipher.NewGCM(block)
	return s.aead, e
}
func aad(r Record) []byte {
	return []byte(fmt.Sprintf("NyaDB/SQL/%s/%s/%d/%d/%d", r.ID, r.Kind, r.ProfileID, r.Version, r.Revision))
}
func (s *Store) put(ctx context.Context, r Record, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, e := s.crypt(ctx)
	if e != nil {
		return e
	}
	plain, e := json.Marshal(value)
	if e != nil {
		return e
	}
	nonce := make([]byte, a.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	r.Version = 1
	r.Payload = a.Seal(nonce, nonce, plain, aad(r))
	return s.repository.Put(ctx, r)
}
func (s *Store) open(ctx context.Context, r Record, dest any) error {
	a, e := s.crypt(ctx)
	if e != nil {
		return e
	}
	if r.Version != 1 || len(r.Payload) < a.NonceSize() {
		s.blocked = errors.New("invalid encrypted SQL document; original record retained")
		return s.blocked
	}
	plain, e := a.Open(nil, r.Payload[:a.NonceSize()], r.Payload[a.NonceSize():], aad(r))
	if e != nil {
		s.blocked = errors.New("SQL document authentication failed; original record retained")
		return s.blocked
	}
	return json.Unmarshal(plain, dest)
}
func (s *Store) SaveWorkspace(ctx context.Context, w Workspace) error {
	return s.put(ctx, Record{ID: "workspace", Kind: "workspace", Revision: w.Revision}, w)
}
func (s *Store) LoadWorkspace(ctx context.Context) (Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var w Workspace
	rs, e := s.repository.List(ctx, "workspace", 0)
	if e != nil {
		return w, e
	}
	if len(rs) == 0 {
		return w, nil
	}
	e = s.open(ctx, rs[0], &w)
	return w, e
}
func (s *Store) SaveNamed(ctx context.Context, n Named) error {
	return s.put(ctx, Record{ID: n.ID, Kind: "query", ProfileID: n.ProfileID, Revision: n.Revision}, n)
}
func (s *Store) ListNamed(ctx context.Context, profile int64) ([]Named, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, e := s.repository.List(ctx, "query", profile)
	if e != nil {
		return nil, e
	}
	ns := make([]Named, 0, len(rs))
	for _, r := range rs {
		var n Named
		if e = s.open(ctx, r, &n); e != nil {
			return nil, e
		}
		ns = append(ns, n)
	}
	return ns, nil
}
func (s *Store) DeleteNamed(ctx context.Context, id string) error {
	if id == "workspace" {
		return errors.New("invalid query identity")
	}
	return s.repository.Delete(ctx, id)
}
