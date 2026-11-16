package driverstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func installedSingleLease(t *testing.T) (*Store, *Lease, Manifest) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	m, files := packageFixture(payload)
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
	store := newStore(t)
	raw := zipFixture(t, m, files, nil)
	digest := digestBytes(raw)
	if _, err := store.Install(context.Background(), bytes.NewReader(raw), digest); err != nil {
		t.Fatal(err)
	}
	l, err := store.Acquire(context.Background(), m.Key, m.Version, digest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return store, l, m
}

func TestSingleSessionRuntimeKeepsOriginalPackageUntilShutdownExit(t *testing.T) {
	store, l, m := installedSingleLease(t)
	t.Setenv("NYADB_DRIVERSTORE_NATIVE_HELPER", "1")
	t.Setenv("NYADB_DRIVERSTORE_PROTOCOL", "1")
	gate := filepath.Join(t.TempDir(), "shutdown")
	t.Setenv("NYADB_DRIVERSTORE_SHUTDOWN_GATE", gate)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	observed := 0
	r, err := l.StartSingleSessionObserved(ctx, "fixture-v1", agentbridge.Options{CancelWait: 5 * time.Second}, func(pid int) error { observed = pid; return nil })
	if err != nil || observed == 0 || r.Session.PID() != observed {
		t.Fatal("captured single process unavailable", err)
	}
	t.Cleanup(func() { _ = os.WriteFile(gate, []byte("release"), 0600); _ = r.Close() })
	if _, err := r.Session.Connect(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); !errors.Is(err, ErrBusy) {
		t.Fatal("active original package released", err)
	}
	done := make(chan error, 1)
	go func() { done <- r.Close() }()
	for {
		if _, err := os.Stat(gate + ".started"); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("original shutdown did not start")
		case <-time.After(time.Millisecond):
		}
	}
	if err := store.Remove(ctx, m.Key, m.Version); !errors.Is(err, ErrBusy) {
		t.Fatal("pending shutdown released its original package", err)
	}
	select {
	case err := <-done:
		t.Fatal("close returned before original shutdown", err)
	default:
	}
	if err := os.WriteFile(gate, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("original process was not reaped")
	}
	select {
	case <-r.Session.Exited():
	default:
		t.Fatal("package released before process exit")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.StartSingleSession(ctx, "fixture-v1", agentbridge.Options{}); !errors.Is(err, ErrBusy) {
		t.Fatal("consumed lease restarted", err)
	}
	if err := store.Remove(ctx, m.Key, m.Version); err != nil {
		t.Fatal(err)
	}
}

func TestSingleSessionRuntimeRejectsV2AndReleasesOnlyFailedOriginalLease(t *testing.T) {
	store, l, m := installedSingleLease(t)
	t.Setenv("NYADB_DRIVERSTORE_NATIVE_HELPER", "1")
	t.Setenv("NYADB_DRIVERSTORE_PROTOCOL", "2")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := l.StartSingleSession(ctx, "fixture-v1", agentbridge.Options{}); !errors.Is(err, agentbridge.ErrSingleVersion) {
		t.Fatal("v2 fallback admitted", err)
	}
	if l.active || !l.consumed || !l.closed {
		t.Fatal("failed captured runtime retained its package")
	}
	if err := store.Remove(ctx, m.Key, m.Version); err != nil {
		t.Fatal(err)
	}
}

func TestSingleSessionRuntimeRejectsJavaBeforeConsumingOriginalLease(t *testing.T) {
	store := newStore(t)
	m, files := packageFixture([]byte("java executable"))
	m.Runtime = "java"
	m.MainClass = "com.example.Agent"
	m.Classpath = []string{"agent.jar"}
	files["agent.jar"] = []byte("jar fixture")
	m.Files = append(m.Files, File{Path: "agent.jar", SHA256: digestBytes(files["agent.jar"])})
	raw := zipFixture(t, m, files, nil)
	digest := digestBytes(raw)
	if _, err := store.Install(context.Background(), bytes.NewReader(raw), digest); err != nil {
		t.Fatal(err)
	}
	l, err := store.Acquire(context.Background(), m.Key, m.Version, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := l.StartSingleSessionObserved(context.Background(), "fixture-v1", agentbridge.Options{}, func(int) error { t.Error("Java executable started through v1 API"); return nil }); !errors.Is(err, ErrPackage) {
		t.Fatal("Java package admitted", err)
	}
	if _, err := l.StartSingleSession(nil, "fixture-v1", agentbridge.Options{}); !errors.Is(err, ErrPackage) {
		t.Fatal("nil context admitted", err)
	}
	if l.consumed || l.active {
		t.Fatal("invalid launch consumed the original lease")
	}
}
