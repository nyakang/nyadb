package engines

import (
	"context"
	"database/sql"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"net"
)

type dialectStrategy interface {
	open(connections.Profile, string, func(context.Context, string, string) (net.Conn, error)) (*sql.DB, error)
	catalog(string) string
	namespace(string) error
	argument(int) string
	schemas(context.Context, queryer) ([]string, error)
	objects(context.Context, queryer, string) (*sql.Rows, error)
	structure(context.Context, *Session, queryer, contract.ObjectRef) (contract.Table, error)
	setSchema(context.Context, *sql.Conn, string) error
	comparison(contract.Field, string, string) string
}

func strategyFor(kind connections.Kind) (dialectStrategy, error) {
	switch kind {
	case connections.OpenGauss:
		return openGaussDialect{}, nil
	case connections.CockroachDB:
		return cockroachDialect{}, nil
	case connections.PostgreSQL:
		return postgresDialect{}, nil
	case connections.SQLite:
		return sqliteDialect{}, nil
	}
	return nil, contract.ErrUnsupported
}
