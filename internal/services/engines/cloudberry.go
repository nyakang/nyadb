package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Cloudberry uses the PostgreSQL protocol, with native distribution, access
// methods and external-table definitions. Its catalog is a PG14 derivative.
type cloudberryDialect struct{ postgresDialect }

func validateCloudberryServer(ctx context.Context, q queryer) error {
	var version string
	var number int
	var policy sql.NullString
	err := queryOne(ctx, q, `SELECT version(),current_setting('server_version_num')::int,
 to_regclass('pg_catalog.gp_distribution_policy')::text`, nil, &version, &number, &policy)
	if err != nil {
		return err
	}
	if !strings.Contains(version, "Apache Cloudberry") || number < 140000 || !policy.Valid {
		return errors.New("Cloudberry requires its native distribution catalog")
	}
	return nil
}

func cloudberryNativeDefinition(ctx context.Context, q queryer, o contract.ObjectRef) (string, bool, error) {
	// pg_get_tabledef is a community helper, not a server built-in. Detect it
	// before calling: a failed query would abort the owning read transaction.
	var schema string
	for _, candidate := range []string{"pg_catalog", "public"} {
		var available bool
		if err := queryOne(ctx, q, `SELECT to_regprocedure($1) IS NOT NULL`, []any{candidate + ".pg_get_tabledef(text,text,boolean)"}, &available); err != nil {
			return "", false, err
		}
		if available {
			schema = candidate
			break
		}
	}
	if schema == "" {
		return "", false, nil
	}
	var ddl sql.NullString
	if err := queryOne(ctx, q, "SELECT "+quote(schema)+".pg_get_tabledef($1,$2,true)", []any{o.Schema, o.Name}, &ddl); err != nil {
		return "", true, err
	}
	if !ddl.Valid || strings.TrimSpace(ddl.String) == "" || len(ddl.String) > 1<<20 || !utf8.ValidString(ddl.String) || strings.ContainsRune(ddl.String, 0) {
		return "", true, errors.New("complete Cloudberry table definition unavailable or exceeds 1 MiB")
	}
	return ddl.String, true, nil
}

const cloudberryModifiersSQL = `SELECT c.relkind::text,COALESCE(am.amname,''),
 COALESCE(array_to_json(c.reloptions)::text,'[]'),COALESCE(fs.srvname,''),
 COALESCE(array_to_json(ft.ftoptions)::text,'[]')
 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 LEFT JOIN pg_catalog.pg_am am ON am.oid=c.relam
 LEFT JOIN pg_catalog.pg_foreign_table ft ON ft.ftrelid=c.oid
 LEFT JOIN pg_catalog.pg_foreign_server fs ON fs.oid=ft.ftserver
 WHERE n.nspname=$1 AND c.relname=$2`

func cloudberryOptions(raw string) ([]string, error) {
	var options []string
	if len(raw) > 256<<10 || !utf8.ValidString(raw) || json.Unmarshal([]byte(raw), &options) != nil || options == nil || len(options) > 1000 {
		return nil, errors.New("invalid or oversized Cloudberry storage options")
	}
	for _, option := range options {
		name, _, ok := strings.Cut(option, "=")
		if !ok || name == "" || strings.ContainsRune(option, 0) {
			return nil, errors.New("incomplete Cloudberry storage option")
		}
	}
	return options, nil
}

// Metadata, distribution and native DDL share the original read transaction.
// No generic CREATE TABLE reconstruction may discard distributed semantics.
func (s *Session) cloudberryTableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	var out *contract.TableStructure
	err := s.dataQuery(ctx, o, nil, func(ctx context.Context, q queryer) error {
		if _, err := q.ExecContext(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
			return err
		}
		t, err := s.structure(ctx, q, o)
		if err != nil {
			return err
		}
		out = structureModel(t)
		if err = s.postgresPresentationMetadata(ctx, q, out); err != nil {
			return err
		}
		var kind, rawOptions, rawExternal string
		if err = queryOne(ctx, q, cloudberryModifiersSQL, []any{o.Schema, o.Name}, &kind, &out.Engine, &rawOptions, &out.ExternalServer, &rawExternal); err != nil {
			return err
		}
		out.StorageOptions, err = cloudberryOptions(rawOptions)
		if err != nil {
			return err
		}
		out.ExternalOptions, err = cloudberryOptions(rawExternal)
		if err != nil {
			return err
		}
		switch kind {
		case "r", "p":
			policy, err := s.readDistributedPolicy(ctx, q, o)
			if err != nil {
				return err
			}
			if policy == nil {
				return errors.New("Cloudberry table distribution is unavailable")
			}
			out.Distribution, err = policy.Clause()
			if err != nil {
				return err
			}
		case "f":
			out.Kind, out.Editable = "外部表", false
			if out.ExternalServer == "" {
				return errors.New("Cloudberry external table server is unavailable")
			}
		case "v", "m":
			return nil // postgresStructure already retains the native view definition.
		default:
			return contract.ErrUnsupported
		}
		out.Definition, err = s.cloudberryDefinition(ctx, q, o, out)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err = s.check(o); err != nil {
		return nil, err
	}
	return out, nil
}
