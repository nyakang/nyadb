package connections_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestEtcdProfileExactPrefixCredentialsAndPrivateURI(t *testing.T) {
	for _, encoding := range []string{"text", "base64"} {
		for _, prefix := range []string{"", " /Case/键🐱 ", strings.Repeat("a", 4096)} {
			original := []byte(prefix)
			if encoding == "base64" {
				original = append(original, 0, 255)
				prefix = base64.StdEncoding.EncodeToString(original)
			}
			q := url.Values{"prefix": {prefix}, "prefix_encoding": {encoding}}
			u := url.URL{Scheme: "etcds", Host: "[::1]:2389", User: url.UserPassword(" exact 用户 ", " 原始密码🐱\t "), RawQuery: q.Encode()}
			parsed, err := connections.ParseEtcdURI(u.String())
			if len(original) > 4096 {
				if err == nil {
					t.Fatal("oversize binary prefix accepted")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			p := parsed.Profile
			if p.Username != " exact 用户 " || parsed.Password != " 原始密码🐱\t " || p.Database != "" || p.EtcdOptions.Prefix != prefix || strings.Contains(fmt.Sprint(p), parsed.Password) {
				t.Fatal("identity or credential changed")
			}
			cfg, err := connections.EtcdProfileConfig(p)
			if err != nil || cfg.Endpoint != "https://[::1]:2389" {
				t.Fatal("endpoint", err)
			}
			got, err := connections.EtcdPrefixBytes(cfg.Options)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatal("prefix bytes changed")
			}
			if len(got) > 0 {
				got[0] ^= 1
				again, _ := connections.EtcdPrefixBytes(cfg.Options)
				if !bytes.Equal(again, original) {
					t.Fatal("prefix snapshot aliased")
				}
			}
		}
	}
	if _, ok := connections.Describe(connections.Etcd); ok {
		t.Fatal("private phase published descriptor")
	}
	if _, err := connections.ParseURI("etcd://localhost"); err == nil {
		t.Fatal("private phase published generic URI")
	}
	p, _ := connections.Defaults(connections.Etcd)
	p.Name = "private"
	if p.Port != 2379 || p.EtcdOptions != connections.DefaultEtcdOptions() {
		t.Fatal("defaults")
	}
	p.EtcdOptions.Auth = "basic"
	p.Username = " exact "
	p, err := connections.NormalizeProfile(p)
	if err != nil || p.Username != " exact " {
		t.Fatal("normalization", err)
	}
	cfg, _ := connections.EtcdProfileConfig(p)
	if connections.ValidateEtcdCredential(cfg, "", false) != nil || connections.ValidateEtcdCredential(cfg, "", true) == nil {
		t.Fatal("deferred credential contract")
	}
}

func TestEtcdRejectsAmbiguousURIAndForeignConfiguration(t *testing.T) {
	for _, raw := range []string{
		"etcd://host/db", "etcd://host/%2f", "etcd://host?", "etcd://host#", "etcd://host:0", "etcd://host:", "http://host", "etcd://host,other",
		"etcd://host?prefix=a&prefix=b", "etcd://host?Prefix=a", "etcd://host?password=secret", "etcd://host?auth=basic",
		"etcd://user:secret@host?auth=none", "etcd://host?auth=certificate", "etcd://host?auth=bearer", "etcd://host?ca_file=file",
		"etcds://host?auth=none&client_cert_file=c&client_key_file=k", "etcds://host?auth=certificate&client_cert_file=c",
		"etcd://host?prefix_encoding=raw", "etcd://host?prefix=%00", "etcd://host?prefix=%FF", "etcd://host?prefix_encoding=base64&prefix=YR%3D%3D",
		"etcd://host?prefix_encoding=base64&prefix=YQ%3D%3D%0A", "etcd://host?connect_timeout_seconds=01", "etcd://host?connect_timeout_seconds=301", "etcd://host?request_timeout_seconds=601",
	} {
		if _, err := connections.ParseEtcdURI(raw); !errors.Is(err, connections.ErrURIOptions) {
			t.Fatalf("invalid URI accepted: %s", raw)
		}
	}
	p, _ := connections.Defaults(connections.Etcd)
	p.Name = "strict"
	for _, mutate := range []func(*connections.Profile){
		func(p *connections.Profile) { p.Kind = connections.PostgreSQL },
		func(p *connections.Profile) { p.PostgreSQLOptions = connections.DefaultPostgreSQLOptions() },
		func(p *connections.Profile) { p.Database = " " },
		func(p *connections.Profile) { p.Host = "bad\x01host" },
		func(p *connections.Profile) { p.TLSMode = "require" },
		func(p *connections.Profile) { p.EtcdOptions.Version = 2 },
		func(p *connections.Profile) { p.EtcdOptions.Prefix = strings.Repeat("a", 4097) },
		func(p *connections.Profile) { p.Username = "unexpected" },
	} {
		bad := p
		mutate(&bad)
		if _, err := connections.NormalizeProfile(bad); err == nil {
			t.Fatal("invalid profile accepted")
		}
	}
	q := url.Values{"auth": {"certificate"}, "client_cert_file": {"/client.crt"}, "client_key_file": {"/client.key"}, "ca_file": {"/ca.crt"}}
	parsed, err := connections.ParseEtcdURI("etcds://localhost?" + q.Encode())
	if err != nil {
		t.Fatal("pure certificate config must not load files", err)
	}
	cfg, _ := connections.EtcdProfileConfig(parsed.Profile)
	if connections.ValidateEtcdCredential(cfg, "secret", false) == nil {
		t.Fatal("certificate auth accepted password")
	}
}
