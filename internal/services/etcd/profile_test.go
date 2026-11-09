package etcd

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestProfileRejectsDeferredCredentialAndForeignOptionsBeforeOpening(t *testing.T) {
	p, _ := connections.Defaults(connections.Etcd)
	p.Name = "native"
	p.EtcdOptions.Auth, p.Username = "basic", " exact "
	if c, err := OpenProfile(context.Background(), p, ""); err == nil || c != nil {
		t.Fatal("missing credential dispatched native open")
	}
	p.PostgreSQLOptions = connections.DefaultPostgreSQLOptions()
	if c, err := OpenProfile(context.Background(), p, "secret"); err == nil || c != nil {
		t.Fatal("foreign options dispatched native open")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c, err := OpenProfile(ctx, p, "secret"); !errors.Is(err, context.Canceled) || c != nil {
		t.Fatal("canceled original context ignored")
	}
}
