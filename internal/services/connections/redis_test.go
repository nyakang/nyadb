package connections_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestRedisTopologyURIValidationAndSeparateCredentials(t *testing.T) {
	for _, uri := range []string{
		"redis://acl:secret@localhost:7000/0?topology=cluster&seeds=localhost:7001,[::1]:7002",
		"rediss://acl:secret@localhost:26379/3?topology=sentinel&seeds=localhost:26380&master_name=mymaster&sentinel_username=monitor&sentinel_tls=true",
	} {
		u, err := connections.ParseURI(uri)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(u.Profile)
		if strings.Contains(string(raw), "secret") {
			t.Fatal("URI password entered public profile")
		}
		if u.Profile.RedisOptions.Topology == "standalone" {
			t.Fatal("topology lost")
		}
	}
	for _, uri := range []string{
		"redis://localhost/1?topology=cluster", "redis://localhost?topology=sentinel",
		"redis://localhost?topology=cluster&seeds=localhost", "redis://localhost?topology=cluster&seeds=localhost:0",
		"redis://localhost?topology=cluster&seeds=localhost:1,localhost:1", "redis://localhost?seeds=localhost:1",
		"redis://localhost?topology=cluster&master_name=m", "redis://localhost?topology=other",
		"redis://localhost?topology=sentinel&master_name=m&sentinel_password=secret",
	} {
		if _, err := connections.ParseURI(uri); err == nil {
			t.Fatalf("invalid topology accepted: %s", uri)
		}
	}
	ctx := connections.WithRedisSentinelPassword(context.Background(), "sentinel-only-secret")
	if connections.RedisSentinelPassword(ctx) != "sentinel-only-secret" || connections.RedisSentinelPassword(context.Background()) != "" {
		t.Fatal("transient credential crossed context scope")
	}
}

func TestRedisURIAndOptionsPreservePublicSettings(t *testing.T) {
	u, e := connections.ParseURI("rediss://acl:temp%40secret@[::1]:6380/3")
	if e != nil || u.Password != "temp@secret" || u.Profile.Kind != connections.Redis || u.Profile.Database != "3" || u.Profile.Host != "::1" || u.Profile.TLSMode != "verify-full" {
		t.Fatal("URI parsing", e)
	}
	raw, _ := json.Marshal(u.Profile)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("password in profile")
	}
	for _, bad := range []string{"redis://localhost/-1", "redis://localhost/01", "redis://localhost/1?db=2", "redis://localhost/2/3", "redis://localhost:0/0", "redis://localhost/65536"} {
		if _, e := connections.ParseURI(bad); e == nil {
			t.Fatal("unsupported URI accepted")
		}
	}
	p, _ := connections.Defaults(connections.Redis)
	p.Name = "Redis"
	p.RedisOptions.ServerName = "server"
	if _, e = connections.NormalizeProfile(p); e == nil {
		t.Fatal("TLS option without TLS accepted")
	}
	p.RedisOptions = connections.DefaultRedisOptions()
	p.RedisOptions.QueryMaxConnections = 65
	if _, e = connections.NormalizeProfile(p); e == nil {
		t.Fatal("invalid pool accepted")
	}
	p.Kind = connections.MySQL
	if _, e = connections.NormalizeProfile(p); e == nil {
		t.Fatal("cross-engine options accepted")
	}
	d, _ := connections.Describe(connections.Redis)
	if !d.Capabilities.KeyValues || d.Capabilities.Documents || d.Capabilities.Transactions || d.Capabilities.DesignTables {
		t.Fatal("misleading Redis capabilities")
	}
}
