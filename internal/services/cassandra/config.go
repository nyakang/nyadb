package cassandra

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// AgentConfig configures the separate managed original CQL agent. Public
// managed-mode publication requires its own product acceptance.
type AgentConfig = connections.CassandraAgentConfig

var (
	ErrConfig = connections.ErrInvalid
	ErrValue  = errors.New("native Cassandra value projection is incomplete")
)

func validName(v string, empty bool) bool {
	return connections.ValidCassandraName(v, empty)
}

func connectionParams(c AgentConfig, secret string) map[string]any {
	// Fallthrough disables native query retries. Keep the original transport
	// setup attempts: the shared retries option also configures reconnection,
	// and zero prevents a connection and crashes the pinned driver's pool.
	options := "retry=FallthroughRetryPolicy&connecttimeout=" + strconv.Itoa(c.ConnectTimeoutMS) + "ms&requesttimeout=" + strconv.Itoa(c.RequestTimeoutMS) + "ms"
	if c.ProtocolVersion != 0 {
		options += "&protocolversion=" + strconv.Itoa(c.ProtocolVersion)
	}
	if c.LocalDatacenter != "" {
		// A URL encoder is used at the protocol boundary below.
		options += "&localdatacenter=" + escapeOption(c.LocalDatacenter)
	}
	return map[string]any{"host": strings.Join(c.ContactPoints, ","), "port": c.Port, "database": c.Keyspace, "username": c.Username, "password": secret, "url_params": options}
}

func configurationFingerprint(c AgentConfig) string {
	raw, _ := json.Marshal(c)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
