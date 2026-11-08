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

func TestQdrantProfileTLSDeadlinesAndIndependentFamily(t *testing.T) {
	p, err := connections.Defaults(connections.Qdrant)
	if err != nil || p.Port != 6333 || p.TLSMode != "disable" || p.QdrantOptions.Auth != "none" || p.Username != "" || p.Database != "" || !p.Kind.IsREST() || p.Kind.IsRESTSearch() || p.Kind.IsMySQLFamily() || p.Kind.IsPostgreSQLFamily() {
		t.Fatal("independent native defaults or family", err)
	}
	p.Name, p.Host, p.TLSMode = "native vector service", "::1", "verify-full"
	o := &p.QdrantOptions
	o.Auth, o.ContextPath = "api_key", "/gateway/qdrant/"
	o.CAFile, o.ClientCertFile, o.ClientKeyFile = filepath.Join(t.TempDir(), "ca.pem"), filepath.Join(t.TempDir(), "client.pem"), filepath.Join(t.TempDir(), "client.key")
	o.ConnectTimeoutSeconds, o.RequestTimeoutSeconds = 12, 45
	p, err = connections.NormalizeProfile(p)
	if err != nil || p.QdrantOptions.ContextPath != "/gateway/qdrant" {
		t.Fatal("original public settings", err)
	}
	cfg, err := connections.QdrantProfileConfig(p)
	if err != nil || cfg.Endpoint != "https://[::1]:6333/gateway/qdrant" || cfg.ConnectTimeoutSeconds != 12 || cfg.RequestTimeoutSeconds != 45 || cfg.ClientKeyFile != p.QdrantOptions.ClientKeyFile {
		t.Fatal("original endpoint/certificates/timeouts changed", err)
	}
	mutations := []func(*connections.Profile){
		func(p *connections.Profile) { p.Username = "unused" }, func(p *connections.Profile) { p.Username = " " },
		func(p *connections.Profile) { p.Database = "collection" }, func(p *connections.Profile) { p.Database = " " },
		func(p *connections.Profile) { p.Host = "http://localhost" }, func(p *connections.Profile) { p.Host = "localhost:6333" },
		func(p *connections.Profile) { p.Port = 0 }, func(p *connections.Profile) { p.TLSMode = "require" },
		func(p *connections.Profile) { p.TLSMode = "disable" }, func(p *connections.Profile) { p.QdrantOptions.Auth = "basic" },
		func(p *connections.Profile) { p.QdrantOptions.Version = 2 }, func(p *connections.Profile) { p.QdrantOptions.ContextPath = "/../other" },
		func(p *connections.Profile) { p.QdrantOptions.ClientKeyFile = "" }, func(p *connections.Profile) { p.QdrantOptions.RequestTimeoutSeconds = 601 },
		func(p *connections.Profile) { p.QdrantOptions.ConnectTimeoutSeconds = -1 },
		func(p *connections.Profile) { p.MeilisearchOptions = connections.DefaultMeilisearchOptions() },
		func(p *connections.Profile) { p.MySQLAdvanced = connections.DefaultMySQLAdvanced() },
		func(p *connections.Profile) { p.Variants = []connections.ConnectionVariant{{Name: "other"}} },
	}
	for i, mutate := range mutations {
		copy := p
		mutate(&copy)
		if _, err := connections.NormalizeProfile(copy); err == nil {
			t.Fatalf("unsupported Qdrant profile accepted: %d", i)
		}
	}
	other, _ := connections.Defaults(connections.Meilisearch)
	other.Name, other.QdrantOptions = "foreign", p.QdrantOptions
	if _, err := connections.NormalizeProfile(other); err == nil {
		t.Fatal("Qdrant options accepted on another engine")
	}
}

func TestQdrantURISeparatesAPIKeyAndRejectsAmbiguity(t *testing.T) {
	for _, tc := range []struct{ uri, key, auth, endpoint string }{
		{"qdrant://localhost", "", "none", "http://localhost:6333"},
		{"qdrant://:owned%2Bkey%3D%3D@[::1]:6390/gateway/qdrant/?sslmode=verify-full", "owned+key==", "api_key", "https://[::1]:6390/gateway/qdrant"},
		{"qdrant://localhost?api_key=owned-key&connect_timeout=12&request_timeout=45", "owned-key", "api_key", "http://localhost:6333"},
		{"qdrant://localhost?auth=api_key", "", "api_key", "http://localhost:6333"},
	} {
		parsed, err := connections.ParseQdrantURI(tc.uri)
		if err != nil || parsed.Password != tc.key || parsed.Profile.QdrantOptions.Auth != tc.auth || parsed.Profile.Username != "" || parsed.Profile.Database != "" {
			t.Fatal("native URI changed original authentication", err)
		}
		cfg, err := connections.QdrantProfileConfig(parsed.Profile)
		if err != nil || cfg.Endpoint != tc.endpoint {
			t.Fatal("native endpoint changed", err)
		}
		public, _ := json.Marshal(parsed)
		if tc.key != "" && (strings.Contains(string(public), tc.key) || strings.Contains(fmt.Sprintf("%+v %#v", parsed, parsed), tc.key)) {
			t.Fatal("temporary URI key exposed")
		}
		if imported, err := connections.ParseURI(tc.uri); err == nil || imported.Profile.Kind != "" || imported.Password != "" {
			t.Fatal("private Qdrant URI exposed before public provider registration")
		}
	}
	for _, uri := range []string{
		"https://localhost:6333", "qdrant://reader:private@localhost", "qdrant://:private@localhost?api_key=other", "qdrant://localhost?api_key=private&auth=none", "qdrant://:private@localhost?auth=none", "qdrant://localhost?api_key=private&api_key=other", "qdrant://localhost?API_Key=private", "qdrant://localhost?username=private", "qdrant://localhost?password=private", "qdrant://localhost?auth=basic", "qdrant://localhost?auth=bearer", "qdrant://localhost?request_timeout=0", "qdrant://localhost?request_timeout=601", "qdrant://localhost?request_timeout=01", "qdrant://localhost?connect_timeout=121", "qdrant://localhost?index_uid=owned", "qdrant://localhost?collection=owned", "qdrant://localhost?client_cert_file=/tmp/client.pem", "qdrant://localhost?ca_file=relative.pem", "qdrant://localhost?server_name=localhost", "qdrant://localhost?sslmode=require", "qdrant://localhost?", "qdrant://localhost#private", "qdrant://localhost:0", "qdrant://localhost:65536", "qdrant://localhost:", "qdrant://::1:6333", "qdrant://localhost/../other", "qdrant://localhost/%2fother", "qdrant://localhost/%252e", "qdrant://localhost//other", "qdrant://localhost?api_key=%20private%20",
	} {
		parsed, err := connections.ParseQdrantURI(uri)
		if err == nil || parsed.Password != "" || parsed.Profile.Kind != "" || strings.Contains(err.Error(), "private") {
			t.Fatal("ambiguous URI exposed or accepted")
		}
	}
	paths := url.Values{"sslmode": {"verify-full"}, "client_cert_file": {filepath.Join(t.TempDir(), "client.pem")}, "client_key_file": {filepath.Join(t.TempDir(), "client.key")}}
	if _, err := connections.ParseQdrantURI("qdrant://localhost?" + paths.Encode()); err != nil {
		t.Fatal("pure mTLS URI read files or rejected valid paths", err)
	}
}
