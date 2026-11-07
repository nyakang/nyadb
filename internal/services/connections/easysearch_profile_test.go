package connections_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestEasysearchProfileAuthAndQualifiedEndpoint(t *testing.T) {
	p, err := connections.Defaults(connections.Easysearch)
	p.Name = "native search"
	if err != nil || p.Port != 9200 || p.Username != "admin" || p.Database != "" || p.TLSMode != "verify-full" || p.Kind.IsMySQLFamily() || p.Kind.IsPostgreSQLFamily() {
		t.Fatal("invalid independent REST defaults")
	}
	p.Host = "::1"
	p.EasysearchOptions.CAFile = filepath.Join(t.TempDir(), "ca.pem")
	p.EasysearchOptions.ServerName = "localhost"
	cfg, err := connections.EasysearchProfileConfig(p)
	if err != nil || cfg.Endpoint != "https://[::1]:9200" || cfg.Auth != "basic" || cfg.CAFile != p.EasysearchOptions.CAFile {
		t.Fatal("qualified native configuration lost", err)
	}
	for _, auth := range []string{"basic", "access_token", "none"} {
		t.Run(auth, func(t *testing.T) {
			q := p
			q.EasysearchOptions.Auth = auth
			if auth != "basic" {
				q.Username = ""
			}
			if _, err := connections.NormalizeProfile(q); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, mutate := range []func(*connections.Profile){
		func(p *connections.Profile) { p.Database = "index" },
		func(p *connections.Profile) { p.MySQLAdvanced = connections.DefaultMySQLAdvanced() },
		func(p *connections.Profile) { p.TrinoOptions.Version = 1 },
		func(p *connections.Profile) { p.Host = "https://localhost" },
		func(p *connections.Profile) { p.Host = "a:9200" },
		func(p *connections.Profile) { p.Port = 0 },
		func(p *connections.Profile) { p.TLSMode = "require" },
		func(p *connections.Profile) { p.TLSMode = "disable" },
		func(p *connections.Profile) { p.EasysearchOptions.Auth = "access_token" },
		func(p *connections.Profile) { p.EasysearchOptions.Version = 2 },
		func(p *connections.Profile) { p.EasysearchOptions.CAFile = "relative.pem" },
	} {
		q := p
		mutate(&q)
		if _, err := connections.NormalizeProfile(q); err == nil {
			t.Fatal("unsupported profile semantics accepted")
		}
	}
	q, _ := connections.Defaults(connections.PostgreSQL)
	q.Name = "foreign"
	q.EasysearchOptions = p.EasysearchOptions
	if _, err := connections.NormalizeProfile(q); err == nil {
		t.Fatal("Easysearch options accepted on another engine")
	}
}

func TestEasysearchURITemporaryCredentialsAndRejectedSemantics(t *testing.T) {
	for _, tc := range []struct{ uri, auth, secret, endpoint string }{
		{"easysearch://reader:private%3Apassword@[::1]:9243?server_name=localhost", "basic", "private:password", "https://[::1]:9243"},
		{"easysearch://:native-opaque-token@localhost?auth=access_token", "access_token", "native-opaque-token", "https://localhost:9200"},
		{"easysearch://localhost?auth=none&sslmode=disable", "none", "", "http://localhost:9200"},
	} {
		parsed, err := connections.ParseURI(tc.uri)
		if err != nil || parsed.Password != tc.secret || parsed.Profile.EasysearchOptions.Auth != tc.auth {
			t.Fatal("URI lost authentication or temporary secret", err)
		}
		cfg, err := connections.EasysearchProfileConfig(parsed.Profile)
		if err != nil || cfg.Endpoint != tc.endpoint {
			t.Fatal("URI lost endpoint", err)
		}
		public, err := json.Marshal(parsed)
		if err != nil || tc.secret != "" && (strings.Contains(string(public), tc.secret) || strings.Contains(fmt.Sprintf("%#v", parsed), tc.secret)) {
			t.Fatal("URI secret exposed in public representation")
		}
	}
	for _, uri := range []string{
		"easysearch://reader:private@localhost?sslmode=disable",
		"easysearch://reader:private@localhost?auth=access_token",
		"easysearch://:private@localhost?auth=api_key",
		"easysearch://reader:private@localhost?auth=none",
		"easysearch://localhost/index", "easysearch://localhost/proxy/",
		"easysearch://localhost?password=private", "easysearch://localhost?access_token=private", "easysearch://localhost?api_key=private",
		"easysearch://localhost?auth=basic&auth=none", "easysearch://localhost?auth=",
		"easysearch://localhost?sslmode=require", "easysearch://localhost?ca_file=relative.pem",
		"easysearch://localhost:0", "easysearch://localhost:65536", "easysearch://localhost:",
		"easysearch://::1:9200", "easysearch://localhost#private", "easysearch://localhost?",
	} {
		parsed, err := connections.ParseURI(uri)
		if err == nil || parsed.Password != "" || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid URI accepted or secret included in error")
		}
	}
}

func TestEasysearchCredentialOptionalSaveAndRequiredConnect(t *testing.T) {
	for _, auth := range []string{"basic", "access_token"} {
		cfg := connections.EasysearchConfig{Auth: auth}
		if connections.ValidateEasysearchCredential(cfg, "", false) != nil || connections.ValidateEasysearchCredential(cfg, "", true) == nil {
			t.Fatal("save and connect credential requirements conflated")
		}
	}
	for _, secret := range []string{"token with spaces", "令牌", "native-token\n", strings.Repeat("x", 4097)} {
		if connections.ValidateEasysearchCredential(connections.EasysearchConfig{Auth: "access_token"}, secret, false) == nil {
			t.Fatal("malformed native header token accepted")
		}
	}
	if connections.ValidateEasysearchCredential(connections.EasysearchConfig{Auth: "none"}, "private", false) == nil {
		t.Fatal("secret accepted for no authentication")
	}
}
