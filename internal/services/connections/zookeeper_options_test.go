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

func TestZooKeeperProfilePreservesEnsembleRootAndPinnedDriver(t *testing.T) {
	p, err := connections.Defaults(connections.ZooKeeper)
	if err != nil || p.Port != 2181 || p.Database != "" || p.Username != "" || !p.Kind.IsNativeKV() {
		t.Fatal("native defaults changed", err)
	}
	d, ok := connections.Describe(p.Kind)
	if !ok || !d.Capabilities.KeyValues || !d.Capabilities.Export || d.Capabilities.Metadata || d.Capabilities.Query {
		t.Fatal("native product advertised unrelated capabilities")
	}
	p.Name, p.Host, p.Username, p.TLSMode = "original", "2001:db8::1", "dbx@ops", "verify-full"
	a := &p.ZooKeeperOptions
	a.AdditionalServers, a.Chroot, a.Namespace, a.Prefix = "zk-2:2281,[2001:db8::2]:2381", "/services/app", "/原空间", "/logical"
	a.Auth = "digest"
	a.CAFile, a.ClientCertFile, a.ClientKeyFile = "/owned/ca.crt", "/owned/client.crt", "/owned/client.key"
	a.DriverVersion, a.DriverSHA256, a.DriverDirectory = "native-1", strings.Repeat("a", 64), t.TempDir()
	original := p.ZooKeeperOptions
	p, err = connections.NormalizeProfile(p)
	if err != nil || p.ZooKeeperOptions != original {
		t.Fatal("captured public options changed", err)
	}
	c, err := connections.ZooKeeperProfileConfig(p)
	if err != nil || c.ConnectString != "[2001:db8::1]:2181,zk-2:2281,[2001:db8::2]:2381/services/app" || c.Namespace != "/原空间" || c.Prefix != "/logical" || !c.SSL || c.Username != "dbx@ops" || c.EndpointRoutes != nil {
		t.Fatal("ensemble or logical identity changed", err)
	}
	_, root, err := connections.NormalizeZooKeeperConfig(c)
	if err != nil || root != "/services/app/原空间" {
		t.Fatal("chroot and namespace were not composed exactly", err)
	}
	if err = connections.ValidateZooKeeperCredential(p, "original-secret"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "original-secret") {
		t.Fatal("secret entered profile")
	}
	for _, change := range []func(*connections.Profile){
		func(p *connections.Profile) { p.Database = "invented" },
		func(p *connections.Profile) { p.Host = "other:2181" },
		func(p *connections.Profile) { p.Host = "host\\path" },
		func(p *connections.Profile) { p.Host = "host\u00a0name" },
		func(p *connections.Profile) { p.Username = " ops " },
		func(p *connections.Profile) { p.Username = "ops:other" },
		func(p *connections.Profile) { p.ZooKeeperOptions.AdditionalServers = "[2001:db8::1]:2181" },
		func(p *connections.Profile) { p.ZooKeeperOptions.AdditionalServers = "zk-2:2281,,zk-3:2381" },
		func(p *connections.Profile) { p.ZooKeeperOptions.AdditionalServers = "zk-2:0" },
		func(p *connections.Profile) { p.ZooKeeperOptions.Chroot = "/a/../b" },
		func(p *connections.Profile) { p.ZooKeeperOptions.Namespace = "/a//b" },
		func(p *connections.Profile) { p.ZooKeeperOptions.Prefix = "/a\x00b" },
		func(p *connections.Profile) { p.ZooKeeperOptions.Version = 2 },
		func(p *connections.Profile) { p.ZooKeeperOptions.SessionTimeoutMS = 999 },
		func(p *connections.Profile) { p.ZooKeeperOptions.ConnectionTimeoutMS = 300001 },
		func(p *connections.Profile) { p.ZooKeeperOptions.MaxBufferSize = 256<<20 + 1 },
		func(p *connections.Profile) { p.ZooKeeperOptions.MaxDocuments = 33 },
		func(p *connections.Profile) { p.ZooKeeperOptions.DriverSHA256 = "" },
		func(p *connections.Profile) { p.ZooKeeperOptions.DriverSHA256 = strings.Repeat("A", 64) },
		func(p *connections.Profile) { p.ZooKeeperOptions.DriverDirectory = filepath.Join("relative", "driver") },
		func(p *connections.Profile) { p.ZooKeeperOptions.ClientKeyFile = "" },
		func(p *connections.Profile) { p.ZooKeeperOptions.Auth = "kerberos" },
		func(p *connections.Profile) { p.ZooKeeperOptions.Auth = "none" },
		func(p *connections.Profile) { p.TLSMode = "require" },
		func(p *connections.Profile) { p.TLSMode = "disable" },
		func(p *connections.Profile) { p.MySQLAdvanced.Charset = "utf8mb4" },
		func(p *connections.Profile) { p.Kind = connections.MySQL },
	} {
		candidate := p
		change(&candidate)
		if _, err := connections.NormalizeProfile(candidate); err == nil {
			t.Fatal("invalid or foreign configuration accepted", fmt.Sprint(candidate))
		}
	}
}

func TestZooKeeperURIEnsembleDBXSpellingsAndTemporaryCredentials(t *testing.T) {
	parsed, err := connections.ParseURI("zookeeper://dbx%40ops:p%40ss@[2001:db8::1]:2181,[2001:db8::2]:2281/services/app?namespace=%2Fns&prefix=%2Flogical&session_timeout=30000&connect_timeout=5000&name=Production")
	if err != nil || parsed.Password != "p@ss" || parsed.Profile.Name != "Production" || parsed.Profile.Host != "2001:db8::1" || parsed.Profile.ZooKeeperOptions.AdditionalServers != "[2001:db8::2]:2281" || parsed.Profile.ZooKeeperOptions.ConnectionTimeoutMS != 5000 || parsed.Profile.ZooKeeperOptions.Chroot != "/services/app" || parsed.Profile.ZooKeeperOptions.Namespace != "/ns" || parsed.Profile.ZooKeeperOptions.Prefix != "/logical" || strings.Contains(fmt.Sprint(parsed.Profile), "p@ss") {
		t.Fatal("DBX ensemble URL lost original options", err)
	}
	direct, err := connections.ParseZooKeeperURI("ZOOKEEPER://localhost")
	if err != nil || direct.Profile.Port != 2181 || direct.Profile.ZooKeeperOptions.Auth != "none" || direct.Profile.ZooKeeperOptions.Prefix != "/" {
		t.Fatal("direct parser defaults changed", err)
	}
	directory := t.TempDir()
	q := url.Values{"driver_version": {"native-1"}, "driver_sha256": {strings.Repeat("a", 64)}, "driver_directory": {directory}}
	pinned, err := connections.ParseURI("zookeeper://localhost?" + q.Encode())
	if err != nil || pinned.Profile.ZooKeeperOptions.DriverDirectory != directory || pinned.Profile.ZooKeeperOptions.DriverSHA256 != strings.Repeat("a", 64) {
		t.Fatal("explicit installed package pin was lost", err)
	}
	for _, raw := range []string{
		"zookeeper://localhost#", "zookeeper://localhost?", "zookeeper://localhost?namespace=", "zookeeper://localhost?namespace=/a&namespace=/b", "zookeeper://localhost?session_timeout=30000&session_timeout_ms=30000", "zookeeper://localhost?connect_timeout=5000&connection_timeout_ms=5000", "zookeeper://localhost?session_timeout_ms=030000", "zookeeper://localhost?max_buffer_size=3e7", "zookeeper://localhost?max_documents=33", "zookeeper://localhost?password=private", "zookeeper://localhost?auth_scheme=sasl_digest", "zookeeper://ops:private@localhost?auth_scheme=none", "zookeeper://localhost?sslmode=require", "zookeeper://localhost?ca_file=/ca.crt", "zookeeper://localhost?driver_version=native-1", "zookeeper://localhost:2181,localhost:2181", "zookeeper://localhost:2181,,zk-2:2181", "zookeeper://localhost:", "zookeeper://localhost:65536", "zookeeper://2001:db8::1:2181", "zookeeper://localhost/a/%2e%2e/b", "zookeeper://localhost?prefix=%00", "zookeeper://localhost/\x00", "zookeeper://localhost/" + strings.Repeat("a", 17<<10),
	} {
		for _, parse := range []func(string) (connections.URIImport, error){connections.ParseURI, connections.ParseZooKeeperURI} {
			got, err := parse(raw)
			if err == nil || got.Password != "" || strings.Contains(err.Error(), "private") {
				t.Fatal("ambiguous URI accepted or credential exposed", raw)
			}
		}
	}
}

func TestZooKeeperCredentialModesAndCapturedRouteCopies(t *testing.T) {
	p, _ := connections.Defaults(connections.ZooKeeper)
	if connections.ValidateZooKeeperCredential(p, "secret") == nil {
		t.Fatal("no-auth accepted hidden credentials")
	}
	p.ZooKeeperOptions.Auth, p.Username = "sasl_digest", "ops"
	if connections.ValidateZooKeeperCredential(p, "") == nil || connections.ValidateZooKeeperCredential(p, "secret") != nil {
		t.Fatal("SASL credentials were not required")
	}
	c := connections.ZooKeeperConfig{ConnectString: "zk-1:2181,zk-2:2181/app", Namespace: "/ns", EndpointRoutes: map[string]string{"zk-1:2181": "127.0.0.1:12181", "zk-2:2181": "127.0.0.1:12182"}}
	normalized, root, err := connections.NormalizeZooKeeperConfig(c)
	if err != nil || root != "/app/ns" {
		t.Fatal(err)
	}
	c.EndpointRoutes["zk-1:2181"] = "127.0.0.1:9999"
	if normalized.EndpointRoutes["zk-1:2181"] != "127.0.0.1:12181" {
		t.Fatal("captured routes aliased caller input")
	}
}
