package connections_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestConsulProfilePreservesOriginalDCKeysAndIndependentTokenMTLS(t *testing.T) {
	p, err := connections.Defaults(connections.Consul)
	if err != nil || p.Port != 8500 || p.Username != "" || p.Database != "" {
		t.Fatal("original Consul defaults changed", err)
	}
	p.Name, p.Host, p.TLSMode = "original", "::1", "verify-full"
	o := &p.ConsulOptions
	o.Auth, o.Datacenter, o.Prefix, o.Consistency = "none", "dc1", " /原键//../\n/ ", "consistent"
	o.CAFile, o.ServerName, o.ClientCertFile, o.ClientKeyFile = "/owned/ca.crt", "localhost", "/owned/client.crt", "/owned/client.key"
	original := p.ConsulOptions
	p, err = connections.NormalizeProfile(p)
	if err != nil || p.ConsulOptions != original {
		t.Fatal("original configuration changed", err)
	}
	cfg, err := connections.ConsulProfileConfig(p)
	if err != nil || cfg.Endpoint != "https://[::1]:8500" || cfg.Options != p.ConsulOptions || connections.ValidateConsulCredential(cfg, "", true) != nil {
		t.Fatal("mTLS incorrectly required token authentication", err)
	}
	p.ConsulOptions.Auth = "token"
	cfg, err = connections.ConsulProfileConfig(p)
	if err != nil || connections.ValidateConsulCredential(cfg, "original-token", true) != nil || connections.ValidateConsulCredential(cfg, "", true) == nil || connections.ValidateConsulCredential(cfg, "", false) != nil {
		t.Fatal("original temporary token validation changed", err)
	}
	if strings.Contains(fmt.Sprint(p), "original-token") {
		t.Fatal("token entered public profile")
	}
	for _, change := range []func(*connections.Profile){
		func(p *connections.Profile) { p.Username = " " },
		func(p *connections.Profile) { p.Database = " " },
		func(p *connections.Profile) { p.ConsulOptions.Consistency = "serializable" },
		func(p *connections.Profile) { p.ConsulOptions.Auth = "certificate" },
		func(p *connections.Profile) { p.TLSMode = "require" },
		func(p *connections.Profile) { p.TLSMode = "disable" },
		func(p *connections.Profile) { p.ConsulOptions.ClientKeyFile = "" },
		func(p *connections.Profile) { p.ConsulOptions.Datacenter = " dc1 " },
		func(p *connections.Profile) { p.ConsulOptions.Prefix = "key\x00" },
		func(p *connections.Profile) { p.ConsulOptions.RequestTimeoutSeconds = 601 },
		func(p *connections.Profile) { p.ConsulOptions.ConnectTimeoutSeconds = 0 },
		func(p *connections.Profile) { p.ConsulOptions.Version = 2 },
		func(p *connections.Profile) { p.MySQLAdvanced.Charset = "utf8mb4" },
		func(p *connections.Profile) { p.EtcdOptions = connections.DefaultEtcdOptions() },
		func(p *connections.Profile) { p.Kind = connections.MySQL },
	} {
		candidate := p
		change(&candidate)
		if _, err := connections.NormalizeProfile(candidate); err == nil {
			t.Fatal("foreign or invalid Consul configuration accepted", fmt.Sprint(candidate))
		}
	}
}

func TestConsulURIExactOriginalScopeAndTemporarySecret(t *testing.T) {
	uri := "consuls://:original-token@[::1]:18501?dc=dc1&prefix=%20%2Fkey%2F%2F%2E%2E%2F%20&consistency=stale&ca_file=%2Fowned%2Fca.crt&server_name=localhost&client_cert_file=%2Fowned%2Fclient.crt&client_key_file=%2Fowned%2Fclient.key&connect_timeout_seconds=12&request_timeout_seconds=45"
	parsed, err := connections.ParseURI(uri)
	if err != nil || parsed.Password != "original-token" || parsed.Profile.Kind != connections.Consul || parsed.Profile.Username != "" || parsed.Profile.Database != "" || parsed.Profile.ConsulOptions.Prefix != " /key//../ " || parsed.Profile.ConsulOptions.Datacenter != "dc1" || parsed.Profile.ConsulOptions.Consistency != "stale" || strings.Contains(fmt.Sprint(parsed.Profile), parsed.Password) {
		t.Fatal("URI lost original native scope or leaked credential", err)
	}
	plain, err := connections.ParseURI("consul://localhost")
	if err != nil || plain.Profile.Port != 8500 || plain.Profile.TLSMode != "disable" || plain.Profile.ConsulOptions.Auth != "none" {
		t.Fatal("HTTP defaults changed", err)
	}
	secure, err := connections.ParseURI("consuls://localhost")
	if err != nil || secure.Profile.Port != 8501 || secure.Profile.TLSMode != "verify-full" {
		t.Fatal("HTTPS URI defaults changed", err)
	}
	for _, uri := range []string{
		"consul://user:private@localhost", "consul://:private@localhost?auth=none", "consul://localhost/db", "consul://localhost#fragment", "consul://localhost?", "consul://localhost?token=private", "consul://localhost?dc=dc1&dc=dc2", "consul://localhost?namespace=foreign", "consul://localhost?partition=foreign", "consul://localhost?sslmode=require", "consul://localhost?auth=certificate", "consul://localhost?prefix=%00", "consul://localhost?request_timeout_seconds=3e1", "consul://localhost?connect_timeout_seconds=010", "consul://localhost?ca_file=/owned/ca.crt", "consuls://localhost?client_cert_file=/owned/client.crt", "consul://localhost:", "consul://localhost:0", "consul://localhost:65536",
	} {
		if parsed, err := connections.ParseURI(uri); err == nil || parsed.Password != "" || strings.Contains(err.Error(), "private") {
			t.Fatal("ambiguous native URI accepted or secret echoed", uri)
		}
	}
}
