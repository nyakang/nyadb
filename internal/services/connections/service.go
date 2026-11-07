package connections

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Repository is the persistence boundary for non-secret connection profiles.
type Repository interface {
	Create(context.Context, Profile) (Profile, error)
	Get(context.Context, int64) (Profile, error)
	List(context.Context) ([]Profile, error)
	Update(context.Context, Profile) (Profile, error)
	Delete(context.Context, int64) error
}

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) List(ctx context.Context) ([]Profile, error) { return s.repository.List(ctx) }
func (s *Service) Get(ctx context.Context, id int64) (Profile, error) {
	return s.repository.Get(ctx, id)
}
func (s *Service) Create(ctx context.Context, p Profile) (Profile, error) {
	return s.repository.Create(ctx, p)
}
func (s *Service) Update(ctx context.Context, p Profile) (Profile, error) {
	return s.repository.Update(ctx, p)
}
func (s *Service) Delete(ctx context.Context, id int64) error { return s.repository.Delete(ctx, id) }

// Duplicate clones only connection metadata and intentionally never credentials.
func (s *Service) Duplicate(ctx context.Context, id int64, name string) (Profile, error) {
	p, err := s.repository.Get(ctx, id)
	if err != nil {
		return Profile{}, err
	}
	p.ID = 0
	p.Name = strings.TrimSpace(name)
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = p.UpdatedAt.UTC()
	if p.Name == "" {
		return Profile{}, fmt.Errorf("%w: duplicate name required", ErrInvalid)
	}
	return s.repository.Create(ctx, p)
}

// Defaults are UI-independent initial values, not connection attempts.
func Defaults(kind Kind) (Profile, error) {
	switch kind {
	case Solr:
		return Profile{Kind: Solr, Host: "localhost", Port: 8983, TLSMode: "verify-full", SolrOptions: DefaultSolrOptions()}, nil
	case Easysearch:
		return Profile{Kind: Easysearch, Host: "localhost", Port: 9200, Username: "admin", TLSMode: "verify-full", EasysearchOptions: DefaultEasysearchOptions()}, nil
	case Elasticsearch:
		return Profile{Kind: Elasticsearch, Host: "localhost", Port: 9200, Username: "elastic", TLSMode: "verify-full", ElasticsearchOptions: DefaultElasticsearchOptions()}, nil
	case Trino:
		return Profile{Kind: Trino, Host: "localhost", Port: 8080, Username: "trino", TLSMode: "verify-full", TrinoOptions: TrinoOptions{Version: 1}}, nil
	case StarRocks:
		return Profile{Kind: StarRocks, Host: "localhost", Port: 9030, Username: "root", TLSMode: "verify-full", StarRocksOptions: StarRocksOptions{Version: 1, Catalog: "default_catalog"}}, nil
	case Doris:
		return Profile{Kind: Doris, Host: "localhost", Port: 9030, Username: "root", TLSMode: "verify-full", DorisOptions: DorisOptions{Version: 1, Catalog: "internal"}}, nil
	case Dameng:
		return Profile{Kind: Dameng, Host: "localhost", Port: 5236, Database: "DAMENG", Username: "SYSDBA", TLSMode: "verify-full", DamengOptions: DefaultDamengOptions()}, nil
	case Redis:
		return Profile{Kind: Redis, Host: "localhost", Port: 6379, Database: "0", TLSMode: "disable", RedisOptions: DefaultRedisOptions()}, nil
	case Oracle:
		return Profile{Kind: Oracle, Host: "localhost", Port: 1521, Database: "XEPDB1", Username: "", TLSMode: "disable", OracleOptions: DefaultOracleOptions()}, nil
	case MongoDB:
		return Profile{Kind: MongoDB, Host: "localhost", Port: 27017, MongoDBOptions: DefaultMongoDBOptions(), TLSMode: "disable"}, nil
	case DuckDB:
		return Profile{Kind: DuckDB, Database: ":memory:", DuckDBOptions: DefaultDuckDBOptions()}, nil
	case SQLServer:
		return Profile{Kind: SQLServer, Host: "localhost", Port: 1433, Database: "master", Username: "sa", TLSMode: "verify-full", SQLServerOptions: DefaultSQLServerOptions()}, nil
	case MySQL, MariaDB:
		return Profile{Kind: kind, Host: "localhost", Port: 3306, Username: "root", MySQLAdvanced: DefaultMySQLAdvanced()}, nil
	case OceanBase:
		return Profile{Kind: kind, Host: "localhost", Port: 2881, Username: "root@test", MySQLAdvanced: DefaultMySQLAdvanced()}, nil
	case TiDB:
		return Profile{Kind: kind, Host: "localhost", Port: 4000, Username: "root", MySQLAdvanced: DefaultMySQLAdvanced()}, nil
	case CockroachDB:
		return Profile{Kind: CockroachDB, Host: "localhost", Port: 26257, Database: "defaultdb", Username: "root", TLSMode: "verify-full", PostgreSQLOptions: DefaultPostgreSQLOptions()}, nil
	case OpenGauss:
		return Profile{Kind: OpenGauss, Host: "localhost", Port: 5432, Database: "postgres", Username: "gaussdb", TLSMode: "verify-full", PostgreSQLOptions: DefaultPostgreSQLOptions()}, nil
	case GaussDB:
		return Profile{Kind: GaussDB, Host: "localhost", Port: 5432, Database: "postgres", Username: "gaussdb", TLSMode: "verify-full", PostgreSQLOptions: DefaultPostgreSQLOptions(), GaussDBOptions: DefaultGaussDBOptions()}, nil
	case Kingbase:
		return Profile{Kind: Kingbase, Host: "localhost", Port: 54321, Database: "test", Username: "system", TLSMode: "verify-full", PostgreSQLOptions: DefaultPostgreSQLOptions()}, nil
	case PostgreSQL:
		return Profile{Kind: PostgreSQL, Host: "localhost", Port: 5432, Database: "postgres", Username: "postgres", PostgreSQLOptions: DefaultPostgreSQLOptions()}, nil
	case SQLite:
		return Profile{Kind: SQLite, SQLiteOptions: DefaultSQLiteOptions()}, nil
	case ClickHouse:
		return Profile{Kind: ClickHouse, Host: "localhost", Port: 9000, Database: "default", Username: "default", TLSMode: "disable"}, nil
	default:
		return Profile{}, errors.Join(ErrInvalid, fmt.Errorf("unsupported connection type"))
	}
}
