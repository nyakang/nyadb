package connections_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestInvalidMySQLAdvancedRejected(t *testing.T) {
	store, _, _ := openStore(t)
	base := connections.Profile{Name: "settings", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	for _, invalid := range []func(*connections.MySQLAdvanced){
		func(a *connections.MySQLAdvanced) { a.ConnectTimeoutSeconds = 0 },
		func(a *connections.MySQLAdvanced) { a.KeepAliveSeconds = -1 },
		func(a *connections.MySQLAdvanced) { a.Charset = "invalid" },
		func(a *connections.MySQLAdvanced) { a.ClientDriver = "invalid" },
		func(a *connections.MySQLAdvanced) { a.MaxSessions = 10001 },
		func(a *connections.MySQLAdvanced) { a.PipeName = "bad\x00pipe" },
	} {
		p := base
		invalid(&p.MySQLAdvanced)
		if _, err := store.Create(context.Background(), p); !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("invalid MySQL advanced options accepted: %+v / %v", p.MySQLAdvanced, err)
		}
	}
	p := connections.Profile{Name: "sqlite", Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "local.db"),
		MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	if _, err := store.Create(context.Background(), p); !errors.Is(err, connections.ErrInvalid) {
		t.Fatalf("MySQL options allowed for SQLite: %v", err)
	}
}
