package connections_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCouchDBPrivateProfileIndependentContextAndExactPrincipal(t *testing.T) {
	p, err := connections.Defaults(connections.CouchDB)
	if err != nil || p.Port != 5984 || p.TLSMode != "disable" || p.Username != "" || p.Database != "" || p.CouchDBOptions.ContextPath != "" || p.CouchDBOptions.Auth != "none" || p.Kind.IsMySQLFamily() || p.Kind.IsPostgreSQLFamily() || p.Kind.IsRESTSearch() {
		t.Fatal("CouchDB native defaults or family changed")
	}
	if _, registered := connections.Describe(connections.CouchDB); registered {
		t.Fatal("private foundation registered before workbench acceptance")
	}
	p.Name, p.Host, p.Username, p.TLSMode = "native", "::1", " 原生用户🐱 ", "verify-full"
	p.CouchDBOptions.Auth = "basic"
	p.CouchDBOptions.ContextPath = "/gateway/couchdb/"
	p.CouchDBOptions.CAFile = filepath.Join(t.TempDir(), "ca.pem")
	p.CouchDBOptions.ServerName = "localhost"
	p, err = connections.NormalizeProfile(p)
	if err != nil || p.Username != " 原生用户🐱 " || p.CouchDBOptions.ContextPath != "/gateway/couchdb" {
		t.Fatal("native principal/context changed", err)
	}
	cfg, err := connections.CouchDBProfileConfig(p)
	if err != nil || cfg.Endpoint != "https://[::1]:5984/gateway/couchdb" || cfg.Username != p.Username || cfg.CAFile != p.CouchDBOptions.CAFile {
		t.Fatal("qualified transport changed", err)
	}
	for _, mutate := range []func(*connections.Profile){
		func(p *connections.Profile) { p.Database = "db" },
		func(p *connections.Profile) { p.MySQLAdvanced = connections.DefaultMySQLAdvanced() },
		func(p *connections.Profile) { p.SolrOptions = connections.DefaultSolrOptions() },
		func(p *connections.Profile) { p.PostgreSQLOptions = connections.DefaultPostgreSQLOptions() },
		func(p *connections.Profile) { p.Host = "https://localhost" },
		func(p *connections.Profile) { p.Host = "localhost:5984" },
		func(p *connections.Profile) { p.Port = 0 },
		func(p *connections.Profile) { p.TLSMode = "require" },
		func(p *connections.Profile) { p.TLSMode = "" },
		func(p *connections.Profile) { p.TLSMode = "disable" },
		func(p *connections.Profile) { p.Username = "" },
		func(p *connections.Profile) { p.Username = "a:b" },
		func(p *connections.Profile) { p.CouchDBOptions.Auth = "cookie" },
		func(p *connections.Profile) { p.CouchDBOptions.Auth = "none" },
		func(p *connections.Profile) { p.CouchDBOptions.Version = 2 },
		func(p *connections.Profile) { p.CouchDBOptions.ContextPath = "/%2e%2e" },
		func(p *connections.Profile) { p.CouchDBOptions.ContextPath = "context" },
		func(p *connections.Profile) { p.Variants = []connections.ConnectionVariant{{Name: "other"}} },
	} {
		copy := p
		mutate(&copy)
		if _, err := connections.NormalizeProfile(copy); err == nil {
			t.Fatal("unsupported native profile accepted")
		}
	}
	other, _ := connections.Defaults(connections.PostgreSQL)
	other.Name, other.CouchDBOptions = "foreign", p.CouchDBOptions
	if _, err := connections.NormalizeProfile(other); err == nil {
		t.Fatal("CouchDB envelope accepted on another engine")
	}
}

func TestCouchDBPrivateURIKeepsExactSecretAndRejectsAmbiguity(t *testing.T) {
	for _, tc := range []struct{ uri, auth, username, secret, endpoint string }{
		{"couchdb://localhost", "none", "", "", "http://localhost:5984"},
		{"couchdb://localhost/", "none", "", "", "http://localhost:5984"},
		{"couchdb://%20user%20:%20secret%20@[::1]:6990/gateway/couchdb/?sslmode=verify-full&server_name=localhost", "basic", " user ", " secret ", "https://[::1]:6990/gateway/couchdb"},
		{"couchdb://reader:private%3Apassword@localhost", "basic", "reader", "private:password", "http://localhost:5984"},
		{"couchdb://reader@localhost?auth=basic", "basic", "reader", "", "http://localhost:5984"},
	} {
		v, err := connections.ParseCouchDBURI(tc.uri)
		if err != nil || v.Password != tc.secret || v.Profile.Username != tc.username || v.Profile.CouchDBOptions.Auth != tc.auth || v.Profile.Database != "" {
			t.Fatal("private URI changed public settings or secret", err)
		}
		cfg, err := connections.CouchDBProfileConfig(v.Profile)
		if err != nil || cfg.Endpoint != tc.endpoint {
			t.Fatal("private URI changed endpoint", err)
		}
		public, _ := json.Marshal(v)
		if tc.secret != "" && (strings.Contains(string(public), tc.secret) || strings.Contains(fmt.Sprintf("%+v %#v", v, v), tc.secret)) {
			t.Fatal("temporary secret leaked")
		}
		if _, err := connections.ParseURI(tc.uri); err == nil {
			t.Fatal("public importer bypassed registration gate")
		}
	}
	for _, uri := range []string{
		"http://localhost:5984", "couchdb://user:private@localhost?auth=none", "couchdb://localhost?auth=basic", "couchdb://localhost?auth=cookie", "couchdb://localhost?password=private", "couchdb://localhost?database=db", "couchdb://localhost?context_path=/proxy", "couchdb://localhost?auth=none&auth=basic", "couchdb://localhost?Auth=none", "couchdb://localhost?sslmode=require", "couchdb://localhost?ca_file=relative.pem", "couchdb://localhost?server_name=localhost", "couchdb://localhost?", "couchdb://localhost#private", "couchdb://localhost:0", "couchdb://localhost:65536", "couchdb://localhost:", "couchdb://::1:5984", "couchdb://localhost/../other", "couchdb://localhost/%2e%2e", "couchdb://localhost/%252e%252e", "couchdb://localhost//other", "couchdb://a%3Ab:private@localhost", "couchdb://reader:has%00null@localhost",
	} {
		v, err := connections.ParseCouchDBURI(uri)
		if err == nil || v.Password != "" || v.Profile.Kind != "" || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid private URI changed draft or exposed secret")
		}
	}
}
