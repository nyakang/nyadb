package connections_test

import (
	"errors"
	"github.com/nyakang/nyadb/internal/services/connections"
	"testing"
)

func TestSolrConfigKeepsContextAndSeparatesCredentials(t *testing.T) {
	for _, tc := range []struct{ in, out string }{{"http://localhost:8983", "http://localhost:8983/solr"}, {"https://localhost:8983/solr/", "https://localhost:8983/solr"}, {"https://[::1]:8983/proxy/solr", "https://[::1]:8983/proxy/solr"}} {
		c, e := connections.NormalizeSolrConfig(connections.SolrConfig{Endpoint: tc.in})
		if e != nil || c.Endpoint != tc.out || c.Auth != "none" {
			t.Fatal("native context was changed", e)
		}
	}
	valid := connections.SolrConfig{Endpoint: "https://localhost:8983/solr", Auth: "basic", Username: "native-user", CAFile: "/private/ca.pem"}
	if _, e := connections.NormalizeSolrConfig(valid); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*connections.SolrConfig){
		func(c *connections.SolrConfig) { c.Endpoint = "http://localhost:8983" }, func(c *connections.SolrConfig) { c.Endpoint = "https://user:secret@localhost:8983/solr" }, func(c *connections.SolrConfig) { c.Endpoint += "?password=secret" }, func(c *connections.SolrConfig) { c.Endpoint += "/../admin" }, func(c *connections.SolrConfig) { c.Endpoint += "/%2e%2e/admin" }, func(c *connections.SolrConfig) { c.Endpoint += "/%00" }, func(c *connections.SolrConfig) { c.Endpoint += "//other" }, func(c *connections.SolrConfig) { c.Endpoint = "https://localhost:0/solr" }, func(c *connections.SolrConfig) { c.CAFile = "relative.pem" }, func(c *connections.SolrConfig) { c.Auth = "api_key" }, func(c *connections.SolrConfig) { c.Auth = "none" }, func(c *connections.SolrConfig) { c.Username = "a:b" },
	} {
		v := valid
		change(&v)
		if _, e := connections.NormalizeSolrConfig(v); !errors.Is(e, connections.ErrInvalid) {
			t.Fatal("ambiguous or unsafe native configuration accepted")
		}
	}
	if e := connections.ValidateSolrCredential(valid, "native-password", true); e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{"", string([]byte{0xff}), "has\x00null"} {
		if e := connections.ValidateSolrCredential(valid, secret, true); e == nil {
			t.Fatal("invalid password accepted")
		}
	}
	if e := connections.ValidateSolrCredential(valid, "", false); e != nil {
		t.Fatal("optional editing password", e)
	}
	if e := connections.ValidateSolrCredential(connections.SolrConfig{Auth: "none"}, "stray password", false); e == nil {
		t.Fatal("no-auth retained password")
	}
}
