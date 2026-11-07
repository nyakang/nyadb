package localdb

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// Keep the existing column and MySQL JSON format. Other engines use an
// explicit versioned envelope; older empty records receive engine defaults.
type engineOptionsEnvelope struct {
	Solr          *connections.SolrOptions          `json:"solr,omitempty"`
	Easysearch    *connections.EasysearchOptions    `json:"easysearch,omitempty"`
	Elasticsearch *connections.ElasticsearchOptions `json:"elasticsearch,omitempty"`
	Trino         *connections.TrinoOptions         `json:"trino,omitempty"`
	StarRocks     *connections.StarRocksOptions     `json:"starrocks,omitempty"`
	Doris         *connections.DorisOptions         `json:"doris,omitempty"`
	Dameng        *connections.DamengOptions        `json:"dameng,omitempty"`
	GaussDB       *connections.GaussDBOptions       `json:"gaussdb,omitempty"`
	Redis         *connections.RedisOptions         `json:"redis,omitempty"`
	MongoDB       *connections.MongoDBOptions       `json:"mongodb,omitempty"`
	Oracle        *connections.OracleOptions        `json:"oracle,omitempty"`
	DuckDB        *connections.DuckDBOptions        `json:"duckdb,omitempty"`
	SQLServer     *connections.SQLServerOptions     `json:"sqlserver,omitempty"`
	Version       int                               `json:"version"`
	Kind          connections.Kind                  `json:"kind"`
	PostgreSQL    *connections.PostgreSQLOptions    `json:"postgresql,omitempty"`
	SQLite        *connections.SQLiteOptions        `json:"sqlite,omitempty"`
}

func encodeProfileOptions(p connections.Profile) (string, error) {
	var value any
	switch p.Kind {
	case connections.Solr:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, Solr: &p.SolrOptions}
	case connections.Easysearch:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, Easysearch: &p.EasysearchOptions}
	case connections.Elasticsearch:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, Elasticsearch: &p.ElasticsearchOptions}
	case connections.Trino:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, Trino: &p.TrinoOptions}
	case connections.StarRocks:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, StarRocks: &p.StarRocksOptions}
	case connections.Doris:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, Doris: &p.DorisOptions}
	case connections.Dameng:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, Dameng: &p.DamengOptions}
	case connections.GaussDB:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, GaussDB: &p.GaussDBOptions, PostgreSQL: &p.PostgreSQLOptions}
	case connections.Redis:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, Redis: &p.RedisOptions}
	case connections.MongoDB:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, MongoDB: &p.MongoDBOptions}
	case connections.Oracle:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, Oracle: &p.OracleOptions}
	case connections.DuckDB:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, DuckDB: &p.DuckDBOptions}
	case connections.SQLServer:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, SQLServer: &p.SQLServerOptions}
	case connections.MySQL, connections.MariaDB, connections.TiDB, connections.OceanBase:
		value = p.MySQLAdvanced
	case connections.PostgreSQL, connections.CockroachDB, connections.OpenGauss, connections.Kingbase:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, PostgreSQL: &p.PostgreSQLOptions}
	case connections.SQLite:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind, SQLite: &p.SQLiteOptions}
	case connections.ClickHouse:
		value = engineOptionsEnvelope{Version: 1, Kind: p.Kind}
	default:
		return "", connections.ErrInvalid
	}
	raw, err := json.Marshal(value)
	return string(raw), err
}
func decodeEngineOptions(p *connections.Profile, raw string) error {
	if raw == "" {
		if p.Kind == connections.Solr {
			return connections.ErrInvalid
		}
		if p.Kind == connections.Easysearch {
			return connections.ErrInvalid
		}
		if p.Kind == connections.Elasticsearch {
			return connections.ErrInvalid
		}
		if p.Kind == connections.Trino {
			p.TrinoOptions = connections.TrinoOptions{Version: 1}
		}
		if p.Kind == connections.StarRocks {
			p.StarRocksOptions = connections.StarRocksOptions{Version: 1, Catalog: "default_catalog"}
		}
		if p.Kind == connections.Doris {
			p.DorisOptions = connections.DorisOptions{Version: 1, Catalog: "internal"}
		}
		if p.Kind == connections.Dameng {
			return connections.ErrInvalid
		}
		if p.Kind == connections.GaussDB {
			p.GaussDBOptions = connections.DefaultGaussDBOptions()
		}
		if p.Kind == connections.Redis {
			p.RedisOptions = connections.DefaultRedisOptions()
		}
		if p.Kind == connections.MongoDB {
			p.MongoDBOptions = connections.DefaultMongoDBOptions()
		}
		if p.Kind == connections.Oracle {
			p.OracleOptions = connections.DefaultOracleOptions()
		}
		if p.Kind == connections.DuckDB {
			p.DuckDBOptions = connections.DefaultDuckDBOptions()
		}
		if p.Kind == connections.SQLServer {
			p.SQLServerOptions = connections.DefaultSQLServerOptions()
		}
		if p.Kind.IsPostgreSQLFamily() {
			p.PostgreSQLOptions = connections.DefaultPostgreSQLOptions()
		} else if p.Kind == connections.SQLite {
			p.SQLiteOptions, _ = connections.NormalizeSQLiteOptions(connections.SQLiteOptions{})
		}
		return nil
	}
	var envelope engineOptionsEnvelope
	if (p.Kind == connections.Solr || p.Kind == connections.Elasticsearch || p.Kind == connections.Easysearch) && !uniqueNativeAuthOptionsJSON(raw) {
		return connections.ErrInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 128<<10 || decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Version != 1 || envelope.Kind != p.Kind {
		return fmt.Errorf("invalid saved engine options")
	}
	if p.Kind == connections.Solr {
		if envelope.Solr == nil || envelope.Solr.Version != 1 || envelope.Solr.ContextPath == "" || envelope.Easysearch != nil || envelope.Elasticsearch != nil || envelope.Trino != nil || envelope.StarRocks != nil || envelope.Doris != nil || envelope.Dameng != nil || envelope.GaussDB != nil || envelope.Redis != nil || envelope.MongoDB != nil || envelope.Oracle != nil || envelope.DuckDB != nil || envelope.SQLServer != nil || envelope.SQLite != nil || envelope.PostgreSQL != nil {
			return connections.ErrInvalid
		}
		p.SolrOptions = *envelope.Solr
		checked, err := connections.NormalizeProfile(*p)
		*p = checked
		return err
	}
	if envelope.Solr != nil {
		return connections.ErrInvalid
	}
	if p.Kind == connections.Easysearch {
		if envelope.Easysearch == nil || envelope.Elasticsearch != nil || envelope.Trino != nil || envelope.StarRocks != nil || envelope.Doris != nil || envelope.Dameng != nil || envelope.GaussDB != nil || envelope.Redis != nil || envelope.MongoDB != nil || envelope.Oracle != nil || envelope.DuckDB != nil || envelope.SQLServer != nil || envelope.SQLite != nil || envelope.PostgreSQL != nil {
			return connections.ErrInvalid
		}
		p.EasysearchOptions = *envelope.Easysearch
		if p.EasysearchOptions.Version != 1 {
			return connections.ErrInvalid
		}
		checked, err := connections.NormalizeProfile(*p)
		*p = checked
		return err
	}
	if envelope.Easysearch != nil {
		return connections.ErrInvalid
	}
	if p.Kind == connections.Elasticsearch {
		if envelope.Elasticsearch == nil || envelope.Trino != nil || envelope.StarRocks != nil || envelope.Doris != nil || envelope.Dameng != nil || envelope.GaussDB != nil || envelope.Redis != nil || envelope.MongoDB != nil || envelope.Oracle != nil || envelope.DuckDB != nil || envelope.SQLServer != nil || envelope.SQLite != nil || envelope.PostgreSQL != nil {
			return connections.ErrInvalid
		}
		p.ElasticsearchOptions = *envelope.Elasticsearch
		if p.ElasticsearchOptions.Version != 1 {
			return connections.ErrInvalid
		}
		checked, err := connections.NormalizeProfile(*p)
		*p = checked
		return err
	}
	if envelope.Elasticsearch != nil {
		return connections.ErrInvalid
	}
	if p.Kind == connections.Trino {
		if envelope.Trino == nil || envelope.StarRocks != nil || envelope.Doris != nil || envelope.Dameng != nil || envelope.GaussDB != nil || envelope.Redis != nil || envelope.MongoDB != nil || envelope.Oracle != nil || envelope.DuckDB != nil || envelope.SQLServer != nil || envelope.SQLite != nil || envelope.PostgreSQL != nil {
			return connections.ErrInvalid
		}
		p.TrinoOptions = *envelope.Trino
		checked, err := connections.NormalizeProfile(*p)
		*p = checked
		return err
	}
	if envelope.Trino != nil {
		return connections.ErrInvalid
	}
	if p.Kind == connections.StarRocks {
		if envelope.StarRocks == nil || envelope.Doris != nil || envelope.Dameng != nil || envelope.GaussDB != nil || envelope.Redis != nil || envelope.MongoDB != nil || envelope.Oracle != nil || envelope.DuckDB != nil || envelope.SQLServer != nil || envelope.SQLite != nil || envelope.PostgreSQL != nil {
			return connections.ErrInvalid
		}
		p.StarRocksOptions = *envelope.StarRocks
		checked, err := connections.NormalizeProfile(*p)
		*p = checked
		return err
	}
	if envelope.StarRocks != nil {
		return connections.ErrInvalid
	}
	if p.Kind == connections.Doris {
		if envelope.Doris == nil || envelope.Dameng != nil || envelope.GaussDB != nil || envelope.Redis != nil || envelope.MongoDB != nil || envelope.Oracle != nil || envelope.DuckDB != nil || envelope.SQLServer != nil || envelope.SQLite != nil || envelope.PostgreSQL != nil {
			return connections.ErrInvalid
		}
		p.DorisOptions = *envelope.Doris
		checked, err := connections.NormalizeProfile(*p)
		*p = checked
		return err
	}
	if envelope.Doris != nil {
		return connections.ErrInvalid
	}
	if p.Kind == connections.Dameng {
		if envelope.Dameng == nil || envelope.GaussDB != nil || envelope.Redis != nil || envelope.MongoDB != nil || envelope.Oracle != nil || envelope.DuckDB != nil || envelope.SQLServer != nil || envelope.SQLite != nil || envelope.PostgreSQL != nil {
			return connections.ErrInvalid
		}
		p.DamengOptions = *envelope.Dameng
		checked, err := connections.NormalizeProfile(*p)
		*p = checked
		return err
	}
	if envelope.Dameng != nil {
		return connections.ErrInvalid
	}
	if p.Kind == connections.GaussDB {
		if envelope.GaussDB == nil || envelope.PostgreSQL == nil || envelope.Redis != nil || envelope.MongoDB != nil || envelope.Oracle != nil || envelope.DuckDB != nil || envelope.SQLServer != nil || envelope.SQLite != nil {
			return connections.ErrInvalid
		}
		p.GaussDBOptions, p.PostgreSQLOptions = *envelope.GaussDB, *envelope.PostgreSQL
		checked, err := connections.NormalizeProfile(*p)
		*p = checked
		return err
	}
	if envelope.GaussDB != nil {
		return connections.ErrInvalid
	}
	if p.Kind == connections.Redis {
		if envelope.Redis == nil || envelope.MongoDB != nil || envelope.Oracle != nil || envelope.DuckDB != nil || envelope.SQLServer != nil || envelope.SQLite != nil || envelope.PostgreSQL != nil {
			return connections.ErrInvalid
		}
		var err error
		p.RedisOptions, err = connections.NormalizeRedisOptions(*envelope.Redis)
		return err
	}
	if envelope.Redis != nil {
		return connections.ErrInvalid
	}
	if p.Kind == connections.Oracle {
		if envelope.MongoDB != nil {
			return connections.ErrInvalid
		}
		if envelope.Oracle == nil || envelope.DuckDB != nil || envelope.SQLServer != nil || envelope.SQLite != nil || envelope.PostgreSQL != nil {
			return connections.ErrInvalid
		}
		checked, err := connections.NormalizeOracleOptions(*envelope.Oracle)
		p.OracleOptions = checked
		return err
	}
	if p.Kind == connections.MongoDB {
		if envelope.MongoDB == nil || envelope.Oracle != nil || envelope.DuckDB != nil || envelope.SQLServer != nil || envelope.SQLite != nil || envelope.PostgreSQL != nil {
			return connections.ErrInvalid
		}
		var err error
		p.MongoDBOptions, err = connections.NormalizeMongoDBOptions(*envelope.MongoDB)
		return err
	}
	if envelope.MongoDB != nil {
		return connections.ErrInvalid
	}
	if envelope.Oracle != nil {
		return connections.ErrInvalid
	}
	if p.Kind == connections.DuckDB {
		if envelope.DuckDB == nil || envelope.SQLServer != nil || envelope.SQLite != nil || envelope.PostgreSQL != nil {
			return connections.ErrInvalid
		}
		checked, err := connections.NormalizeDuckDBOptions(*envelope.DuckDB)
		p.DuckDBOptions = checked
		return err
	}
	if envelope.DuckDB != nil {
		return connections.ErrInvalid
	}
	if p.Kind.IsPostgreSQLFamily() && envelope.PostgreSQL != nil && envelope.SQLite == nil {
		if envelope.SQLServer != nil {
			return connections.ErrInvalid
		}
		p.PostgreSQLOptions = *envelope.PostgreSQL
		checked, err := connections.NormalizePostgreSQLOptions(p.PostgreSQLOptions)
		p.PostgreSQLOptions = checked
		return err
	}
	if p.Kind == connections.SQLite && envelope.SQLite != nil && envelope.PostgreSQL == nil {
		if envelope.SQLServer != nil {
			return connections.ErrInvalid
		}
		p.SQLiteOptions = *envelope.SQLite
		checked, err := connections.NormalizeSQLiteOptions(p.SQLiteOptions)
		p.SQLiteOptions = checked
		return err
	}
	if p.Kind == connections.SQLServer && envelope.SQLServer != nil && envelope.SQLite == nil && envelope.PostgreSQL == nil {
		checked, err := connections.NormalizeSQLServerOptions(*envelope.SQLServer)
		p.SQLServerOptions = checked
		return err
	}
	if p.Kind == connections.ClickHouse && envelope.SQLite == nil && envelope.PostgreSQL == nil && envelope.SQLServer == nil {
		return nil
	}
	return fmt.Errorf("engine options do not match profile")
}
