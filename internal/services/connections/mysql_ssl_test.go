package connections_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMySQLSSLInvalidPathsRejected(t *testing.T) {
	store, _, _ := openStore(t)
	for _, modify := range []func(*connections.MySQLSSL){
		func(s *connections.MySQLSSL) { s.ClientKey = "/tmp/key\x00.pem" },
		func(s *connections.MySQLSSL) { s.ClientCertificate = strings.Repeat("x", 4097) },
		func(s *connections.MySQLSSL) { s.CACertificate = "bad\npath" },
	} {
		a := connections.DefaultMySQLAdvanced()
		modify(&a.SSL)
		_, err := store.Create(context.Background(), connections.Profile{
			Name: "bad-ssl", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: a,
		})
		if !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("invalid SSL path accepted: %v", err)
		}
	}
}
