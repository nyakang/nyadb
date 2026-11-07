package connections_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestGaussDBConfigurationPreservesIndependentEndpointsAndCopiesInputs(t *testing.T) {
	c := connections.GaussDBConfig{Endpoints: []connections.GaussDBEndpoint{{Host: " database.example "}, {Host: "2001:db8::1", Port: 5433}}, Database: "app", Username: "reader"}
	got, err := connections.NormalizeGaussDBConfig(c)
	if err != nil || got.TLSMode != "verify-full" || got.TargetSessionAttrs != "any" || got.Endpoints[0].Host != "database.example" || got.Endpoints[0].Port != 5432 || got.Endpoints[1].Port != 5433 {
		t.Fatal("native defaults or independent endpoint identity lost", err)
	}
	if c.Endpoints[0].Host != " database.example " || c.Endpoints[0].Port != 0 {
		t.Fatal("normalization mutated caller endpoints")
	}
	got.Endpoints[0].Host = "changed.example"
	if c.Endpoints[0].Host != " database.example " {
		t.Fatal("configuration retained caller endpoint slice")
	}
	raw, err := json.Marshal(got)
	if err != nil || strings.Contains(strings.ToLower(string(raw)), "password") {
		t.Fatal("public configuration contains credentials", err)
	}
	var decoded connections.GaussDBConfig
	if err = json.Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(got, decoded) {
		t.Fatal("public endpoint settings do not round trip", err)
	}
}

func TestGaussDBConfigurationRejectsAmbiguousEndpointsAndForeignSemantics(t *testing.T) {
	base := connections.GaussDBConfig{Endpoints: []connections.GaussDBEndpoint{{Host: "db.example"}}, Database: "app", Username: "reader", Options: connections.DefaultPostgreSQLOptions()}
	for _, host := range []string{"", "postgres://db", "reader@db", "db:5432", "[2001:db8::1]", "host/path", "host?password=secret", "db.example,other", "a\nb", "a\x00b", "a\xffb"} {
		bad := base
		bad.Endpoints = []connections.GaussDBEndpoint{{Host: host}}
		if _, err := connections.NormalizeGaussDBConfig(bad); !errors.Is(err, connections.ErrInvalid) {
			t.Fatalf("invalid endpoint %q accepted: %v", host, err)
		}
	}
	for _, mutate := range []func(*connections.GaussDBConfig){
		func(c *connections.GaussDBConfig) { c.Endpoints = nil },
		func(c *connections.GaussDBConfig) {
			c.Endpoints = []connections.GaussDBEndpoint{{Host: "db.example"}, {Host: "DB.EXAMPLE", Port: 5432}}
		},
		func(c *connections.GaussDBConfig) { c.Endpoints = make([]connections.GaussDBEndpoint, 17) },
		func(c *connections.GaussDBConfig) { c.Database = "" },
		func(c *connections.GaussDBConfig) { c.Username = "" },
		func(c *connections.GaussDBConfig) { c.TLSMode = "allow" },
		func(c *connections.GaussDBConfig) { c.TargetSessionAttrs = "primary password=secret" },
		func(c *connections.GaussDBConfig) { c.Options.LockTimeoutMS = 1 },
		func(c *connections.GaussDBConfig) { c.Options.ToolsDirectory = "/pg/tools" },
		func(c *connections.GaussDBConfig) { c.Options.SSH.Enabled = true },
		func(c *connections.GaussDBConfig) { c.Options.HTTP.Enabled = true },
		func(c *connections.GaussDBConfig) { c.TLSMode, c.Options.CAFile = "disable", "/ca.pem" },
		func(c *connections.GaussDBConfig) { c.Options.CertificateFile = "/client.pem" },
	} {
		bad := base
		mutate(&bad)
		if _, err := connections.NormalizeGaussDBConfig(bad); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("unsupported native configuration accepted", err)
		}
	}
}
