package connections_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCouchDBConfigPreservesContextAndCredentialBytes(t *testing.T) {
	for _, tc := range []struct{ in, out string }{{"http://localhost:5984/", "http://localhost:5984"}, {"https://localhost:6984/proxy/couchdb/", "https://localhost:6984/proxy/couchdb"}, {"http://[::1]:5984", "http://[::1]:5984"}} {
		c, err := connections.NormalizeCouchDBConfig(connections.CouchDBConfig{Endpoint: tc.in})
		if err != nil || c.Endpoint != tc.out || c.Auth != "none" {
			t.Fatal("native endpoint changed", err)
		}
	}
	valid := connections.CouchDBConfig{Endpoint: "http://localhost:5984", Auth: "basic", Username: " 原生用户🐱 "}
	normal, err := connections.NormalizeCouchDBConfig(valid)
	if err != nil || normal.Username != valid.Username {
		t.Fatal("Basic principal changed", err)
	}
	for _, password := range []string{"secret", " password\t", "\u00a0密码🐱\u00a0"} {
		if err := connections.ValidateCouchDBCredential(normal, password, true); err != nil {
			t.Fatal("valid password bytes rejected", err)
		}
	}
	if err := connections.ValidateCouchDBCredential(normal, "", false); err != nil {
		t.Fatal("optional editing credential rejected", err)
	}
	for _, change := range []func(*connections.CouchDBConfig){
		func(c *connections.CouchDBConfig) { c.Endpoint = "http://user:password@localhost:5984" },
		func(c *connections.CouchDBConfig) { c.Endpoint += "?password=secret" },
		func(c *connections.CouchDBConfig) { c.Endpoint += "/../other" },
		func(c *connections.CouchDBConfig) { c.Endpoint += "/%2e%2e/other" },
		func(c *connections.CouchDBConfig) { c.Endpoint += "/%00" },
		func(c *connections.CouchDBConfig) { c.Endpoint += "//other" },
		func(c *connections.CouchDBConfig) { c.Endpoint = "http://localhost:0" },
		func(c *connections.CouchDBConfig) { c.CAFile = filepath.Join(t.TempDir(), "ca.pem") },
		func(c *connections.CouchDBConfig) { c.ServerName = "localhost" },
		func(c *connections.CouchDBConfig) { c.Endpoint = "https://localhost:5984"; c.CAFile = "relative.pem" },
		func(c *connections.CouchDBConfig) { c.Auth = "cookie" },
		func(c *connections.CouchDBConfig) { c.Auth = "none" },
		func(c *connections.CouchDBConfig) { c.Username = "a:b" },
	} {
		c := valid
		change(&c)
		if _, err := connections.NormalizeCouchDBConfig(c); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("ambiguous configuration accepted")
		}
	}
	for _, password := range []string{"", "has\x00null", string([]byte{0xff})} {
		if err := connections.ValidateCouchDBCredential(normal, password, true); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("invalid credential accepted")
		}
	}
	if err := connections.ValidateCouchDBCredential(connections.CouchDBConfig{Auth: "none"}, "stray", false); err == nil {
		t.Fatal("anonymous config retained a password")
	}
}
