package driverstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProductBoundInstallRejectsForeignSnapshotBeforePublication(t *testing.T) {
	s := newStore(t)
	m, files := packageFixture([]byte("original executable"))
	raw := zipFixture(t, m, files, nil)
	product := Product{Key: m.Key, Runtime: "java", MainClass: "com.dbx.agent.h2.H2Agent"}
	if _, err := s.install(context.Background(), bytes.NewReader(raw), digestBytes(raw), &product); !errors.Is(err, ErrPackage) {
		t.Fatal("digest-matched foreign snapshot was published", err)
	}
	if _, err := s.Acquire(context.Background(), m.Key, m.Version, digestBytes(raw)); err == nil {
		t.Fatal("rejected product snapshot became an installed version")
	}
}

func TestDriverImportListPinsOriginalPackageAndRejectsForeignReplacement(t *testing.T) {
	s := newStore(t)
	m, files := packageFixture([]byte("original executable"))
	raw := zipFixture(t, m, files, nil)
	file := filepath.Join(t.TempDir(), "original.zip")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	product := Product{Key: m.Key, Runtime: m.Runtime, MainClass: m.MainClass}
	wrong := product
	wrong.Key = "another-product"
	if _, err := s.Import(context.Background(), wrong, file); !errors.Is(err, ErrPackage) {
		t.Fatal("foreign package was imported", err)
	}
	list, err := s.List(context.Background(), product)
	if err != nil || len(list) != 0 {
		t.Fatal("rejected foreign import was published", err)
	}
	driver, err := s.Import(context.Background(), product, file)
	if err != nil || driver.SHA256 != digestBytes(raw) || driver.Version != m.Version || driver.Directory != s.directory {
		t.Fatal("original import identity changed", err)
	}
	lease, err := s.Acquire(context.Background(), m.Key, m.Version, driver.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	copy, err := s.Import(context.Background(), product, file)
	if err != nil || copy.SHA256 != driver.SHA256 {
		t.Fatal("same original archive reimport was not idempotent", err)
	}
	list, err = s.List(context.Background(), product)
	if err != nil || len(list) != 1 || !list[0].Supports("fixture-v1") {
		t.Fatal("installed original unavailable", err)
	}
	list[0].Profiles[0] = "mutated"
	list, err = s.List(context.Background(), product)
	if err != nil || !list[0].Supports("fixture-v1") {
		t.Fatal("inventory exposed mutable original metadata", err)
	}
	files["bin/agent"] = []byte("replacement")
	for i := range m.Files {
		if m.Files[i].Path == "bin/agent" {
			m.Files[i].SHA256 = digestBytes(files["bin/agent"])
		}
	}
	changed := zipFixture(t, m, files, nil)
	if err := os.WriteFile(file, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(context.Background(), product, file); err == nil {
		t.Fatal("installed version was replaced")
	}
	if _, err := lease.LaunchSpec("fixture-v1"); err != nil {
		t.Fatal("rejected import damaged original lease", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.List(ctx, product); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled inventory completed", err)
	}
}

func TestDriverPinnedRemovalRejectsChangedDigestAndLeasedOriginal(t *testing.T) {
	s := newStore(t)
	m, files := packageFixture([]byte("original executable"))
	raw := zipFixture(t, m, files, nil)
	if _, err := s.Install(context.Background(), bytes.NewReader(raw), digestBytes(raw)); err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePinned(context.Background(), m.Key, m.Version, digestBytes([]byte("foreign"))); !errors.Is(err, ErrPackage) {
		t.Fatal("foreign removal accepted", err)
	}
	lease, err := s.Acquire(context.Background(), m.Key, m.Version, digestBytes(raw))
	if err != nil {
		t.Fatal("mismatch damaged installed package", err)
	}
	if err := s.RemovePinned(context.Background(), m.Key, m.Version, digestBytes(raw)); !errors.Is(err, ErrBusy) {
		t.Fatal("leased package removed", err)
	}
	if _, err := lease.LaunchSpec("fixture-v1"); err != nil {
		t.Fatal("removal damaged original lease", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.RemovePinned(ctx, m.Key, m.Version, digestBytes(raw)); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled removal completed", err)
	}
	if err := s.RemovePinned(context.Background(), m.Key, m.Version, digestBytes(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(context.Background(), m.Key, m.Version, digestBytes(raw)); !errors.Is(err, ErrMissing) {
		t.Fatal("removed package still executable", err)
	}
}
