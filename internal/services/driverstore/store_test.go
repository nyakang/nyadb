package driverstore

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func init() {
	if os.Getenv("NYADB_DRIVERSTORE_NATIVE_HELPER") != "1" {
		return
	}
	encode := json.NewEncoder(os.Stdout)
	_ = encode.Encode(map[string]bool{"ready": true})
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var r struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			os.Exit(2)
		}
		result := any(map[string]bool{"ok": true})
		if r.Method == "handshake" {
			result = agentbridge.Handshake{ProtocolVersion: 2, AgentProtocolVersion: 2, Capabilities: []string{"connect", "query", "multi_session", "structured_error_v1"}}
			if os.Getenv("NYADB_DRIVERSTORE_PROTOCOL") == "1" {
				result = agentbridge.Handshake{ProtocolVersion: 1, AgentProtocolVersion: 1, Capabilities: []string{"connect", "kv"}}
			}
		}
		if r.Method == "shutdown" {
			gate := os.Getenv("NYADB_DRIVERSTORE_SHUTDOWN_GATE")
			if gate != "" {
				if os.WriteFile(gate+".started", []byte("original"), 0600) != nil {
					os.Exit(3)
				}
				for i := 0; i < 500; i++ {
					if _, err := os.Stat(gate); err == nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		}
		_ = encode.Encode(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": result})
		if r.Method == "shutdown" {
			os.Exit(0)
		}
	}
	os.Exit(0)
}

func digestBytes(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }

func packageFixture(payload []byte) (Manifest, map[string][]byte) {
	files := map[string][]byte{"bin/agent": payload, "LICENSE": []byte("fixture license"), "NOTICE": []byte("fixture notice"), "sources.json": []byte(`{"scope":"owned test fixture"}`)}
	m := Manifest{Format: 1, Key: "fixture", Version: "1.0.0", Platform: runtime.GOOS + "-" + runtime.GOARCH, Runtime: "native", Executable: "bin/agent", Profiles: []string{"fixture-v1"}, Licenses: []string{"LICENSE", "NOTICE"}, Provenance: "sources.json"}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m.Files = append(m.Files, File{Path: name, SHA256: digestBytes(files[name]), Executable: name == "bin/agent"})
	}
	return m, files
}

func zipFixture(t *testing.T, m Manifest, files map[string][]byte, additional func(*zip.Writer)) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, raw []byte) {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", raw)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		write(name, files[name])
	}
	if additional != nil {
		additional(w)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInstallPinsExactVersionAndRejectsReplacementWhileLeased(t *testing.T) {
	s := newStore(t)
	m, files := packageFixture([]byte("original executable"))
	raw := zipFixture(t, m, files, nil)
	digest := digestBytes(raw)
	installed, err := s.Install(context.Background(), bytes.NewReader(raw), digest)
	if err != nil {
		t.Fatal(err)
	}
	installed.Files[0].SHA256 = strings.Repeat("0", 64)
	l, err := s.Acquire(context.Background(), m.Key, m.Version, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	spec, err := l.LaunchSpec("fixture-v1")
	if err != nil || len(spec.Artifacts) != 4 || len(spec.Args) != 0 || spec.Directory != "" {
		t.Fatalf("original launch: %+v %v", spec, err)
	}
	if _, err = l.LaunchSpec("different-profile"); !errors.Is(err, ErrPackage) {
		t.Fatal(err)
	}
	if err = s.Remove(context.Background(), m.Key, m.Version); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if _, err = s.Install(context.Background(), bytes.NewReader(raw), digest); !errors.Is(err, ErrExists) {
		t.Fatal(err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = l.LaunchSpec("fixture-v1"); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
	if err = s.Remove(context.Background(), m.Key, m.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Acquire(context.Background(), m.Key, m.Version, digest); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
}

func TestRejectedArchiveIsNeverPublishedAndKeepsOriginalVersion(t *testing.T) {
	for _, scenario := range []string{"archive-digest", "file-digest", "traversal", "symlink", "duplicate", "unlisted", "platform", "license", "prefix-collision", "case-collision"} {
		t.Run(scenario, func(t *testing.T) {
			s := newStore(t)
			m, files := packageFixture([]byte("original"))
			raw := zipFixture(t, m, files, nil)
			digest := digestBytes(raw)
			if _, err := s.Install(context.Background(), bytes.NewReader(raw), digest); err != nil {
				t.Fatal(err)
			}
			m.Version = "2.0.0"
			var extra func(*zip.Writer)
			switch scenario {
			case "file-digest":
				files["bin/agent"] = []byte("changed")
			case "traversal":
				m.Files[0].Path = "../escape"
				files["../escape"] = []byte("x")
			case "symlink":
				delete(files, "bin/agent")
				extra = func(w *zip.Writer) {
					h := &zip.FileHeader{Name: "bin/agent"}
					h.SetMode(os.ModeSymlink | 0700)
					f, err := w.CreateHeader(h)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = f.Write([]byte("../../escape"))
				}
			case "duplicate":
				extra = func(w *zip.Writer) {
					f, err := w.Create("LICENSE")
					if err != nil {
						t.Fatal(err)
					}
					_, _ = f.Write([]byte("duplicate"))
				}
			case "unlisted":
				files["extra"] = []byte("unknown")
			case "platform":
				m.Platform = "another-platform"
			case "license":
				m.Licenses = []string{"absent"}
			case "prefix-collision":
				m.Files = append(m.Files, File{Path: "bin", SHA256: digestBytes([]byte("x"))})
				files["bin"] = []byte("x")
			case "case-collision":
				m.Files = append(m.Files, File{Path: "license", SHA256: digestBytes([]byte("x"))})
				files["license"] = []byte("x")
			}
			bad := zipFixture(t, m, files, extra)
			badDigest := digestBytes(bad)
			if scenario == "archive-digest" {
				badDigest = strings.Repeat("0", 64)
			}
			if _, err := s.Install(context.Background(), bytes.NewReader(bad), badDigest); !errors.Is(err, ErrPackage) {
				t.Fatalf("bad archive accepted: %v", err)
			}
			entries, err := os.ReadDir(s.directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".install-") || entry.Name() == "fixture@2.0.0" {
					t.Fatal("failed install left publishable content")
				}
			}
			l, err := s.Acquire(context.Background(), "fixture", "1.0.0", digest)
			if err != nil {
				t.Fatalf("original install changed: %v", err)
			}
			if err = l.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type canceledInput struct{ cancel context.CancelFunc }

func (r canceledInput) Read(p []byte) (int, error) {
	r.cancel()
	return copy(p, "partial archive"), io.EOF
}

func TestCanceledInstallAndTamperedInstalledFileCannotLaunch(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := s.Install(ctx, canceledInput{cancel}, strings.Repeat("0", 64)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	m, files := packageFixture([]byte("original"))
	raw := zipFixture(t, m, files, nil)
	if _, err := s.Install(context.Background(), bytes.NewReader(raw), digestBytes(raw)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.directory, "fixture@1.0.0", "bin", "agent"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(context.Background(), m.Key, m.Version, digestBytes(raw)); !errors.Is(err, ErrPackage) {
		t.Fatal(err)
	}
	if err := s.Remove(context.Background(), m.Key, m.Version); err != nil {
		t.Fatal("tampered install incorrectly acquired a lease", err)
	}
}

func TestInstalledManifestRemainsBoundToOriginalArchiveDigest(t *testing.T) {
	s := newStore(t)
	m, files := packageFixture([]byte("original"))
	raw := zipFixture(t, m, files, nil)
	if _, err := s.Install(context.Background(), bytes.NewReader(raw), digestBytes(raw)); err != nil {
		t.Fatal(err)
	}
	m.Profiles = []string{"replacement-profile"}
	changed, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.directory, "fixture@1.0.0", "manifest.json"), changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Acquire(context.Background(), m.Key, m.Version, digestBytes(raw)); !errors.Is(err, ErrPackage) {
		t.Fatal("changed manifest was accepted under the original archive digest", err)
	}
}

func TestJavaBundleUsesItsOriginalRuntimeClasspathAndProfile(t *testing.T) {
	s := newStore(t)
	m, files := packageFixture([]byte("java binary fixture"))
	m.Runtime = "java"
	m.MainClass = "com.example.DatabaseAgent"
	m.Classpath = []string{"agent.jar", "drivers/engine.jar"}
	for _, p := range m.Classpath {
		files[p] = []byte("fixture jar " + p)
		m.Files = append(m.Files, File{Path: p, SHA256: digestBytes(files[p])})
	}
	raw := zipFixture(t, m, files, nil)
	if _, err := s.Install(context.Background(), bytes.NewReader(raw), digestBytes(raw)); err != nil {
		t.Fatal(err)
	}
	l, err := s.Acquire(context.Background(), m.Key, m.Version, digestBytes(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	spec, err := l.LaunchSpec("fixture-v1")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(s.directory, "fixture@1.0.0", "agent.jar") + string(os.PathListSeparator) + filepath.Join(s.directory, "fixture@1.0.0", "drivers", "engine.jar")
	if len(spec.Args) != 3 || spec.Args[0] != "-cp" || spec.Args[1] != want || spec.Args[2] != m.MainClass {
		t.Fatalf("runtime lost original classpath: %#v", spec.Args)
	}
}

func TestStoreInstancesShareMutationLockAndOriginalLeaseMarker(t *testing.T) {
	s := newStore(t)
	other, err := New(s.directory)
	if err != nil {
		t.Fatal(err)
	}
	m, files := packageFixture([]byte("original"))
	raw := zipFixture(t, m, files, nil)
	if _, err = s.Install(context.Background(), bytes.NewReader(raw), digestBytes(raw)); err != nil {
		t.Fatal(err)
	}
	root, err := s.root()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lock, err := lockStore(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.Acquire(context.Background(), m.Key, m.Version, digestBytes(raw))
	unlockStore(lock)
	if !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	l, err := s.Acquire(context.Background(), m.Key, m.Version, digestBytes(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err = other.Remove(context.Background(), m.Key, m.Version); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if err = other.Remove(context.Background(), m.Key, m.Version); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRuntimeKeepsBundleUntilOriginalProcessAcknowledgesShutdown(t *testing.T) {
	s := newStore(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	m, files := packageFixture(payload)
	// Windows LookPath requires an executable extension even when the payload
	// is already an absolute path to the original test executable.
	if runtime.GOOS == "windows" {
		delete(files, m.Executable)
		m.Executable = "bin/agent.exe"
		files[m.Executable] = payload
		for i := range m.Files {
			if m.Files[i].Executable {
				m.Files[i].Path = m.Executable
			}
		}
	}
	raw := zipFixture(t, m, files, nil)
	if _, err = s.Install(context.Background(), bytes.NewReader(raw), digestBytes(raw)); err != nil {
		t.Fatal(err)
	}
	l, err := s.Acquire(context.Background(), m.Key, m.Version, digestBytes(raw))
	if err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(t.TempDir(), "shutdown")
	t.Setenv("NYADB_DRIVERSTORE_NATIVE_HELPER", "1")
	t.Setenv("NYADB_DRIVERSTORE_SHUTDOWN_GATE", gate)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := l.Start(ctx, "fixture-v1", agentbridge.Options{CancelWait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(gate, []byte("release"), 0600); _ = r.Close() })
	if err = l.Close(); !errors.Is(err, ErrBusy) {
		t.Fatal("active process released its artifacts", err)
	}
	done := make(chan error, 1)
	go func() { done <- r.Close() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = os.Stat(gate + ".started"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("original shutdown was not dispatched")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = s.Remove(ctx, m.Key, m.Version); !errors.Is(err, ErrBusy) {
		t.Fatal("shutdown request released package before acknowledgement", err)
	}
	select {
	case err := <-done:
		t.Fatal("close completed before original acknowledgement", err)
	default:
	}
	if err = os.WriteFile(gate, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("original process was not reaped")
	}
	if r.Client.Alive() {
		t.Fatal("runtime still alive after close")
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Remove(ctx, m.Key, m.Version); err != nil {
		t.Fatal(err)
	}
}
