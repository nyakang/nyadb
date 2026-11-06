package connections_test

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestConnectionURIImportSeparatesSecretsAndPreservesEngineSettings(t *testing.T) {
	for _, spec := range []struct {
		uri                                 string
		kind                                connections.Kind
		host, user, database, tls, password string
		port                                uint16
	}{
		{"mysql://alice:p%40ss%3Aword@[::1]:3307/db%2Fone?sslmode=verify-full", connections.MySQL, "::1", "alice", "db/one", "verify-full", "p@ss:word", 3307},
		{"postgresql://alice:secret@db.example:5433/research?sslmode=verify-ca", connections.PostgreSQL, "db.example", "alice", "research", "verify-ca", "secret", 5433},
		{"postgres://db.example", connections.PostgreSQL, "db.example", "postgres", "postgres", "", "", 5432},
		{"mysql://db.example", connections.MySQL, "db.example", "root", "", "", "", 3306},
		{"mariadb://db.example", connections.MariaDB, "db.example", "root", "", "", "", 3306},
		{"sqlite:///tmp/user%20database.sqlite", connections.SQLite, "", "", "/tmp/user database.sqlite", "", "", 0},
	} {
		if runtime.GOOS == "windows" && spec.kind == connections.SQLite {
			spec.uri = "sqlite:///C:/tmp/user%20database.sqlite"
			spec.database = `C:\tmp\user database.sqlite`
		}
		got, err := connections.ParseURI(spec.uri)
		if err != nil || got.Profile.Kind != spec.kind || got.Profile.Host != spec.host || got.Profile.Port != spec.port || got.Profile.Username != spec.user || got.Profile.Database != spec.database || got.Profile.TLSMode != spec.tls || got.Password != spec.password {
			t.Fatalf("URI engine mapping failed for %s: %v", spec.kind, err)
		}
		encoded, _ := json.Marshal(got)
		if spec.password != "" && (strings.Contains(string(encoded), spec.password) || strings.Contains(fmt.Sprintf("%+v %#v", got, got), spec.password)) {
			t.Fatal("formatted import leaked its password")
		}
	}
}
func TestConnectionURIRejectsUnsupportedOrAmbiguousInputWithoutEchoingSecrets(t *testing.T) {
	for _, uri := range []string{
		"mysql://user:private-password@host:0/db", "mysql://user:private-password@host:65536/db", "mysql://user:private-password@host:/db",
		"mysql://user:private-password@host:abc/db", "mysql://user:private-password@::1/db", "mysql://user:private-password@host/db/path",
		"mysql://user:private-password@host/db?sslmode=prefer", "mysql://user:private-password@host/db?sslmode=disable&sslmode=require",
		"postgres://user:private-password@host/db?password=other", "postgres://user:private-password@host/db#fragment",
		"sqlite://user:private-password@host/tmp/file", "sqlite:///tmp/file?mode=ro", "sqlite:relative.sqlite",
		"unknown://user:private-password@host/db", "mysql://user:private-password%00@host/db", "mysql://user:private-password@host/%00",
		"mysql://user:private-password%XX@host/db",
	} {
		got, err := connections.ParseURI(uri)
		if err == nil || got.Password != "" || got.Profile.Kind != "" || strings.Contains(err.Error(), "private-password") {
			t.Fatal("invalid URI accepted or leaked a password")
		}
	}
}
