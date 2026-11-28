package connections_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestPrestoSQLPublicProfilePinTLSAndForeignSettings(t *testing.T) {
	p, err := connections.Defaults(connections.PrestoSQL)
	if err != nil || p.Port != 8080 || p.TLSMode != "disable" {
		t.Fatal("defaults", err)
	}
	p.Name = "original"
	if _, err = connections.NormalizeProfile(p); err != nil {
		t.Fatal(err)
	}
	if _, err = connections.PrestoSQLProfileManagedConfig(p); err == nil {
		t.Fatal("unselected package executable")
	}
	p.PrestoSQLOptions.DriverVersion = "jdbc-350"
	p.PrestoSQLOptions.DriverSHA256 = strings.Repeat("a", 64)
	p.Username, p.Database, p.PrestoSQLOptions.DefaultSchema = " user ", " catalog ", " schema "
	p.TLSMode = "verify-ca"
	p.PrestoSQLOptions.TrustStorePath = filepath.Join(t.TempDir(), "trust.p12")
	p.PrestoSQLOptions.TrustStoreType = "PKCS12"
	normalized, err := connections.NormalizeProfile(p)
	if err != nil || normalized.Username != p.Username || normalized.Database != p.Database {
		t.Fatal("original identity changed", err)
	}
	config, err := connections.PrestoSQLProfileManagedConfig(p)
	if err != nil || !strings.Contains(config.JDBCParameters, "SSLVerification=CA") || config.Endpoint != "https://localhost:8080" {
		t.Fatal("TLS mapping", err)
	}
	for _, mutate := range []func(*connections.Profile){
		func(p *connections.Profile) { p.PrestoSQLOptions.DriverSHA256 = "" },
		func(p *connections.Profile) { p.PrestoSQLOptions.DriverDirectory = "relative" },
		func(p *connections.Profile) { p.PrestoSQLOptions.Version = 2 },
		func(p *connections.Profile) { p.PrestoSQLOptions.JDBCParameters = "password=secret" },
		func(p *connections.Profile) { p.PrestoSQLOptions.JDBCParameters = "accessToken=secret" },
		func(p *connections.Profile) { p.PrestoSQLOptions.JDBCParameters = "SSLVerification=NONE" },
		func(p *connections.Profile) { p.PrestoSQLOptions.TrustStoreType = "unknown" },
		func(p *connections.Profile) { p.TLSMode = "disable" },
		func(p *connections.Profile) { p.TrinoOptions = connections.TrinoOptions{Version: 1} },
	} {
		bad := p
		mutate(&bad)
		if _, err := connections.NormalizeProfile(bad); err == nil {
			t.Fatal("invalid public settings accepted")
		}
	}
	foreign, _ := connections.Defaults(connections.PostgreSQL)
	foreign.PrestoSQLOptions = p.PrestoSQLOptions
	if _, err := connections.NormalizeProfile(foreign); err == nil {
		t.Fatal("foreign settings accepted")
	}
}

func TestPrestoSQLCredentialsExplicitCodecRejectsMalformedAndRedacts(t *testing.T) {
	c := connections.PrestoSQLCredentials{Password: "password +&= 猫", TrustStorePassword: "trust +%= 猫", KeyStorePassword: "key", AccessToken: "token", ExtraCredentials: "name:value"}
	encoded, err := connections.EncodePrestoSQLCredentials(c)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := connections.DecodePrestoSQLCredentials(encoded)
	if err != nil || decoded != c {
		t.Fatal("credential roundtrip", err)
	}
	raw, _ := json.Marshal(c)
	for _, out := range []string{string(raw), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c)} {
		if strings.Contains(out, c.Password) || strings.Contains(out, c.TrustStorePassword) {
			t.Fatal("diagnostic exposed credentials")
		}
	}
	envelope := `{"version":1,"password":"p","trust_store_password":"t","key_store_password":"k","access_token":"a","extra_credentials":"e"}`
	for _, raw := range []string{
		strings.Replace(envelope, `"version":1`, `"version":2`, 1),
		strings.Replace(envelope, `"password":"p"`, `"password":"p","password":"q"`, 1),
		strings.Replace(envelope, `"password":"p"`, `"password":null`, 1),
		strings.Replace(envelope, `"password":"p"`, `"Password":"p"`, 1),
		envelope + `{}`, strings.Replace(envelope, `"extra_credentials":"e"`, `"unknown":"e"`, 1),
	} {
		if _, err := connections.DecodePrestoSQLCredentials("nyadb-prestosql-credentials-v1:" + base64.RawStdEncoding.EncodeToString([]byte(raw))); err == nil {
			t.Fatal("malformed credential envelope accepted")
		}
	}
	if _, err := connections.DecodePrestoSQLCredentials("raw-password"); err == nil {
		t.Fatal("ambiguous plain password accepted")
	}
	huge := strings.Repeat("\x01", 65536)
	if _, err := connections.EncodePrestoSQLCredentials(connections.PrestoSQLCredentials{Password: huge, TrustStorePassword: huge}); err == nil {
		t.Fatal("encoder produced an unreadable oversized envelope")
	}
	c.AccessToken = "token&other=secret"
	if _, err := connections.EncodePrestoSQLCredentials(c); err == nil {
		t.Fatal("driver query delimiter accepted")
	}
}

func TestPrestoSQLURIPreservesOriginalJDBCSemanticsAndProductContext(t *testing.T) {
	trustStorePath := filepath.Join(t.TempDir(), "original trust+store.p12")
	jdbc := "jdbc:presto://localhost:8443/memory/default?user=reader&password=original+pass&SSL=true&SSLVerification=CA&SSLTrustStorePath=" + url.PathEscape(trustStorePath) + "&SSLTrustStorePassword=trust+pass&accessToken=original+token&extraCredentials=name:value&source=client+name"
	if _, err := connections.ParseURI(jdbc); err == nil {
		t.Fatal("ambiguous PrestoDB scheme selected PrestoSQL")
	}
	parsed, err := connections.ParsePrestoSQLURI(" " + jdbc + " ")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Profile.PrestoSQLOptions.TrustStorePath != trustStorePath {
		t.Fatal("original trust store path changed")
	}
	if parsed.Profile.Kind != connections.PrestoSQL || parsed.Profile.TLSMode != "verify-ca" || parsed.Profile.PrestoSQLOptions.DefaultSchema != "default" || parsed.Password != "original+pass" || parsed.PrestoSQLCredentials.TrustStorePassword != "trust+pass" || parsed.PrestoSQLCredentials.AccessToken != "original+token" || parsed.Profile.PrestoSQLOptions.JDBCParameters != "source=client%2Bname" {
		t.Fatal("original JDBC parsing changed")
	}
	raw, _ := json.Marshal(parsed.Profile)
	if strings.Contains(string(raw), "original+pass") || strings.Contains(string(raw), "trust+pass") || strings.Contains(string(raw), "original+token") {
		t.Fatal("URI credentials persisted in profile")
	}
	standard, err := connections.ParseURI("prestosql://reader:original%2Bpass@localhost/memory/default?sslmode=verify-full&source=client+name")
	if err != nil || standard.Password != "original+pass" || standard.Profile.PrestoSQLOptions.JDBCParameters != "source=client+name" {
		t.Fatal("standard URI semantics", err)
	}
	for _, raw := range []string{
		"prestosql://host:", "prestosql://host/a/b/c", "prestosql://host/a%2Fb/c", "prestosql://host/a/", "prestosql://host/a?user=a&user=b",
		"prestosql://host/a?driver_version=350", "prestosql://host/a?password=a&password=b", "prestosql://user:pass@host/a?password=another",
		"jdbc:presto://host/a?SSLVerification=NONE", "jdbc:presto://host/a?SSL=true&SSL=true", "jdbc:presto://host/a?source=hello%26other=world",
	} {
		if _, err := connections.ParsePrestoSQLURI(raw); err == nil {
			t.Fatal("ambiguous/unsupported URI accepted")
		}
	}
}
