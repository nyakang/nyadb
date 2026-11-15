package driverstore

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func TestJDBCSettingsRejectNativeOverflowAndKeepLeaseAvailable(t *testing.T) {
	for _, s := range []JDBCSettings{{}, {MaxConnections: 33, WaitTimeout: time.Second}, {MaxConnections: 8, MetadataReserve: 8, WaitTimeout: time.Second}, {MaxConnections: 8, MetadataReserve: -1, WaitTimeout: time.Second}, {MaxConnections: 8, WaitTimeout: 249 * time.Millisecond}, {MaxConnections: 8, WaitTimeout: 301 * time.Second}} {
		if _, err := s.arguments(); !errors.Is(err, ErrPackage) {
			t.Fatal("invalid native settings admitted")
		}
	}
	store := newStore(t)
	m, files := packageFixture([]byte("original native executable"))
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
	settings := JDBCSettings{MaxConnections: 4, MetadataReserve: 1, WaitTimeout: time.Second}
	if _, err := l.StartJDBCObserved(context.Background(), "fixture-v1", settings, agentbridge.Options{}, func(int) error { t.Error("invalid native package launched"); return nil }); !errors.Is(err, ErrPackage) {
		t.Fatal("JDBC settings accepted for a native executable", err)
	}
	if _, err := l.LaunchSpec("fixture-v1"); err != nil || l.consumed || l.active {
		t.Fatal("rejected settings consumed the original lease", err)
	}
}
