package driverstore

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func jdbcTestJar(t *testing.T, manifest string) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	if manifest != "" {
		entry, err := w.Create("META-INF/MANIFEST.MF")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(manifest)); err != nil {
			t.Fatal(err)
		}
	}
	entry, err := w.Create("META-INF/LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	entry.Write([]byte("owned JDBC fixture license retained inside original jar"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func jdbcTestBase(t *testing.T, s *Store) (connections.ManagedDriver, Product) {
	t.Helper()
	m, files := packageFixture([]byte("owned JRE fixture"))
	m.Key, m.Runtime, m.MainClass = "h2", "java", "com.dbx.agent.h2.H2Agent"
	m.Profiles = []string{"h2-v1", "h2-v2", "h2-v3", "h2-auto"}
	files["agent.jar"] = jdbcTestJar(t, "Manifest-Version: 1.0\r\n\r\n")
	m.Classpath = []string{"agent.jar"}
	m.Files = nil
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m.Files = append(m.Files, File{Path: name, SHA256: digestBytes(files[name]), Executable: name == m.Executable})
	}
	raw := zipFixture(t, m, files, nil)
	if _, err := s.Install(context.Background(), bytes.NewReader(raw), digestBytes(raw)); err != nil {
		t.Fatal(err)
	}
	return s.snapshot(m, digestBytes(raw)), Product{Key: m.Key, Runtime: m.Runtime, MainClass: m.MainClass}
}

func jdbcTestIndexedJar(t *testing.T, index string) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	entry, err := w.Create("META-INF/INDEX.LIST")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(index)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestJDBCImportCapturesJarIndexGraphAndRejectsUncapturedReferences(t *testing.T) {
	for _, reference := range []string{"deps/driver.jar", "deps/missing.jar", "https://example.invalid/remote.jar", "/outside.jar"} {
		t.Run(reference, func(t *testing.T) {
			s := newStore(t)
			base, product := jdbcTestBase(t, s)
			source := t.TempDir()
			entry := filepath.Join(source, "entry.jar")
			dependency := filepath.Join(source, "deps", "driver.jar")
			if err := os.MkdirAll(filepath.Dir(dependency), 0700); err != nil {
				t.Fatal(err)
			}
			entryBytes := jdbcTestIndexedJar(t, "JarIndex-Version: 1.0\n\nentry.jar\nfixture\n\n"+reference+"\norg/h2\n\n")
			dependencyBytes := jdbcTestJar(t, "Manifest-Version: 1.0\r\n\r\n")
			if err := os.WriteFile(entry, entryBytes, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dependency, dependencyBytes, 0600); err != nil {
				t.Fatal(err)
			}
			imported, err := s.ImportJDBC(context.Background(), product, connections.JDBCImport{BaseVersion: base.Version, BaseSHA256: base.SHA256, Profile: "h2-custom", Class: "org.h2.Driver", Paths: []string{entry}})
			if reference != "deps/driver.jar" {
				if err == nil {
					t.Fatal("uncaptured JarIndex graph published")
				}
				list, err := s.List(context.Background(), product)
				if err != nil || len(list) != 1 {
					t.Fatal("rejected JarIndex graph changed package inventory", err)
				}
				return
			}
			if err != nil {
				t.Fatal("local JarIndex graph unavailable", err)
			}
			if err := os.RemoveAll(source); err != nil {
				t.Fatal(err)
			}
			lease, err := s.Acquire(context.Background(), imported.Key, imported.Version, imported.SHA256)
			if err != nil {
				t.Fatal("JarIndex graph depended on deleted source", err)
			}
			defer lease.Close()
			_, paths, err := lease.JDBC()
			if err != nil || len(paths) != 1 {
				t.Fatal("captured JarIndex entry unavailable", err)
			}
			for name, expected := range map[string][]byte{"entry.jar": entryBytes, "deps/driver.jar": dependencyBytes} {
				actual, err := os.ReadFile(filepath.Join(filepath.Dir(paths[0]), filepath.FromSlash(name)))
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatal("JarIndex bytes or relative layout changed", name, err)
				}
			}
		})
	}
}

func TestJDBCImportPreservesOriginalRelativeGraphBytesAndBaseLeases(t *testing.T) {
	s := newStore(t)
	base, product := jdbcTestBase(t, s)
	original, err := s.Acquire(context.Background(), base.Key, base.Version, base.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "drivers"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "deps"), 0700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(source, "drivers", "entry.jar")
	dependency := filepath.Join(source, "deps", "my h2.jar")
	entryBytes := jdbcTestJar(t, "Manifest-Version: 1.0\r\nClass-Path: ../deps/my%20h2.\r\n jar\r\n\r\n")
	dependencyBytes := jdbcTestJar(t, "Manifest-Version: 1.0\r\n\r\n")
	if err := os.WriteFile(entry, entryBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dependency, dependencyBytes, 0600); err != nil {
		t.Fatal(err)
	}
	request := connections.JDBCImport{BaseVersion: base.Version, BaseSHA256: base.SHA256, Profile: "h2-custom", Class: "org.h2.Driver", Paths: []string{entry}}
	imported, err := s.ImportJDBC(context.Background(), product, request)
	if err != nil || imported.SHA256 == base.SHA256 || !imported.Supports("h2-custom") || imported.JDBCClass != request.Class {
		t.Fatal("original graph was not published as a distinct package", err)
	}
	copy, err := s.ImportJDBC(context.Background(), product, request)
	if err != nil || copy.Version != imported.Version || copy.SHA256 != imported.SHA256 {
		t.Fatal("same captured graph was not idempotent", err)
	}
	if err := os.WriteFile(entry, []byte("changed caller file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dependency); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Acquire(context.Background(), imported.Key, imported.Version, imported.SHA256)
	if err != nil {
		t.Fatal("caller mutation changed retained graph", err)
	}
	defer lease.Close()
	class, paths, err := lease.JDBC()
	if err != nil || class != request.Class || len(paths) != 1 || !strings.HasPrefix(paths[0], s.directory) {
		t.Fatal("runtime did not resolve original package paths", err)
	}
	retainedEntry, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(retainedEntry, entryBytes) {
		t.Fatal("entry jar bytes or manifest were rewritten", err)
	}
	retainedDependency, err := os.ReadFile(filepath.Join(filepath.Dir(paths[0]), "..", "deps", "my h2.jar"))
	if err != nil || !bytes.Equal(retainedDependency, dependencyBytes) {
		t.Fatal("relative dependency bytes or layout changed", err)
	}
	m := lease.Manifest()
	m.JDBC.Classpath[0] = "changed"
	if _, originalPaths, err := lease.JDBC(); err != nil || originalPaths[0] != paths[0] {
		t.Fatal("manifest exposed mutable original graph")
	}
	if _, err := original.LaunchSpec("h2-auto"); err != nil {
		t.Fatal("custom import damaged original active base lease", err)
	}
	if original.Manifest().JDBC != nil || original.Manifest().Version != base.Version {
		t.Fatal("base package was changed")
	}
}

func TestJDBCImportRejectsMissingRemoteAbsoluteAndChangedBaseBeforePublication(t *testing.T) {
	for _, reference := range []string{"missing.jar", "https://example.invalid/remote.jar", "file:/outside.jar", "/outside.jar"} {
		t.Run(reference, func(t *testing.T) {
			s := newStore(t)
			base, product := jdbcTestBase(t, s)
			entry := filepath.Join(t.TempDir(), "entry.jar")
			if err := os.WriteFile(entry, jdbcTestJar(t, "Manifest-Version: 1.0\r\nClass-Path: "+reference+"\r\n\r\n"), 0600); err != nil {
				t.Fatal(err)
			}
			request := connections.JDBCImport{BaseVersion: base.Version, BaseSHA256: base.SHA256, Profile: "h2-custom", Class: "org.h2.Driver", Paths: []string{entry}}
			if _, err := s.ImportJDBC(context.Background(), product, request); err == nil {
				t.Fatal("uncaptured graph reference accepted")
			}
			list, err := s.List(context.Background(), product)
			if err != nil || len(list) != 1 || list[0].Version != base.Version {
				t.Fatal("failed graph import published another package", err)
			}
			request.BaseSHA256 = strings.Repeat("0", 64)
			if _, err := s.ImportJDBC(context.Background(), product, request); err == nil {
				t.Fatal("changed original base admitted")
			}
		})
	}
}

func TestJDBCArchiveImportCannotBypassCapturedDependencyGraph(t *testing.T) {
	s := newStore(t)
	base, product := jdbcTestBase(t, s)
	lease, err := s.Acquire(context.Background(), base.Key, base.Version, base.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	m := lease.Manifest()
	lease.Close()
	files := map[string][]byte{}
	for _, file := range m.Files {
		files[file.Path], err = os.ReadFile(filepath.Join(s.directory, base.Key+"@"+base.Version, filepath.FromSlash(file.Path)))
		if err != nil {
			t.Fatal(err)
		}
	}
	m.Version = "invalid-custom"
	m.Profiles = append(m.Profiles, "h2-custom")
	m.JDBC = &JDBCDriver{Class: "org.h2.Driver", Classpath: []string{"jdbc/entry.jar"}}
	files["jdbc/entry.jar"] = jdbcTestJar(t, "Manifest-Version: 1.0\r\nClass-Path: missing.jar\r\n\r\n")
	m.Files = append(m.Files, File{Path: "jdbc/entry.jar", SHA256: digestBytes(files["jdbc/entry.jar"])})
	raw := zipFixture(t, m, files, nil)
	if _, err := s.install(context.Background(), bytes.NewReader(raw), digestBytes(raw), &product); !errors.Is(err, ErrPackage) {
		t.Fatal("archive import bypassed graph validation", err)
	}
	if _, err := s.Acquire(context.Background(), m.Key, m.Version, digestBytes(raw)); err == nil {
		t.Fatal("rejected graph became an installed version")
	}
}
