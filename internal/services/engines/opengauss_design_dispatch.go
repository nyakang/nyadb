package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// Native pg_am retains the individual callbacks (and an optional handler).
// PostgreSQL's amtype/handler-only model cannot validate this dispatch table.
func openGaussCheckDesignDispatch(ctx context.Context, c *sql.Conn, table schemaddl.Table, schema string, types map[uint64]bool) error {
	var ids []string
	for oid := range types {
		ids = append(ids, strconv.FormatUint(oid, 10))
	}
	typeArray := "{" + strings.Join(ids, ",") + "}"
	checkedMethods := map[string]uint64{}
	checkClasses := func(array string) error { return openGaussCheckDesignOperatorClasses(ctx, c, array) }
	checkMethod := func(name string) (uint64, error) {
		if oid := checkedMethods[name]; oid != 0 {
			return oid, nil
		}
		if schemaddl.PostgreSQLIdentifier(name) != nil {
			return 0, errOpenGaussDesignDeclaration
		}
		var oid uint64
		var definition, raw string
		err := c.QueryRowContext(ctx, openGaussAccessMethodCatalogSQL, name).Scan(&oid, &definition, &raw)
		if err != nil || oid == 0 || oid >= 16384 {
			return 0, errors.Join(errOpenGaussDesignDeclaration, err)
		}
		var callbacks []uint64
		if json.Unmarshal([]byte(raw), &callbacks) != nil || len(callbacks) != 17 {
			return 0, errOpenGaussDesignDeclaration
		}
		if err = openGaussCheckNativeAccessMethod(ctx, c, name, oid, definition, callbacks); err != nil {
			return 0, err
		}
		// Automatic polymorphic classes may be selected through a concrete
		// array's elements, including when no explicit index is requested.
		var classes string
		err = c.QueryRowContext(ctx, `SELECT COALESCE(array_agg(o.oid)::text,'{}') FROM pg_opclass o
 WHERE o.opcmethod=$1::oid AND o.opcdefault AND (o.opcintype=ANY($2::oid[]) OR o.opcintype IN(
 SELECT t.oid FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace
 WHERE n.nspname='pg_catalog' AND t.typname IN('anyarray','anyenum','record','anyrange','anyset')))`, oid, typeArray).Scan(&classes)
		if err != nil {
			return 0, err
		}
		if err = checkClasses(classes); err != nil {
			return 0, fmt.Errorf("native %s default operator classes: %w", name, err)
		}
		checkedMethods[name] = oid
		return oid, nil
	}
	var defaultUstore bool
	if err := c.QueryRowContext(ctx, "SELECT current_setting('enable_ustore')::boolean AND current_setting('enable_default_ustore_table')::boolean").Scan(&defaultUstore); err != nil {
		return err
	}
	implicit, err := schemaddl.OpenGaussDesignImplicitIndexMethods(table, defaultUstore)
	if err != nil {
		return errors.Join(errOpenGaussDesignDeclaration, err)
	}
	// Heap btree/hash can also be selected by grouping/value proof plans.
	for _, method := range append([]string{"btree", "hash"}, implicit...) {
		if _, err := checkMethod(method); err != nil {
			return err
		}
	}
	for _, definition := range table.Definitions {
		if definition.Kind != "index" {
			continue
		}
		layout, err := schemaddl.OpenGaussIndexProbeLayout(definition, schema, table, defaultUstore)
		if err != nil {
			return errors.Join(errOpenGaussDesignDeclaration, err)
		}
		method, err := checkMethod(layout.Method)
		if err != nil {
			return err
		}
		for _, key := range layout.Keys {
			if key.Class.Name == "" {
				continue
			}
			if key.Class.Schema != "" && key.Class.Schema != "pg_catalog" {
				return errOpenGaussDesignDeclaration
			}
			var oid uint64
			err = c.QueryRowContext(ctx, `SELECT o.oid::bigint FROM pg_opclass o JOIN pg_namespace n ON n.oid=o.opcnamespace
 WHERE n.nspname='pg_catalog' AND o.opcname=$1 AND o.opcmethod=$2::oid`, key.Class.Name, method).Scan(&oid)
			if err != nil {
				return errors.Join(errOpenGaussDesignDeclaration, err)
			}
			if err = checkClasses("{" + strconv.FormatUint(oid, 10) + "}"); err != nil {
				return err
			}
		}
	}
	collations, err := schemaddl.OpenGaussDesignCollations(table, schema)
	if err != nil {
		return err
	}
	for _, collation := range collations {
		if collation.Schema != "" && collation.Schema != "pg_catalog" {
			return errOpenGaussDesignDeclaration
		}
		var trusted bool
		err = c.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_collation co JOIN pg_namespace n ON n.oid=co.collnamespace
 WHERE n.nspname='pg_catalog' AND co.collname=$1 AND co.oid<16384 AND co.collencoding IN(-1,pg_char_to_encoding(getdatabaseencoding())))`, collation.Name).Scan(&trusted)
		if err != nil || !trusted {
			return errors.Join(errOpenGaussDesignDeclaration, err)
		}
	}
	return ctx.Err()
}

func openGaussCheckDesignOperatorClasses(ctx context.Context, c *sql.Conn, array string) error {
	var unsafe bool
	err := c.QueryRowContext(ctx, `WITH RECURSIVE classes AS(SELECT o.oid,o.* FROM pg_opclass o WHERE o.oid=ANY($1::oid[])),
 families(oid,opfnamespace) AS(SELECT f.oid,f.opfnamespace FROM pg_opfamily f JOIN classes c ON c.opcfamily=f.oid
 UNION SELECT sf.oid,sf.opfnamespace FROM pg_amop a JOIN families f ON f.oid=a.amopfamily JOIN pg_opfamily sf ON sf.oid=a.amopsortfamily),
 routines AS(SELECT p.amproc::oid AS oid FROM pg_amproc p JOIN families f ON f.oid=p.amprocfamily
 UNION SELECT o.oprcode::oid FROM pg_amop a JOIN families f ON f.oid=a.amopfamily JOIN pg_operator o ON o.oid=a.amopopr)
 SELECT EXISTS(SELECT 1 FROM unnest($1::oid[]) r(oid) LEFT JOIN pg_opclass o ON o.oid=r.oid WHERE o.oid IS NULL)
 OR EXISTS(SELECT 1 FROM classes o LEFT JOIN pg_namespace n ON n.oid=o.opcnamespace LEFT JOIN pg_opfamily f ON f.oid=o.opcfamily
 WHERE o.oid>=16384 OR n.oid IS NULL OR n.nspname<>'pg_catalog' OR f.oid IS NULL OR o.opcintype>=16384 OR o.opckeytype>=16384)
 OR EXISTS(SELECT 1 FROM families f LEFT JOIN pg_namespace n ON n.oid=f.opfnamespace WHERE f.oid>=16384 OR n.oid IS NULL OR n.nspname<>'pg_catalog')
 OR EXISTS(SELECT 1 FROM pg_amproc a JOIN families f ON f.oid=a.amprocfamily WHERE a.oid>=16384 OR a.amproclefttype>=16384 OR a.amprocrighttype>=16384)
 OR EXISTS(SELECT 1 FROM pg_amop a JOIN families f ON f.oid=a.amopfamily LEFT JOIN pg_operator o ON o.oid=a.amopopr
 LEFT JOIN pg_opfamily sf ON sf.oid=a.amopsortfamily
 LEFT JOIN pg_namespace n ON n.oid=o.oprnamespace WHERE a.oid>=16384 OR a.amoplefttype>=16384 OR a.amoprighttype>=16384
 OR a.amopsortfamily<>0 AND (sf.oid IS NULL OR sf.oid>=16384)
 OR o.oid IS NULL OR o.oid>=16384 OR n.nspname<>'pg_catalog' OR o.oprleft>=16384 OR o.oprright>=16384 OR o.oprresult>=16384)
 OR EXISTS(SELECT 1 FROM routines r LEFT JOIN pg_proc p ON p.oid=r.oid LEFT JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE p.oid IS NULL OR p.oid>=16384 OR n.nspname<>'pg_catalog' OR p.prolang NOT IN(12,13) OR p.provolatile NOT IN('i','s')
 OR p.proisagg OR p.proiswindow OR p.proretset OR p.prosecdef OR p.proconfig IS NOT NULL)`, array).Scan(&unsafe)
	if err != nil || unsafe {
		return errors.Join(errOpenGaussDesignDeclaration, err)
	}
	return nil
}
