package connections_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func damengProfile(t *testing.T) connections.Profile {
	t.Helper()
	p, err := connections.Defaults(connections.Dameng)
	if err != nil {
		t.Fatal(err)
	}
	p.Name, p.TLSMode = "DM8 profile", "disable"
	p.DamengOptions.LibraryFile = filepath.Join(t.TempDir(), "libdmdpi.so")
	return p
}

func TestDamengProfilePreservesQualifiedIdentityAndIndependentSettings(t *testing.T) {
	p := damengProfile(t)
	p.Host, p.Username, p.Database = " ::1 ", " 用户 ' \" ", " Catalog "
	p.DamengOptions.DefaultSchema = " Schema ' \" "
	before := p
	got, err := connections.NormalizeProfile(p)
	if err != nil || got.Host != "::1" || got.Username != p.Username || got.Database != p.Database || got.DamengOptions != p.DamengOptions || !reflect.DeepEqual(p, before) {
		t.Fatal("native identity/settings changed", err)
	}
	cfg, err := connections.DamengProfileConfig(got)
	if err != nil || cfg.Username != p.Username || cfg.Database != p.Database || cfg.Options != p.DamengOptions {
		t.Fatal("profile did not resolve its explicit native configuration", err)
	}
	defaults, _ := connections.Defaults(connections.Dameng)
	if defaults.Port != 5236 || defaults.Database != "DAMENG" || defaults.Username != "SYSDBA" || defaults.TLSMode != "verify-full" || defaults.DamengOptions != connections.DefaultDamengOptions() || defaults.Kind.IsMySQLFamily() || defaults.Kind.IsPostgreSQLFamily() {
		t.Fatal("native defaults/family changed")
	}
	if _, err = connections.NormalizeProfile(defaults); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("incomplete native library/certificate defaults were accepted")
	}
}

func TestDamengProfileRejectsForeignOrIncompleteSettings(t *testing.T) {
	for _, change := range []func(*connections.Profile){
		func(p *connections.Profile) { p.Port = 0 },
		func(p *connections.Profile) { p.DamengOptions.LibraryFile = "" },
		func(p *connections.Profile) { p.DamengOptions.Version = 0 },
		func(p *connections.Profile) { p.MySQLAdvanced = connections.DefaultMySQLAdvanced() },
		func(p *connections.Profile) { p.PostgreSQLOptions = connections.DefaultPostgreSQLOptions() },
		func(p *connections.Profile) { p.GaussDBOptions = connections.DefaultGaussDBOptions() },
		func(p *connections.Profile) { p.OracleOptions = connections.DefaultOracleOptions() },
		func(p *connections.Profile) { p.SQLiteOptions.ReadOnly = true },
		func(p *connections.Profile) { p.ActiveVariant = "alternate" },
		func(p *connections.Profile) { p.Variants = []connections.ConnectionVariant{{Name: "alternate"}} },
	} {
		p := damengProfile(t)
		change(&p)
		if _, err := connections.NormalizeProfile(p); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("unsupported native profile accepted", err)
		}
	}
	for _, kind := range []connections.Kind{connections.MySQL, connections.PostgreSQL, connections.GaussDB, connections.Kingbase, connections.SQLite, connections.Oracle, connections.Redis} {
		p, _ := connections.Defaults(kind)
		p.Name, p.DamengOptions = "foreign", connections.DefaultDamengOptions()
		if _, err := connections.NormalizeProfile(p); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("native options accepted on another engine", kind)
		}
		if _, err := connections.DamengProfileConfig(p); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("another engine selected a native DM8 connector", kind)
		}
	}
}

func TestDamengURIImportsOnlyExplicitNativeSettingsAndTemporarySecret(t *testing.T) {
	for _, scheme := range []string{"dameng", "DM"} {
		library := filepath.Join(t.TempDir(), "native client", "libdmdpi.so")
		q := url.Values{"library_file": {library}, "sslmode": {"disable"}, "schema": {" Schema ' \" "}, "connect_timeout_seconds": {"3"}, "operation_timeout_seconds": {"7"}, "max_connections": {"2"}, "sql_max_connections": {"4"}}
		u := url.URL{Scheme: scheme, Host: "[::1]:5237", User: url.UserPassword(" 用户 ", "private@secret"), Path: "/ Catalog ", RawQuery: q.Encode()}
		imported, err := connections.ParseURI(u.String())
		p := imported.Profile
		if err != nil || p.Kind != connections.Dameng || p.Host != "::1" || p.Port != 5237 || p.Username != " 用户 " || p.Database != " Catalog " || p.DamengOptions.LibraryFile != library || p.DamengOptions.DefaultSchema != q.Get("schema") || p.DamengOptions.ConnectTimeoutSeconds != 3 || p.DamengOptions.OperationTimeoutSeconds != 7 || p.DamengOptions.MaxConnections != 2 || p.DamengOptions.SQLMaxConnections != 4 || imported.Password != "private@secret" {
			t.Fatal("explicit native URI did not survive import", err)
		}
		raw, err := json.Marshal(imported)
		if err != nil || strings.Contains(string(raw), "private") || strings.Contains(fmt.Sprintf("%+v %#v", imported, imported), "private") {
			t.Fatal("temporary password escaped import draft", err)
		}
		q.Del("sslmode")
		for _, key := range []string{"ca_file", "certificate_file", "key_file"} {
			q.Set(key, filepath.Join(t.TempDir(), key+".pem"))
		}
		q.Set("server_name", "database.example")
		u.Host, u.RawQuery = "localhost", q.Encode()
		imported, err = connections.ParseURI(u.String())
		if err != nil || imported.Profile.Port != 5236 || imported.Profile.TLSMode != "verify-full" || imported.Profile.DamengOptions.ServerName != "database.example" {
			t.Fatal("default native TLS verification/certificate paths were lost", err)
		}
	}
}

func TestDamengURIRejectsAmbiguityAndRedactsFailures(t *testing.T) {
	q := url.Values{"library_file": {filepath.Join(t.TempDir(), "libdmdpi.so")}, "sslmode": {"disable"}}
	base := "dameng://reader:private%40secret@localhost/DAMENG?" + q.Encode()
	for _, uri := range []string{
		"dameng://reader:private@localhost/DAMENG", // A library and certificate identity are explicit.
		strings.Replace(base, "reader:private%40secret@", "", 1),
		strings.Replace(base, "localhost/", "localhost:0/", 1),
		strings.Replace(base, "localhost/", "localhost:65536/", 1),
		strings.Replace(base, "localhost/", "localhost:/", 1),
		strings.Replace(base, "localhost/", "::1/", 1),
		strings.Replace(base, "/DAMENG?", "?", 1),
		strings.Replace(base, "/DAMENG?", "/DAMENG/other?", 1),
		strings.Replace(base, "/DAMENG?", "/%00?", 1),
		strings.Replace(base, "reader:", "%0A:", 1),
		strings.Replace(base, "private%40secret", "private%00secret", 1),
		base + "#private", base + "&sslmode=require", base + "&schema=", base + "&password=private",
		base + "&max_connections=02", base + "&max_connections=65", base + "&operation_timeout_seconds=0",
		strings.Replace(base, "sslmode=disable", "sslmode=prefer", 1),
		strings.Replace(base, "sslmode=disable", "sslmode=verify-full", 1),
		base + "&ca_file=" + url.QueryEscape(filepath.Join(t.TempDir(), "ca.pem")),
	} {
		imported, err := connections.ParseURI(uri)
		if err == nil || imported.Password != "" || strings.Contains(err.Error(), "private") {
			t.Fatal("ambiguous URI accepted or error exposed a secret")
		}
	}
}
