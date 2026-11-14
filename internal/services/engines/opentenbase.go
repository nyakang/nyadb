package engines

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// OpenTenBase has a PG10-derived wire/catalog model and native distributed
// storage. Its own table renderer retains distribution, placement and partitions.
type openTenBaseDialect struct{ postgresDialect }

func validateOpenTenBaseServer(ctx context.Context, q queryer) error {
	var version string
	var number int
	var catalog sql.NullString
	var tableDefinition bool
	err := queryOne(ctx, q, `SELECT version(),current_setting('server_version_num')::int,
 to_regclass('pg_catalog.pgxc_class')::text,
 to_regprocedure('pg_catalog.pg_get_tabledef(regclass)') IS NOT NULL`, nil, &version, &number, &catalog, &tableDefinition)
	if err != nil {
		return err
	}
	if !strings.Contains(version, "OpenTenBase") || number < 100000 || !catalog.Valid || !tableDefinition {
		return errors.New("OpenTenBase requires its native distribution catalog and table definition function")
	}
	return nil
}

func (s *Session) openTenBaseTableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	var out *contract.TableStructure
	err := s.dataQuery(ctx, o, nil, func(ctx context.Context, q queryer) error {
		if _, err := q.ExecContext(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on; SET LOCAL quote_all_identifiers=on"); err != nil {
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
		var kind, store, rawOptions, rawExternal string
		err = queryOne(ctx, q, `SELECT c.relkind::text,c.relstore::text,
 COALESCE(array_to_json(c.reloptions)::text,'[]'),COALESCE(fs.srvname,''),
 COALESCE(array_to_json(ft.ftoptions)::text,'[]')
 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 LEFT JOIN pg_catalog.pg_foreign_table ft ON ft.ftrelid=c.oid
 LEFT JOIN pg_catalog.pg_foreign_server fs ON fs.oid=ft.ftserver
 WHERE n.nspname=$1 AND c.relname=$2`, []any{o.Schema, o.Name}, &kind, &store, &rawOptions, &out.ExternalServer, &rawExternal)
		if err != nil {
			return err
		}
		out.Engine = "OpenTenBase (" + store + ")"
		if store == "r" {
			out.Engine = "OpenTenBase (row)"
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
				return errors.New("OpenTenBase table distribution is unavailable")
			}
			out.Distribution, err = policy.Clause()
			if err != nil {
				return err
			}
		case "f":
			out.Kind, out.Editable = "外部表", false
			if out.ExternalServer == "" {
				return errors.New("OpenTenBase external table server is unavailable")
			}
		case "v", "m":
			return nil
		default:
			return contract.ErrUnsupported
		}
		var definition sql.NullString
		if err := queryOne(ctx, q, `SELECT pg_catalog.pg_get_tabledef(c.oid)
 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname=$1 AND c.relname=$2`, []any{o.Schema, o.Name}, &definition); err != nil {
			return err
		}
		if !definition.Valid || strings.TrimSpace(definition.String) == "" || len(definition.String) > 1<<20 || !utf8.ValidString(definition.String) || strings.ContainsRune(definition.String, 0) {
			return errors.New("complete OpenTenBase definition unavailable or exceeds 1 MiB")
		}
		out.Definition, err = s.openTenBaseDefinitionExtras(ctx, q, o, definition.String)
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
