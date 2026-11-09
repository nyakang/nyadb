package connections_test

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNeo4jProfilesPreserveNativeSchemesRoutingAndExactCredentials(t *testing.T) {
	for _, scheme := range []string{"bolt", "neo4j", "bolt+s", "neo4j+s", "bolt+ssc", "neo4j+ssc"} {
		t.Run(scheme, func(t *testing.T) {
			uri := url.URL{Scheme: scheme, Host: "[::1]:7689", Path: "/MixedCase", User: url.UserPassword(" native 用户 ", " 原始密码🐱\t ")}
			q := url.Values{"connect_timeout": {"12"}, "request_timeout": {"45"}}
			if strings.HasPrefix(scheme, "neo4j") {
				q.Set("region", "local")
				q.Set("policy", "reader")
			}
			uri.RawQuery = q.Encode()
			parsed, e := connections.ParseURI(uri.String())
			if e != nil {
				t.Fatal(e)
			}
			p := parsed.Profile
			if p.Kind != connections.Neo4j || p.Database != "MixedCase" || p.Username != " native 用户 " || parsed.Password != " 原始密码🐱\t " || strings.Contains(fmt.Sprint(p), parsed.Password) {
				t.Fatal("native fields narrowed or secret entered profile")
			}
			cfg, e := connections.Neo4jProfileConfig(p)
			if e != nil {
				t.Fatal(e)
			}
			u, _ := url.Parse(cfg.URI)
			if u.Scheme != scheme || u.Host != uri.Host || u.User != nil || u.Path != "" || cfg.Database != p.Database || cfg.ConnectTimeoutSeconds != 12 || cfg.RequestTimeoutSeconds != 45 {
				t.Fatal("native URI projection changed identity")
			}
			if strings.HasPrefix(scheme, "neo4j") && u.RawQuery != "policy=reader&region=local" {
				t.Fatal("routing context changed")
			}
		})
	}
	tlsDir := t.TempDir()
	caFile := filepath.Join(tlsDir, "ca.crt")
	clientCertFile := filepath.Join(tlsDir, "client.crt")
	clientKeyFile := filepath.Join(tlsDir, "client.key")
	tlsOptions := url.Values{
		"auth": {"none"}, "database": {"system"}, "region": {"local"},
		"ca_file": {caFile}, "client_cert_file": {clientCertFile}, "client_key_file": {clientKeyFile},
	}
	parsed, e := connections.ParseNeo4jURI("neo4j+s://localhost?" + tlsOptions.Encode())
	if e != nil || parsed.Profile.Username != "" || parsed.Password != "" || parsed.Profile.Database != "system" {
		t.Fatal("no-auth native TLS import failed", e)
	}
	if o := parsed.Profile.Neo4jOptions; o.CAFile != caFile || o.ClientCertFile != clientCertFile || o.ClientKeyFile != clientKeyFile {
		t.Fatal("native TLS paths changed during import")
	}
	missing, e := connections.ParseNeo4jURI("bolt://neo4j@localhost")
	if e != nil {
		t.Fatal(e)
	}
	cfg, _ := connections.Neo4jProfileConfig(missing.Profile)
	if connections.ValidateNeo4jDraftCredential(cfg, "") != nil || connections.ValidateNeo4jCredential(cfg, "") == nil {
		t.Fatal("missing password cannot be deferred to explicit prompt")
	}
	if d, registered := connections.Describe(connections.Neo4j); !registered || d.Dialect != "cypher" || !d.Capabilities.Query || !d.Capabilities.Metadata || d.Capabilities.EditableTables {
		t.Fatal("Neo4j public descriptor lost native capabilities")
	}
}

func TestNeo4jRejectsConflictingURIAndForeignOptions(t *testing.T) {
	tlsDir := t.TempDir()
	caFile := url.QueryEscape(filepath.Join(tlsDir, "ca.crt"))
	clientCertFile := url.QueryEscape(filepath.Join(tlsDir, "client.crt"))
	for _, uri := range []string{
		"bolt://host?region=local", "neo4j://host?password=private", "neo4j://host?api_token=private", "neo4j://host?address=other",
		"neo4j://host?region=one&region=two", "neo4j://host?region=%FF", "neo4j://host?region=%00",
		"bolt+s://host?sslmode=disable", "bolt+ssc://host?ca_file=" + caFile,
		"neo4j+s://host?server_name=other",
		"bolt://user:private@host?auth=none", "bolt://host?auth=bearer", "bolt://:private@host",
		"neo4j://host/database?database=other", "neo4j://host/one/two", "bolt://host:0", "bolt://host?request_timeout=01",
		"bolt+s://host?client_cert_file=" + clientCertFile, "bolt://host?connect_timeout=121",
	} {
		if _, e := connections.ParseNeo4jURI(uri); !errors.Is(e, connections.ErrURIOptions) {
			t.Fatalf("invalid native import accepted: %s", uri)
		}
	}
	p, _ := connections.Defaults(connections.Neo4j)
	p.Name = "native"
	p.Database = " exact database "
	p.Username = " exact user "
	n, e := connections.NormalizeProfile(p)
	if e != nil || n.Database != p.Database || n.Username != p.Username {
		t.Fatal("profile identity trimmed", e)
	}
	p.Kind = connections.PostgreSQL
	if _, e := connections.NormalizeProfile(p); e == nil {
		t.Fatal("foreign native options accepted")
	}
	p, _ = connections.Defaults(connections.Neo4j)
	p.Name = "native"
	p.PostgreSQLOptions = connections.DefaultPostgreSQLOptions()
	if _, e := connections.NormalizeProfile(p); e == nil {
		t.Fatal("relational settings accepted on graph engine")
	}
}
