package connections_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSolrProfileContextAuthAndIndependentDefaults(t *testing.T) {
	p, err := connections.Defaults(connections.Solr)
	p.Name = "native Solr"
	if err != nil || p.Port != 8983 || p.Username != "" || p.Database != "" || p.TLSMode != "verify-full" || p.SolrOptions.Auth != "none" || p.SolrOptions.ContextPath != "/solr" || p.Kind.IsMySQLFamily() || p.Kind.IsPostgreSQLFamily() {
		t.Fatal("independent native defaults lost")
	}
	if _, registered := connections.Describe(connections.Solr); registered {
		t.Fatal("profile foundation prematurely registered public capabilities")
	}
	p.Host = "::1"
	p.SolrOptions.ContextPath = "/gateway/solr/"
	p.SolrOptions.CAFile = filepath.Join(t.TempDir(), "ca.pem")
	p.SolrOptions.ServerName = "localhost"
	p, err = connections.NormalizeProfile(p)
	if err != nil || p.SolrOptions.ContextPath != "/gateway/solr" {
		t.Fatal("proxy context lost", err)
	}
	cfg, err := connections.SolrProfileConfig(p)
	if err != nil || cfg.Endpoint != "https://[::1]:8983/gateway/solr" || cfg.Auth != "none" || cfg.CAFile != p.SolrOptions.CAFile {
		t.Fatal("qualified transport changed", err)
	}
	spaced := p
	spaced.Username, spaced.SolrOptions.Auth = "\u00a0native-user\u00a0", "basic"
	spaced, err = connections.NormalizeProfile(spaced)
	if err != nil || spaced.Username != "\u00a0native-user\u00a0" {
		t.Fatal("native Basic principal was silently trimmed", err)
	}
	for _, mutate := range []func(*connections.Profile){
		func(p *connections.Profile) { p.Database = "core" },
		func(p *connections.Profile) { p.MySQLAdvanced = connections.DefaultMySQLAdvanced() },
		func(p *connections.Profile) { p.EasysearchOptions = connections.DefaultEasysearchOptions() },
		func(p *connections.Profile) { p.Host = "https://localhost" },
		func(p *connections.Profile) { p.Host = "localhost:8983" },
		func(p *connections.Profile) { p.Port = 0 },
		func(p *connections.Profile) { p.TLSMode = "require" },
		func(p *connections.Profile) { p.TLSMode = "disable" },
		func(p *connections.Profile) { p.Username = "unexpected" },
		func(p *connections.Profile) { p.Username, p.SolrOptions.Auth = " reader ", "basic" },
		func(p *connections.Profile) { p.SolrOptions.Auth = "basic" },
		func(p *connections.Profile) { p.SolrOptions.Auth = "api_key" },
		func(p *connections.Profile) { p.SolrOptions.Version = 2 },
		func(p *connections.Profile) { p.SolrOptions.CAFile = "relative.pem" },
		func(p *connections.Profile) { p.SolrOptions.ContextPath = "/solr/../other" },
		func(p *connections.Profile) { p.SolrOptions.ContextPath = "/%2e%2e" },
		func(p *connections.Profile) { p.SolrOptions.ContextPath = "solr" },
		func(p *connections.Profile) { p.SolrOptions.ContextPath = "/solr?core=other" },
	} {
		q := p
		mutate(&q)
		if _, err := connections.NormalizeProfile(q); err == nil {
			t.Fatal("unsupported profile semantics accepted")
		}
	}
	q, _ := connections.Defaults(connections.PostgreSQL)
	q.Name, q.SolrOptions = "foreign", p.SolrOptions
	if _, err := connections.NormalizeProfile(q); err == nil {
		t.Fatal("Solr envelope accepted on foreign engine")
	}
}

func TestSolrURIPreservesContextAndTemporaryCredential(t *testing.T) {
	for _, tc := range []struct{ uri, auth, username, secret, endpoint string }{
		{"solr://reader:private%3Apassword@[::1]:8990/gateway/solr?server_name=localhost", "basic", "reader", "private:password", "https://[::1]:8990/gateway/solr"},
		{"solr://reader@localhost/solr?auth=basic", "basic", "reader", "", "https://localhost:8983/solr"},
		{"solr://%C2%A0native-user%C2%A0:private@localhost/solr", "basic", "\u00a0native-user\u00a0", "private", "https://localhost:8983/solr"},
		{"solr://localhost?auth=none&sslmode=disable", "none", "", "", "http://localhost:8983/solr"},
		{"solr://localhost/solr/", "none", "", "", "https://localhost:8983/solr"},
	} {
		parsed, err := connections.ParseURI(tc.uri)
		if err != nil || parsed.Password != tc.secret || parsed.Profile.SolrOptions.Auth != tc.auth || parsed.Profile.Username != tc.username || parsed.Profile.Database != "" {
			t.Fatal("URI context or credential lost", err)
		}
		cfg, err := connections.SolrProfileConfig(parsed.Profile)
		if err != nil || cfg.Endpoint != tc.endpoint {
			t.Fatal("URI endpoint changed", err)
		}
		public, err := json.Marshal(parsed)
		if err != nil || tc.secret != "" && (strings.Contains(string(public), tc.secret) || strings.Contains(fmt.Sprintf("%+v %#v", parsed, parsed), tc.secret)) {
			t.Fatal("temporary credential exposed")
		}
	}
	for _, uri := range []string{
		"solr://reader:private@localhost?sslmode=disable", "solr://reader:private@localhost?auth=none", "solr://localhost?auth=basic",
		"solr://%20reader%20:private@localhost", "solr://reader:%20private%20@localhost", "solr://reader:%09private@localhost",
		"solr://reader:private@localhost?auth=access_token", "solr://localhost?password=private", "solr://localhost?api_key=private",
		"solr://localhost/solr/../other", "solr://localhost/%2e%2e/other", "solr://localhost/%252e%252e", "solr://localhost//other",
		"solr://localhost/solr?context_path=/proxy/solr", "solr://localhost?auth=none&auth=basic", "solr://localhost?Auth=none",
		"solr://localhost?auth=", "solr://localhost?sslmode=require", "solr://localhost?ca_file=relative.pem",
		"solr://localhost:0", "solr://localhost:65536", "solr://localhost:", "solr://::1:8983", "solr://localhost#private", "solr://localhost?",
	} {
		parsed, err := connections.ParseURI(uri)
		if err == nil || parsed.Password != "" || parsed.Profile.Kind != "" || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid URI accepted or credential leaked")
		}
	}
}
