package connections_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

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
