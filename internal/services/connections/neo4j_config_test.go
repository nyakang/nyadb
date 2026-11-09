package connections_test

import (
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNeo4jFoundationConfigPreservesRoutingTLSAndSeparateCredentials(t *testing.T) {
	for _, scheme := range []string{"bolt", "neo4j", "bolt+s", "neo4j+s", "bolt+ssc", "neo4j+ssc"} {
		c, err := connections.NormalizeNeo4jConfig(connections.Neo4jConfig{URI: scheme + "://[::1]:7687", Username: "neo4j"})
		if err != nil || c.Auth != "basic" || c.Database != "neo4j" || c.RequestTimeoutSeconds != 40 {
			t.Fatal(c, err)
		}
		if err = connections.ValidateNeo4jCredential(c, " 原始密码🐱 \t"); err != nil {
			t.Fatal("native credential bytes were narrowed", err)
		}
	}
	c, err := connections.NormalizeNeo4jConfig(connections.Neo4jConfig{URI: "neo4j+s://cluster:7687?region=local&policy=reader", Username: "reader", CAFile: "/owned/ca.pem", ServerName: "cluster"})
	if err != nil || c.URI != "neo4j+s://cluster:7687?policy=reader&region=local" {
		t.Fatal(c, err)
	}
	for _, c := range []connections.Neo4jConfig{
		{URI: "bolt://user:secret@host:7687", Username: "neo4j"}, {URI: "bolt://host:0", Username: "neo4j"},
		{URI: "neo4j://host?password=private", Username: "neo4j"}, {URI: "bolt://host?region=local", Username: "neo4j"},
		{URI: "bolt://host", Username: "neo4j", CAFile: "/owned/ca.pem"}, {URI: "neo4j+s://host", Username: "neo4j", ClientCertFile: "/owned/client.pem"},
		{URI: "neo4j+ssc://host", Username: "neo4j", CAFile: "/owned/ca.pem"}, {URI: "neo4j+s://host", Username: "neo4j", CAFile: "relative.pem"},
		{URI: "neo4j://host/db", Username: "neo4j"}, {URI: "neo4j://host?address=other", Username: "neo4j"},
	} {
		if _, err := connections.NormalizeNeo4jConfig(c); err == nil {
			t.Fatal("invalid public configuration accepted", c)
		}
	}
	if _, err = connections.NormalizeNeo4jConfig(connections.Neo4jConfig{URI: "bolt://localhost", Auth: "none", Username: "neo4j"}); err == nil {
		t.Fatal("no-auth silently discarded a username")
	}
}

func TestNeo4jSeparateCredentialsRejectInvalidSecrets(t *testing.T) {
	for _, secret := range []string{"", "contains\x00nul", "\xff", strings.Repeat("x", 4097)} {
		if connections.ValidateNeo4jCredential(connections.Neo4jConfig{Auth: "basic"}, secret) == nil {
			t.Fatal("invalid basic secret accepted")
		}
	}
	if connections.ValidateNeo4jCredential(connections.Neo4jConfig{Auth: "none"}, "") != nil || connections.ValidateNeo4jCredential(connections.Neo4jConfig{Auth: "none"}, "do not silently discard") == nil {
		t.Fatal("invalid no-auth credential boundary")
	}
}
