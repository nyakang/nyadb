package dbruntime

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/cassandra"
	clickhousesvc "github.com/nyakang/nyadb/internal/services/clickhouse"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dameng"
	"github.com/nyakang/nyadb/internal/services/doris"
	"github.com/nyakang/nyadb/internal/services/duckdb"
	"github.com/nyakang/nyadb/internal/services/easysearch"
	"github.com/nyakang/nyadb/internal/services/elasticsearch"
	"github.com/nyakang/nyadb/internal/services/engines"
	mongosvc "github.com/nyakang/nyadb/internal/services/mongodb"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	oraclesvc "github.com/nyakang/nyadb/internal/services/oracle"
	redissvc "github.com/nyakang/nyadb/internal/services/redis"
	"github.com/nyakang/nyadb/internal/services/solr"
	"github.com/nyakang/nyadb/internal/services/sqlserver"
	"github.com/nyakang/nyadb/internal/services/starrocks"
	"github.com/nyakang/nyadb/internal/services/trino"
)

func RegisterBuiltins(m *Manager, mysqlTemplate *mysqlsvc.MySQLSessionManager) {
	m.Register(connections.Cassandra, ProviderFunc(cassandra.OpenSession))
	m.Register(connections.Solr, ProviderFunc(solr.OpenSession))
	m.Register(connections.Easysearch, ProviderFunc(easysearch.OpenSession))
	m.Register(connections.Elasticsearch, ProviderFunc(elasticsearch.OpenSession))
	m.Register(connections.Trino, ProviderFunc(trino.OpenSession))
	m.Register(connections.StarRocks, ProviderFunc(starrocks.OpenSession))
	m.Register(connections.Doris, ProviderFunc(doris.OpenSession))
	m.Register(connections.Dameng, ProviderFunc(dameng.OpenSession))
	m.Register(connections.Redis, ProviderFunc(redissvc.OpenSession))
	m.Register(connections.MongoDB, ProviderFunc(mongosvc.OpenSession))
	m.Register(connections.Oracle, ProviderFunc(oraclesvc.OpenSession))
	m.Register(connections.DuckDB, ProviderFunc(duckdb.OpenSession))
	m.Register(connections.SQLServer, ProviderFunc(sqlserver.OpenSession))
	m.Register(connections.ClickHouse, ProviderFunc(clickhousesvc.OpenSession))
	for _, kind := range []connections.Kind{connections.MySQL, connections.MariaDB, connections.TiDB, connections.OceanBase} {
		m.Register(kind, ProviderFunc(func(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
			return mysqlsvc.OpenSession(ctx, p, password, mysqlTemplate)
		}))
	}
	for _, kind := range []connections.Kind{connections.PostgreSQL, connections.CockroachDB, connections.OpenGauss, connections.GaussDB, connections.Kingbase, connections.SQLite} {
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
