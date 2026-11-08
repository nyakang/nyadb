package connections_test

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestTrinoProfileURIAndIndependentCapabilities(t *testing.T) {
	p, err := connections.Defaults(connections.Trino)
	p.Name = "native"
	if err != nil || p.Port != 8080 || p.Username == "" || p.TLSMode != "verify-full" || p.Kind.IsMySQLFamily() || p.Kind.IsPostgreSQLFamily() {
		t.Fatal("invalid native defaults")
	}
	if _, err = connections.NormalizeProfile(p); err != nil {
		t.Fatal(err)
	}
	d, ok := connections.Describe(p.Kind)
	if !ok || d.Dialect != "trino" || d.ClientFamily != "trino" || !d.Capabilities.Query || !d.Capabilities.Metadata || !d.Capabilities.Export || d.Capabilities.EditableTables || d.Capabilities.Transactions || d.Capabilities.Import || d.Capabilities.Accounts || d.Capabilities.Backup {
		t.Fatal("unverified connector capabilities advertised")
	}
	caFile := filepath.Join(t.TempDir(), "ca with spaces.pem")
	u, err := connections.ParseURI("trino://reader:temporary@[::1]:8443/memory/default?sslmode=verify-full&ca_file=" + url.QueryEscape(caFile) + "&server_name=localhost")
	if err != nil || u.Password != "temporary" || u.Profile.Kind != p.Kind || u.Profile.Host != "::1" || u.Profile.Database != "memory" || u.Profile.TrinoOptions.DefaultSchema != "default" || u.Profile.TrinoOptions.CAFile != caFile {
		t.Fatal("qualified URI or temporary secret lost", err)
	}
	for _, uri := range []string{
		"trino://reader:secret@localhost?sslmode=disable", "trino://localhost?schema=default",
		"trino://localhost/memory/default?schema=other", "trino://localhost?password=secret",
		"trino://localhost?sslmode=require", "trino://localhost?ca_file=relative.pem",
		"trino://localhost?schema=a&schema=b", "trino://localhost/a/b/c",
	} {
		if _, err := connections.ParseURI(uri); err == nil {
			t.Fatalf("invalid URI accepted: %s", uri)
		}
	}
	p.Database = strings.Repeat("x", 257)
	if _, err := connections.NormalizeProfile(p); err == nil {
		t.Fatal("oversized catalog accepted")
	}
	other, _ := connections.Defaults(connections.PostgreSQL)
	other.Name = "other"
	other.TrinoOptions = u.Profile.TrinoOptions
	if _, err := connections.NormalizeProfile(other); err == nil {
		t.Fatal("Trino options accepted by another engine")
	}
}
