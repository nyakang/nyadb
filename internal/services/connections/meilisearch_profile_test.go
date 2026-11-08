package connections_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestMeilisearchPrivateProfileScopeTLSDeadlinesAndFamily(t *testing.T) {
	p, err := connections.Defaults(connections.Meilisearch)
	if err != nil || p.Port != 7700 || p.TLSMode != "disable" || p.MeilisearchOptions.Auth != "none" || p.Username != "" || p.Database != "" || p.Kind.IsMySQLFamily() || p.Kind.IsPostgreSQLFamily() || p.Kind.IsREST() {
		t.Fatal("private defaults/family inflated", err)
	}
	if _, registered := connections.Describe(p.Kind); registered {
		t.Fatal("private foundation published product before provider")
	}
	p.Name, p.Host, p.TLSMode = "native", "::1", "verify-full"
	o := &p.MeilisearchOptions
	o.Auth, o.ContextPath, o.IndexUID = "bearer", "/gateway/meili/", "owned-scope"
	o.CAFile, o.ClientCertFile, o.ClientKeyFile = filepath.Join(t.TempDir(), "ca.pem"), filepath.Join(t.TempDir(), "client.pem"), filepath.Join(t.TempDir(), "client.key")
	o.ConnectTimeoutSeconds, o.RequestTimeoutSeconds = 12, 45
	p, err = connections.NormalizeProfile(p)
	if err != nil || p.MeilisearchOptions.ContextPath != "/gateway/meili" {
		t.Fatal("private public settings changed", err)
	}
	cfg, err := connections.MeilisearchProfileConfig(p)
	if err != nil || cfg.Endpoint != "https://[::1]:7700/gateway/meili" || cfg.IndexUID != "owned-scope" || cfg.ConnectTimeoutSeconds != 12 || cfg.RequestTimeoutSeconds != 45 || cfg.ClientKeyFile != p.MeilisearchOptions.ClientKeyFile {
		t.Fatal("original configuration lost", err)
	}
	for _, mutate := range []func(*connections.Profile){
		func(p *connections.Profile) { p.Username = "unused" }, func(p *connections.Profile) { p.Username = " " }, func(p *connections.Profile) { p.Database = "index" }, func(p *connections.Profile) { p.Host = "http://localhost" }, func(p *connections.Profile) { p.Host = "localhost:7700" }, func(p *connections.Profile) { p.Port = 0 }, func(p *connections.Profile) { p.TLSMode = "require" }, func(p *connections.Profile) { p.TLSMode = "disable" }, func(p *connections.Profile) { p.MeilisearchOptions.Auth = "basic" }, func(p *connections.Profile) { p.MeilisearchOptions.Version = 2 }, func(p *connections.Profile) { p.MeilisearchOptions.IndexUID = "../other" }, func(p *connections.Profile) { p.MeilisearchOptions.ContextPath = "/../other" }, func(p *connections.Profile) { p.MeilisearchOptions.ClientKeyFile = "" }, func(p *connections.Profile) { p.MeilisearchOptions.RequestTimeoutSeconds = 601 }, func(p *connections.Profile) { p.MeilisearchOptions.ConnectTimeoutSeconds = -1 }, func(p *connections.Profile) { p.CouchDBOptions = connections.DefaultCouchDBOptions() }, func(p *connections.Profile) { p.MySQLAdvanced = connections.DefaultMySQLAdvanced() }, func(p *connections.Profile) { p.Variants = []connections.ConnectionVariant{{Name: "other"}} },
	} {
		copy := p
		mutate(&copy)
		if _, err := connections.NormalizeProfile(copy); err == nil {
			t.Fatal("unsupported Meilisearch profile accepted")
		}
	}
	other, _ := connections.Defaults(connections.CouchDB)
	other.Name = "foreign"
	other.MeilisearchOptions = p.MeilisearchOptions
	if _, err := connections.NormalizeProfile(other); err == nil {
		t.Fatal("foreign Meilisearch settings accepted")
	}
}

func TestMeilisearchPrivateURISeparatesBearerAndRejectsAmbiguity(t *testing.T) {
	for _, tc := range []struct{ uri, key, auth, index, endpoint string }{
		{"meilisearch://localhost", "", "none", "", "http://localhost:7700"},
		{"meilisearch://:owned%2Bkey%3D%3D@[::1]:7790/gateway/meili/?sslmode=verify-full&index_uid=owned", "owned+key==", "bearer", "owned", "https://[::1]:7790/gateway/meili"},
		{"meilisearch://localhost?api_key=owned-key&connect_timeout=12&request_timeout=45", "owned-key", "bearer", "", "http://localhost:7700"},
		{"meilisearch://localhost?auth=bearer&index_uid=scope", "", "bearer", "scope", "http://localhost:7700"},
	} {
		parsed, err := connections.ParseMeilisearchURI(tc.uri)
		if err != nil || parsed.Password != tc.key || parsed.Profile.MeilisearchOptions.Auth != tc.auth || parsed.Profile.MeilisearchOptions.IndexUID != tc.index || parsed.Profile.Username != "" {
			t.Fatal("private URI changed scope or key", err)
		}
		cfg, err := connections.MeilisearchProfileConfig(parsed.Profile)
		if err != nil || cfg.Endpoint != tc.endpoint {
			t.Fatal("endpoint changed", err)
		}
		public, _ := json.Marshal(parsed)
		if tc.key != "" && (strings.Contains(string(public), tc.key) || strings.Contains(fmt.Sprintf("%+v %#v", parsed, parsed), tc.key)) {
			t.Fatal("URI temporary key leaked")
		}
		if _, err := connections.ParseURI(tc.uri); err == nil {
			t.Fatal("public importer enabled before provider")
		}
	}
	for _, uri := range []string{
		"https://localhost:7700", "meilisearch://reader:private@localhost", "meilisearch://:private@localhost?api_key=other", "meilisearch://localhost?api_key=private&auth=none", "meilisearch://:private@localhost?auth=none", "meilisearch://localhost?api_key=private&api_key=other", "meilisearch://localhost?API_Key=private", "meilisearch://localhost?username=private", "meilisearch://localhost?password=private", "meilisearch://localhost?auth=basic", "meilisearch://localhost?request_timeout=0", "meilisearch://localhost?request_timeout=601", "meilisearch://localhost?request_timeout=01", "meilisearch://localhost?connect_timeout=121", "meilisearch://localhost?index_uid=../other", "meilisearch://localhost?client_cert_file=/tmp/client.pem", "meilisearch://localhost?ca_file=relative.pem", "meilisearch://localhost?server_name=localhost", "meilisearch://localhost?sslmode=require", "meilisearch://localhost?", "meilisearch://localhost#private", "meilisearch://localhost:0", "meilisearch://localhost:65536", "meilisearch://localhost:", "meilisearch://::1:7700", "meilisearch://localhost/../other", "meilisearch://localhost/%2fother", "meilisearch://localhost/%252e", "meilisearch://localhost//other", "meilisearch://localhost?api_key=%20private%20",
	} {
		parsed, err := connections.ParseMeilisearchURI(uri)
		if err == nil || parsed.Password != "" || parsed.Profile.Kind != "" || strings.Contains(err.Error(), "private") {
			t.Fatal("ambiguous private URI leaked or accepted")
		}
	}
	paths := url.Values{"sslmode": {"verify-full"}, "client_cert_file": {filepath.Join(t.TempDir(), "client.pem")}, "client_key_file": {filepath.Join(t.TempDir(), "client.key")}}
	if _, err := connections.ParseMeilisearchURI("meilisearch://localhost?" + paths.Encode()); err != nil {
		t.Fatal("pure mTLS URI rejected without opening files", err)
	}
}
