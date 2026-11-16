package zookeeper

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

func TestProviderTrustCaptureFailureReturnsNilSessionBeforeLaunch(t *testing.T) {
	// This package only exercises the real selection/capture boundary. Its inert
	// payload must never execute; it does not implement a database protocol.
	files := map[string][]byte{"bin/agent": []byte("must never execute"), "LICENSE": []byte("test fixture"), "sources.json": []byte(`{"scope":"prelaunch failure regression"}`)}
	m := driverstore.Manifest{Format: 1, Key: "zookeeper", Version: "test-trust-failure", Platform: runtime.GOOS + "-" + runtime.GOARCH, Runtime: "native", Executable: "bin/agent", Profiles: []string{"zookeeper-v1"}, Licenses: []string{"LICENSE"}, Provenance: "sources.json"}
	for _, name := range []string{"bin/agent", "LICENSE", "sources.json"} {
		hash := sha256.Sum256(files[name])
		m.Files = append(m.Files, driverstore.File{Path: name, SHA256: hex.EncodeToString(hash[:]), Executable: name == m.Executable})
	}
	manifest, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	files["manifest.json"] = manifest
	for _, name := range []string{"manifest.json", "bin/agent", "LICENSE", "sources.json"} {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	store, err := driverstore.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(archive.Bytes())
	d, err := store.Install(context.Background(), bytes.NewReader(archive.Bytes()), hex.EncodeToString(hash[:]))
	if err != nil {
		t.Fatal(err)
	}
	p, err := connections.Defaults(connections.ZooKeeper)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Name, p.Host, p.TLSMode = 1, "trust capture failure", "127.0.0.1", "verify-full"
	p.ZooKeeperOptions.DriverDirectory, p.ZooKeeperOptions.DriverVersion, p.ZooKeeperOptions.DriverSHA256 = directory, d.Version, hex.EncodeToString(hash[:])
	p.ZooKeeperOptions.CAFile = filepath.Join(t.TempDir(), "invalid-ca.pem")
	if err := os.WriteFile(p.ZooKeeperOptions.CAFile, []byte("invalid PEM"), 0600); err != nil {
		t.Fatal(err)
	}
	starts := 0
	session, err := (Provider{Started: func(int) error { starts++; return nil }}).Open(context.Background(), p, "")
	if !errors.Is(err, ErrInvalid) || session != nil || starts != 0 {
		t.Fatalf("failed trust capture returned session=%#v, error=%v, starts=%d", session, err, starts)
	}
	if err := store.Remove(context.Background(), d.Key, d.Version); err != nil {
		t.Fatal("failed capture retained a package lease", err)
	}
}
