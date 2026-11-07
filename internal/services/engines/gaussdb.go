package engines

import (
	"context"
	"database/sql"
	"net"

	"github.com/nyakang/nyadb/internal/services/connections"
	gaussdbsvc "github.com/nyakang/nyadb/internal/services/gaussdb"
)

// Native catalog rules are shared only after each product's own connector
// verifies the physical server. GaussDB does not inherit management features.
type gaussDBDialect struct{ openGaussDialect }

func (gaussDBDialect) open(p connections.Profile, password string, dial func(context.Context, string, string) (net.Conn, error)) (*sql.DB, error) {
	p, err := connections.NormalizeProfile(p)
	if err != nil {
		return nil, err
	}
	cfg, err := connections.GaussDBProfileConfig(p)
	if err != nil {
		return nil, err
	}
	connector, err := gaussdbsvc.NewSessionConnector(cfg, password, dial)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}
