package connections_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestElasticsearchProfileAuthAndQualifiedEndpoint(t *testing.T) {
	p, err := connections.Defaults(connections.Elasticsearch)
	p.Name = "native search"
	if err != nil || p.Port != 9200 || p.Username != "elastic" || p.Database != "" || p.TLSMode != "verify-full" || p.Kind.IsMySQLFamily() || p.Kind.IsPostgreSQLFamily() {
		t.Fatal("invalid independent REST defaults")
	}
	p.Host = "::1"
	p.ElasticsearchOptions.CAFile = filepath.Join(t.TempDir(), "ca.pem")
	p.ElasticsearchOptions.ServerName = "localhost"
	cfg, err := connections.ElasticsearchProfileConfig(p)
	if err != nil || cfg.Endpoint != "https://[::1]:9200" || cfg.Auth != "basic" || cfg.CAFile != p.ElasticsearchOptions.CAFile {
		t.Fatal("qualified native configuration lost", err)
	}
	for _, auth := range []string{"basic", "api_key", "none"} {
		t.Run(auth, func(t *testing.T) {
			q := p
			q.ElasticsearchOptions.Auth = auth
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
		func(p *connections.Profile) { p.ElasticsearchOptions.Auth = "api_key" },
		func(p *connections.Profile) { p.ElasticsearchOptions.Version = 2 },
		func(p *connections.Profile) { p.ElasticsearchOptions.CAFile = "relative.pem" },
	} {
		q := p
		mutate(&q)
		if _, err := connections.NormalizeProfile(q); err == nil {
			t.Fatal("unsupported profile semantics accepted")
		}
	}
	q, _ := connections.Defaults(connections.PostgreSQL)
	q.Name = "foreign"
	q.ElasticsearchOptions = p.ElasticsearchOptions
	if _, err := connections.NormalizeProfile(q); err == nil {
		t.Fatal("Elasticsearch options accepted on another engine")
	}
}

func TestElasticsearchURITemporaryCredentialsAndRejectedSemantics(t *testing.T) {
	for _, tc := range []struct{ uri, auth, secret, endpoint string }{
		{"elasticsearch://reader:private%3Apassword@[::1]:9243?server_name=localhost", "basic", "private:password", "https://[::1]:9243"},
		{"elasticsearch://:aWQ6a2V5@localhost?auth=api_key", "api_key", "aWQ6a2V5", "https://localhost:9200"},
		{"elasticsearch://localhost?auth=none&sslmode=disable", "none", "", "http://localhost:9200"},
	} {
		parsed, err := connections.ParseURI(tc.uri)
		if err != nil || parsed.Password != tc.secret || parsed.Profile.ElasticsearchOptions.Auth != tc.auth {
			t.Fatal("URI lost authentication or temporary secret", err)
		}
		cfg, err := connections.ElasticsearchProfileConfig(parsed.Profile)
		if err != nil || cfg.Endpoint != tc.endpoint {
			t.Fatal("URI lost endpoint", err)
		}
		public, err := json.Marshal(parsed)
		if err != nil || tc.secret != "" && (strings.Contains(string(public), tc.secret) || strings.Contains(fmt.Sprintf("%#v", parsed), tc.secret)) {
			t.Fatal("URI secret exposed in public representation")
		}
	}
	for _, uri := range []string{
		"elasticsearch://reader:private@localhost?sslmode=disable",
		"elasticsearch://reader:private@localhost?auth=api_key",
		"elasticsearch://:private@localhost?auth=api_key",
		"elasticsearch://reader:private@localhost?auth=none",
		"elasticsearch://localhost/index", "elasticsearch://localhost/proxy/",
		"elasticsearch://localhost?password=private", "elasticsearch://localhost?api_key=private",
		"elasticsearch://localhost?auth=basic&auth=none", "elasticsearch://localhost?auth=",
		"elasticsearch://localhost?sslmode=require", "elasticsearch://localhost?ca_file=relative.pem",
		"elasticsearch://localhost:0", "elasticsearch://localhost:65536", "elasticsearch://localhost:",
		"elasticsearch://::1:9200", "elasticsearch://localhost#private", "elasticsearch://localhost?",
	} {
		parsed, err := connections.ParseURI(uri)
		if err == nil || parsed.Password != "" || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid URI accepted or secret included in error")
		}
	}
}

func TestElasticsearchCredentialOptionalSaveAndRequiredConnect(t *testing.T) {
	for _, auth := range []string{"basic", "api_key"} {
		cfg := connections.ElasticsearchConfig{Auth: auth}
		if connections.ValidateElasticsearchCredential(cfg, "", false) != nil || connections.ValidateElasticsearchCredential(cfg, "", true) == nil {
			t.Fatal("save and connect credential requirements conflated")
		}
	}
	for _, secret := range []string{"invalid", "OmtleQ==", "aWQ6", "aWQ6a2V5\n", strings.Repeat("x", 4097)} {
		if connections.ValidateElasticsearchCredential(connections.ElasticsearchConfig{Auth: "api_key"}, secret, false) == nil {
			t.Fatal("malformed encoded API key accepted")
		}
	}
	if connections.ValidateElasticsearchCredential(connections.ElasticsearchConfig{Auth: "none"}, "private", false) == nil {
		t.Fatal("secret accepted for no authentication")
	}
}
