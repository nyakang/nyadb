package dbruntime

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	clickhousesvc "github.com/nyakang/nyadb/internal/services/clickhouse"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/duckdb"
	"github.com/nyakang/nyadb/internal/services/engines"
	mongosvc "github.com/nyakang/nyadb/internal/services/mongodb"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	oraclesvc "github.com/nyakang/nyadb/internal/services/oracle"
	redissvc "github.com/nyakang/nyadb/internal/services/redis"
	"github.com/nyakang/nyadb/internal/services/sqlserver"
)

func RegisterBuiltins(m *Manager, mysqlTemplate *mysqlsvc.MySQLSessionManager) {
	m.Register(connections.Redis, ProviderFunc(redissvc.OpenSession))
	m.Register(connections.MongoDB, ProviderFunc(mongosvc.OpenSession))
	m.Register(connections.Oracle, ProviderFunc(oraclesvc.OpenSession))
	m.Register(connections.DuckDB, ProviderFunc(duckdb.OpenSession))
	m.Register(connections.SQLServer, ProviderFunc(sqlserver.OpenSession))
	m.Register(connections.ClickHouse, ProviderFunc(clickhousesvc.OpenSession))
	for _, kind := range []connections.Kind{connections.MySQL, connections.MariaDB, connections.TiDB} {
		m.Register(kind, ProviderFunc(func(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
			return mysqlsvc.OpenSession(ctx, p, password, mysqlTemplate)
		}))
	}
	for _, kind := range []connections.Kind{connections.PostgreSQL, connections.SQLite} {
		m.Register(kind, ProviderFunc(func(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
			return engines.OpenSession(ctx, p, password)
		}))
	}
}

// Administration returns an operation-bound optional backend interface.
func (m *Manager) Administration(id int64) mysqlsvc.Administration {
	s, e := m.Session(id)
	if e != nil {
		return nil
	}
	p, e := contract.Require[interface {
		Administration() mysqlsvc.Administration
	}](s)
	if e != nil {
		return nil
	}
	return &administration{Administration: p.Administration(), id: id, generation: s.Generation(), session: s.(*session)}
}
