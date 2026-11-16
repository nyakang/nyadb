package zookeeper_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/zookeeper"
)

func TestPublicProviderRequiresOriginalProductCredentialAndInstalledPin(t *testing.T) {
	started := false
	provider := zookeeper.Provider{Started: func(int) error { started = true; return nil }}
	p, _ := connections.Defaults(connections.ZooKeeper)
	p.ID, p.Name = 7, "original"
	if s, err := provider.Open(context.Background(), p, ""); s != nil || !errors.Is(err, zookeeper.ErrInvalid) {
		t.Fatal("missing package pin was accepted", err)
	}
	p.ZooKeeperOptions.DriverVersion, p.ZooKeeperOptions.DriverSHA256, p.ZooKeeperOptions.DriverDirectory = "missing-original", strings.Repeat("a", 64), t.TempDir()
	if s, err := provider.Open(context.Background(), p, ""); s != nil || !errors.Is(err, zookeeper.ErrInvalid) {
		t.Fatal("missing installed original was replaced", err)
	}
	if s, err := provider.Open(context.Background(), p, "private"); s != nil || err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("no-auth profile accepted hidden credential")
	}
	p.Kind = connections.MySQL
	if s, err := provider.Open(context.Background(), p, ""); s != nil || err == nil {
		t.Fatal("foreign product accepted")
	}
	if started {
		t.Fatal("invalid public configuration started a native process")
	}
}
