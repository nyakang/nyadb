package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

var errOpenGaussDesignDeclaration = errors.New("native table design requires verified core type, cast and expression implementations")

// Grammar inspection precedes server parsing: immutable user functions can
// otherwise run during CREATE's constant folding, even on an empty table.
// This validator uses native catalogs, without PostgreSQL prokind, access
// method, multirange or type-subscripting assumptions.
func openGaussCheckDesignDeclaration(ctx context.Context, c *sql.Conn, desired schemaddl.Table, schema string) error {
	var isolated bool
	if err := c.QueryRowContext(ctx, "SELECT current_setting('search_path')='pg_catalog' AND current_setting('standard_conforming_strings')='on'").Scan(&isolated); err != nil || !isolated {
		return errors.Join(errOpenGaussDesignDeclaration, err)
	}
	scope := desired.Clone()
	var definitions []schemaddl.Definition
	for _, d := range scope.Definitions {
		if d.Kind == "rule" {
			continue
		} // Rules stay outside expression probes.
		definitions = append(definitions, d)
	}
	scope.Definitions = definitions
	for i, col := range scope.Clauses {
		if col.Kind != "column" {
			continue
		}
		f, err := schemaddl.ParseOpenGaussField(col.SQL)
		if err != nil {
			return err
		}
		switch strings.ToLower(f.Type) {
		case "bigserial", "serial8":
			f.Type = "bigint"
		case "serial", "serial4":
			f.Type = "integer"
		case "smallserial", "serial2":
			f.Type = "smallint"
		}
		scope.Clauses[i].SQL, err = f.SQL()
		if err != nil {
			return err
		}
	}
	types, err := schemaddl.PostgreSQLProbeTypes(scope, schema)
	if err != nil {
		return errors.Join(errOpenGaussDesignDeclaration, err)
	}
	checkedTypes := map[uint64]bool{}
	checkedRoutines := map[uint64]bool{}
	var checkType func(uint64) error
	checkRoutine := func(oid uint64) error {
		if oid == 0 || checkedRoutines[oid] {
			return nil
		}
		if oid >= 16384 {
			return errOpenGaussDesignDeclaration
		}
		var trusted bool
		err := c.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE p.oid=$1::oid AND n.nspname='pg_catalog' AND p.prolang IN(12,13) AND p.provolatile IN('i','s') AND NOT p.proisagg AND NOT p.proiswindow
 AND NOT p.proretset AND NOT p.prosecdef AND p.proconfig IS NULL)`, oid).Scan(&trusted)
		if err != nil || !trusted {
			return errors.Join(errOpenGaussDesignDeclaration, err)
		}
		checkedRoutines[oid] = true
		return nil
	}
	checkType = func(oid uint64) error {
		if checkedTypes[oid] {
			return nil
		}
		if oid == 0 || oid >= 16384 || len(checkedTypes) >= 4096 {
			return errOpenGaussDesignDeclaration
		}
		var namespace, kind string
		var element uint64
		var routines [7]uint64
		err := c.QueryRowContext(ctx, `SELECT n.nspname,t.typtype::text,t.typelem::bigint,t.typinput::bigint,t.typoutput::bigint,
 t.typreceive::bigint,t.typsend::bigint,t.typmodin::bigint,t.typmodout::bigint,t.typanalyze::bigint
 FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE t.oid=$1::oid`, oid).Scan(&namespace, &kind, &element, &routines[0], &routines[1], &routines[2], &routines[3], &routines[4], &routines[5], &routines[6])
		if err != nil || namespace != "pg_catalog" || kind != "b" || routines[0] == 0 || routines[1] == 0 {
			return errors.Join(errOpenGaussDesignDeclaration, err)
		}
		checkedTypes[oid] = true
		for _, r := range routines {
			if err = checkRoutine(r); err != nil {
				return err
			}
		}
		if element != 0 {
			return checkType(element)
		}
		return nil
	}
	for _, ref := range types {
		namespace := ref.Schema
		if namespace == "" {
			namespace = "pg_catalog"
		}
		if namespace != "pg_catalog" {
			return errOpenGaussDesignDeclaration
		}
		var oid, array uint64
		err = c.QueryRowContext(ctx, "SELECT t.oid::bigint,t.typarray::bigint FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname=$1 AND t.typname=$2", namespace, ref.Name).Scan(&oid, &array)
		if err != nil {
			return errors.Join(errOpenGaussDesignDeclaration, err)
		}
		if err = checkType(oid); err != nil {
			return err
		}
		if ref.Array {
			if err = checkType(array); err != nil {
				return err
			}
		}
	}
	calls, err := schemaddl.PostgreSQLDeclarationProbeCalls(scope, schema)
	if err != nil {
		return errors.Join(errOpenGaussDesignDeclaration, err)
	}
	for _, call := range calls {
		if call.Schema != "" && call.Schema != "pg_catalog" {
			return errOpenGaussDesignDeclaration
		}
		if !postgresProbeRoutinePolicy(call.Name, call.Scope) {
			return errOpenGaussDesignDeclaration
		}
		rows, err := c.QueryContext(ctx, `SELECT p.oid::bigint,p.prolang::bigint,p.provolatile::text,p.proisagg,p.proiswindow,p.proargtypes::text,p.prorettype::bigint,
 p.proretset OR p.prosecdef OR p.proconfig IS NOT NULL
 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='pg_catalog' AND p.proname=$1`, call.Name)
		if err != nil {
			return err
		}
		type routine struct {
			oid, language, result uint64
			volatility, args      string
			aggregate, window     bool
			unsafe                bool
		}
		var routines []routine
		for rows.Next() {
			var r routine
			if err = rows.Scan(&r.oid, &r.language, &r.volatility, &r.aggregate, &r.window, &r.args, &r.result, &r.unsafe); err != nil {
				break
			}
			if len(routines) >= 128 {
				err = errOpenGaussDesignDeclaration
				break
			}
			routines = append(routines, r)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		if len(routines) == 0 {
			return errOpenGaussDesignDeclaration
		}
		for _, r := range routines {
			if r.oid == 0 || r.oid >= 16384 || r.language != 12 && r.language != 13 || r.aggregate || r.window || r.unsafe || r.volatility != "i" && call.Scope != "default" {
				return fmt.Errorf("native routine %s has unsupported implementation: %w", call.Name, errOpenGaussDesignDeclaration)
			}
			// Polymorphic core signatures are resolved by the trusted builtin.
			// Concrete input/result identities still join the cast closure.
			for _, part := range append(strings.Fields(r.args), strconv.FormatUint(r.result, 10)) {
				oid, e := strconv.ParseUint(part, 10, 32)
				if e != nil {
					return e
				}
				var pseudo bool
				if err = c.QueryRowContext(ctx, "SELECT typtype='p' FROM pg_type WHERE oid=$1::oid", oid).Scan(&pseudo); err != nil {
					return err
				}
				if !pseudo {
					if err = checkType(oid); err != nil {
						return err
					}
				}
			}
		}
	}
	operators, err := schemaddl.PostgreSQLProbeOperators(scope, schema)
	if err != nil {
		return errors.Join(errOpenGaussDesignDeclaration, err)
	}
	castTypes := map[uint64]bool{}
	for oid := range checkedTypes {
		castTypes[oid] = true
	}
	if err = openGaussCheckDesignOperators(ctx, c, operators, checkType); err != nil {
		return err
	}
	if err = openGaussCheckDesignCastClosure(ctx, c, checkedTypes, castTypes, checkType, checkRoutine); err != nil {
		return fmt.Errorf("native cast closure: %w", err)
	}
	if err = openGaussCheckDesignDispatch(ctx, c, scope, schema, checkedTypes); err != nil {
		return fmt.Errorf("native expression dispatch: %w", err)
	}
	return ctx.Err()
}
