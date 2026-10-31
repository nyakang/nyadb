package credentials

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/nyakang/nyadb/internal/transport"
	keyring "github.com/zalando/go-keyring"
)

const tunnelService = "NyaDB transport secrets v1"
const sshHostsService = "NyaDB SSH host identities v1"

// TunnelStore keeps unsaved secrets in memory and optional saved snapshots in
// a separate OS keyring namespace. Existing database credential keys are untouched.
type TunnelStore struct {
	mu         sync.Mutex
	memory     map[string]transport.Secrets
	persistent bool
	vault      Vault
	saved      map[string]bool
}

func NewTunnelStore(persistent bool) *TunnelStore {
	return &TunnelStore{memory: make(map[string]transport.Secrets), persistent: persistent, vault: osTunnelVault{}, saved: make(map[string]bool)}
}
func tunnelAccount(id int64, variant string) string {
	return strconv.FormatInt(id, 10) + "/" + base64.RawURLEncoding.EncodeToString([]byte(variant))
}
func (s *TunnelStore) Get(id int64, variant string) (transport.Secrets, bool, error) {
	key := tunnelAccount(id, variant)
	s.mu.Lock()
	v, ok := s.memory[key]
	saved := s.saved[key]
	s.mu.Unlock()
	if ok {
		return v, saved, nil
	}
	if !s.persistent {
		return transport.Secrets{}, false, nil
	}
	text, found, e := s.vault.Get(id, variant)
	if e == nil && !found {
		return transport.Secrets{}, false, nil
	}
	if errors.Is(e, keyring.ErrNotFound) {
		return transport.Secrets{}, false, nil
	}
	if e != nil {
		return transport.Secrets{}, false, fmt.Errorf("read tunnel keyring: %w", e)
	}
	if e = json.Unmarshal([]byte(text), &v); e != nil {
		return transport.Secrets{}, false, errors.New("invalid tunnel credential record")
	}
	return v, true, nil
}
func (s *TunnelStore) Put(id int64, variant string, v transport.Secrets, save bool) error {
	if id <= 0 {
		return errors.New("missing profile identity")
	}
	key := tunnelAccount(id, variant)
	if save {
		if !s.persistent {
			return errors.New("system tunnel keyring unavailable")
		}
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		if e = s.vault.Set(id, variant, string(b)); e != nil {
			return fmt.Errorf("save tunnel keyring: %w", e)
		}

	}
	s.mu.Lock()
	s.memory[key] = v
	s.saved[key] = save
	s.mu.Unlock()
	return nil
}
func (s *TunnelStore) Forget(id int64, variant string) error {
	key := tunnelAccount(id, variant)
	s.mu.Lock()
	delete(s.memory, key)
	delete(s.saved, key)
	s.mu.Unlock()
	if s.persistent {
		e := s.vault.Delete(id, variant)
		if !errors.Is(e, keyring.ErrNotFound) {
			return e
		}
	}
	return nil
}
func (s *TunnelStore) ClearMemory() {
	s.mu.Lock()
	clear(s.memory)
	clear(s.saved)
	s.mu.Unlock()
}

// OSHostStore preserves the first explicitly accepted SHA256 fingerprint.
// Failure to read a previous key never creates a fresh trust baseline.
type OSHostStore struct{}

func (OSHostStore) Lookup(host string) (string, bool, error) {
	v, e := keyring.Get(sshHostsService, base64.RawURLEncoding.EncodeToString([]byte(host)))
	if errors.Is(e, keyring.ErrNotFound) {
		return "", false, nil
	}
	return v, e == nil, e
}
func (OSHostStore) Remember(host, fingerprint string) error {
	if fingerprint == "" {
		return errors.New("empty host fingerprint")
	}
	return keyring.Set(sshHostsService, base64.RawURLEncoding.EncodeToString([]byte(host)), fingerprint)
}

// MemoryHostStore supports an unlocked process-only trust baseline when the
// native keyring is unavailable. It never substitutes for an unreadable record.
type MemoryHostStore struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *MemoryHostStore) Lookup(host string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[host]
	return v, ok, nil
}
func (s *MemoryHostStore) Remember(host, fp string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = make(map[string]string)
	}
	if old, ok := s.values[host]; ok && old != fp {
		return transport.ErrHostChanged
	}
	s.values[host] = fp
	return nil
}

func (s *TunnelStore) RemoveSaved(id int64, variant string) error {
	if !s.persistent {
		return nil
	}
	err := s.vault.Delete(id, variant)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// NewTunnelStoreWithVault permits deterministic credential failure tests.
func NewTunnelStoreWithVault(vault Vault) *TunnelStore {
	s := NewTunnelStore(vault != nil)
	s.vault = vault
	return s
}

type osTunnelVault struct{}

func (osTunnelVault) Get(id int64, v string) (string, bool, error) {
	key, err := account(id, v)
	if err != nil {
		return "", false, err
	}
	value, err := keyring.Get(tunnelService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", false, nil
	}
	return value, err == nil, err
}
func (osTunnelVault) Set(id int64, v, value string) error {
	key, err := account(id, v)
	if err != nil {
		return err
	}
	return keyring.Set(tunnelService, key, value)
}
func (osTunnelVault) Delete(id int64, v string) error {
	key, err := account(id, v)
	if err != nil {
		return err
	}
	err = keyring.Delete(tunnelService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
