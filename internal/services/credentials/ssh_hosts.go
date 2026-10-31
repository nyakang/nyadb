package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/transport"
	"os"
	"path/filepath"
	"sync"
)

// FileHostStore contains public fingerprints only. A missing OS keyring does
// not prevent host verification; unreadable/corrupt existing trust never resets.
type FileHostStore struct {
	Path string
	mu   sync.Mutex
}

func DefaultHostStore() (*FileHostStore, error) {
	dir, e := os.UserConfigDir()
	if e != nil {
		return nil, e
	}
	return &FileHostStore{Path: filepath.Join(dir, "NyaDB", "ssh-hosts-v1.json")}, nil
}
func (s *FileHostStore) read() (map[string]string, error) {
	b, e := os.ReadFile(s.Path)
	if errors.Is(e, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if e != nil {
		return nil, e
	}
	if len(b) > 1<<20 {
		return nil, errors.New("SSH trust file exceeds limit")
	}
	var m map[string]string
	if e = json.Unmarshal(b, &m); e != nil {
		return nil, fmt.Errorf("SSH trust file unreadable: %w", e)
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}
func (s *FileHostStore) Lookup(host string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, e := s.read()
	if e != nil {
		return "", false, e
	}
	v, ok := m[host]
	return v, ok, nil
}
func (s *FileHostStore) Remember(host, fp string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, e := s.read()
	if e != nil {
		return e
	}
	if old, ok := m[host]; ok && old != fp {
		return transport.ErrHostChanged
	}
	if len(m) >= 4096 {
		return errors.New("SSH host trust limit exceeded")
	}
	m[host] = fp
	b, e := json.Marshal(m)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(s.Path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(s.Path), ".ssh-hosts-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), s.Path)
}
