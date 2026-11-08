package connections_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCassandraPublicProfileURIAndExactPrincipal(t *testing.T) {
	p, err := connections.Defaults(connections.Cassandra)
	if err != nil || p.Port != 9042 || p.TLSMode != "verify-full" || p.CassandraOptions.Version != 1 || p.CassandraOptions.Auth != "none" {
		t.Fatal("native defaults lost")
	}
	d, ok := connections.Describe(connections.Cassandra)
	if !ok || d.ClientFamily != "cassandra" || d.Dialect != "cassandra" || d.DefaultPort != 9042 || !d.Capabilities.Query || !d.Capabilities.Metadata || !d.Capabilities.Export || d.Capabilities.EditableTables || d.Capabilities.Explain || d.Capabilities.Transactions || d.Capabilities.Import {
		t.Fatal("public native capability boundary")
	}
	for _, raw := range []string{
		"cassandra://localhost/",
		"cassandra://reader:private%40secret@[::1]:9043/MixedCase?sslmode=disable",
		"cassandra://%20native%20:space%20secret%20@localhost/app",
	} {
		parsed, err := connections.ParseCassandraURI(raw)
		if err != nil {
			t.Fatal("valid private URI rejected", err)
		}
		if parsed.Profile.Kind != connections.Cassandra || strings.Contains(fmt.Sprint(parsed), "secret") || strings.Contains(fmt.Sprintf("%#v", parsed), "secret") {
			t.Fatal("native identity or redaction lost")
		}
		if public, err := connections.ParseURI(raw); err != nil || !reflect.DeepEqual(public, parsed) {
			t.Fatal("public URI differs from pure native parser")
		}
		if strings.Contains(raw, "MixedCase") && (parsed.Profile.Database != "MixedCase" || parsed.Password != "private@secret" || parsed.Profile.Host != "::1") {
			t.Fatal("qualified case or IPv6 lost")
		}
		if strings.Contains(raw, "native") && (parsed.Profile.Username != " native " || parsed.Password != "space secret ") {
			t.Fatal("native principal/password trimmed")
		}
	}
	p.Name = "native"
	p.CassandraOptions.Auth = "password"
	p.Username = " 用户 "
	p.Database = "MixedCase"
	p.CassandraOptions.CAFile = filepath.Join(t.TempDir(), "ca.pem")
	checked, err := connections.NormalizeProfile(p)
	if err != nil || checked.Username != p.Username || checked.Database != p.Database {
		t.Fatal("profile canonicalized native identifiers")
	}
	other, _ := connections.Defaults(connections.PostgreSQL)
	other.Name = "foreign"
	other.CassandraOptions = p.CassandraOptions
	if _, err := connections.NormalizeProfile(other); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign native options accepted")
	}
}

func TestCassandraPrivateURIRejectsUnsupportedOrAmbiguousSemantics(t *testing.T) {
	for _, raw := range []string{
		"cassandra://reader:private@localhost:0/app", "cassandra://reader:private@localhost:65536/app", "cassandra://reader:private@first,second/app",
		"cassandra://reader:private@localhost/app?auth=none", "cassandra://localhost/app?auth=password", "cassandra://localhost/app?auth=basic",
		"cassandra://localhost/app?sslmode=require", "cassandra://localhost/app?sslmode=disable&server_name=localhost",
		"cassandra://localhost/app?auth=none&auth=password", "cassandra://localhost/app?AUTH=none", "cassandra://localhost/app?unknown=private",
		"cassandra://localhost/app/other", "cassandra://localhost/..", "cassandra://localhost/a%20b", "cassandra://localhost/app#private", "cassandra:localhost", "cassandra://localhost/app?",
		"cassandra://localhost/" + strings.Repeat("a", 49),
	} {
		v, err := connections.ParseCassandraURI(raw)
		if err == nil || v.Profile.Kind != "" || v.Password != "" || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid native URI accepted or leaked")
		}
	}
}
