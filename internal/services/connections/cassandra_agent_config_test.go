package connections_test

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCassandraAgentConfigurationCapturesContactPointsWithoutChangingExistingTransport(t *testing.T) {
	c := connections.CassandraAgentConfig{ContactPoints: []string{"127.0.0.1", "2001:db8::1"}, LocalDatacenter: "dc-one"}
	n, err := connections.NormalizeCassandraAgentConfig(c)
	if err != nil || n.Port != 9042 || n.ConnectTimeoutMS != 15000 {
		t.Fatal("native agent defaults", err)
	}
	c.ContactPoints[0] = "replacement.invalid"
	if n.ContactPoints[0] != "127.0.0.1" {
		t.Fatal("captured native contact points aliased caller")
	}
	// The existing public v4 transport configuration retains its field formats.
	if _, err := connections.NormalizeCassandraConfig(connections.CassandraConfig{Endpoint: "localhost:9042", TLSMode: "disable"}); err != nil {
		t.Fatal("existing public native configuration changed", err)
	}
	for _, hosts := range [][]string{nil, {"host:9042"}, {"host", "host"}, {"http://host"}, {"host\n"}, {"host,other"}} {
		if _, err := connections.NormalizeCassandraAgentConfig(connections.CassandraAgentConfig{ContactPoints: hosts}); err == nil {
			t.Fatal("ambiguous native contacts accepted", hosts)
		}
	}
}

func TestCassandraAgentProtocolVersionsMatchOriginalNativeOptions(t *testing.T) {
	for _, version := range []int{-1, 0, 1, 2, 3, 4, 5, 6} {
		_, err := connections.NormalizeCassandraAgentConfig(connections.CassandraAgentConfig{ContactPoints: []string{"localhost"}, ProtocolVersion: version})
		valid := version == 0 || version >= 3 && version <= 5
		if (err == nil) != valid {
			t.Fatal("original native protocol option boundary changed", version, err)
		}
	}
}
