package credentials

import (
	"errors"
	"github.com/nyakang/nyadb/internal/transport"
	"os"
	"path/filepath"
	"testing"
)

type tunnelFixtureVault struct {
	values      map[string]string
	unavailable bool
}

func (v *tunnelFixtureVault) Get(id int64, name string) (string, bool, error) {
	if v.unavailable {
		return "", false, errors.New("locked")
	}
	value, found := v.values[tunnelAccount(id, name)]
	return value, found, nil
}
func (v *tunnelFixtureVault) Set(id int64, name, value string) error {
	if v.unavailable {
		return errors.New("locked")
	}
	if v.values == nil {
		v.values = map[string]string{}
	}
	v.values[tunnelAccount(id, name)] = value
	return nil
}
func (v *tunnelFixtureVault) Delete(id int64, name string) error {
	if v.unavailable {
		return errors.New("locked")
	}
	delete(v.values, tunnelAccount(id, name))
	return nil
}
func TestTunnelSecretMemoryKeyringAndUnreadableRetention(t *testing.T) {
	vault := &tunnelFixtureVault{}
	store := NewTunnelStoreWithVault(vault)
	secret := transport.Secrets{SSHPassword: "ssh", SSHPassphrase: "key", HTTPToken: "token", ProxyPassword: "proxy"}
	if e := store.Put(7, "alternate", secret, true); e != nil {
		t.Fatal(e)
	}
	store.ClearMemory()
	got, saved, e := store.Get(7, "alternate")
	if e != nil || !saved || got != secret {
		t.Fatalf("secret restore: %+v %v %v", got, saved, e)
	}
	vault.unavailable = true
	if _, _, e = store.Get(7, "alternate"); e == nil {
		t.Fatal("locked vault appears absent")
	}
	updated := transport.Secrets{SSHPassword: "memory"}
	if e = store.Put(7, "alternate", updated, false); e != nil {
		t.Fatal(e)
	}
	got, saved, e = store.Get(7, "alternate")
	if e != nil || saved || got != updated {
		t.Fatal("memory fallback unavailable")
	}
	if e = store.Put(7, "alternate", updated, true); e == nil {
		t.Fatal("locked vault overwritten")
	}
	vault.unavailable = false
	store.ClearMemory()
	got, saved, e = store.Get(7, "alternate")
	if e != nil || !saved || got != secret {
		t.Fatal("old secret lost")
	}
}
func TestSSHFileFingerprintsPersistAndCorruptionDoesNotReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh-hosts.json")
	s := &FileHostStore{Path: path}
	if _, found, e := s.Lookup("host:22"); e != nil || found {
		t.Fatal("new trust not empty")
	}
	if e := s.Remember("host:22", "SHA256:first"); e != nil {
		t.Fatal(e)
	}
	other := &FileHostStore{Path: path}
	fp, found, e := other.Lookup("host:22")
	if e != nil || !found || fp != "SHA256:first" {
		t.Fatal("trust not persisted")
	}
	if e = other.Remember("host:22", "SHA256:changed"); !errors.Is(e, transport.ErrHostChanged) {
		t.Fatal("fingerprint overwritten")
	}
	os.WriteFile(path, []byte("broken"), 0600)
	if _, _, e = other.Lookup("host:22"); e == nil {
		t.Fatal("corruption silently reset")
	}
	if e = other.Remember("host:22", "SHA256:new"); e == nil {
		t.Fatal("corrupt old trust replaced")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "broken" {
		t.Fatal("old trust not retained")
	}
}
