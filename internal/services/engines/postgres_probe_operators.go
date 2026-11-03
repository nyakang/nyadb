package engines

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

var errPostgresProbeDispatch = errors.New("PostgreSQL definition probe requires supported core operators and access methods")

func (p *postgresProbeTypeCheck) operatorForwarder(oid, left, right, result int64) error {
	if oid <= 0 || oid >= 16384 {
		return errPostgresProbeDispatch
	}
	var namespace, language, kind, volatility, source, body string
	err := p.c.QueryRowContext(p.ctx, `SELECT n.nspname::text,l.lanname::text,p.prokind::text,p.provolatile::text,p.prosrc,COALESCE(p.prosqlbody::text,'')
FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace JOIN pg_catalog.pg_language l ON l.oid=p.prolang WHERE p.oid=$1`, oid).Scan(&namespace, &language, &kind, &volatility, &source, &body)
	if err != nil {
		return errors.Join(errPostgresProbeDispatch, err)
	}
	if namespace != "pg_catalog" || language != "sql" || kind != "f" || volatility != "i" && volatility != "s" || source != "" {
		return errPostgresProbeDispatch
	}
	operator, routine, err := schemaddl.PostgreSQLProbeOperatorForwarder(body, left, right, result)
	if err != nil || operator >= 16384 || routine >= 16384 || routine == oid {
		return errPostgresProbeDispatch
	}
	var matched bool
	err = p.c.QueryRowContext(p.ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_operator o JOIN pg_catalog.pg_namespace n ON n.oid=o.oprnamespace
WHERE o.oid=$1 AND o.oprcode=$2 AND o.oprleft=$3 AND o.oprright=$4 AND o.oprresult=$5 AND n.nspname='pg_catalog')`, operator, routine, right, left, result).Scan(&matched)
	if err != nil || !matched {
		return errors.Join(errPostgresProbeDispatch, err)
	}
	// The terminal implementation must be an ordinary trusted core C/internal
	// routine. Do not recursively authorize SQL forwarding cycles or bodies.
	_, err = p.routineID(routine)
	return err
}

// This reads dispatch metadata BEFORE the independent CREATE. User code cannot
// gain permission by declaring an operator, opclass or handler IMMUTABLE. The
// result is request-local; it does not establish concurrent catalog stability.
func postgresCheckProbeOperators(ctx context.Context, c *sql.Conn, desired schemaddl.Table, schema string, extraTypes ...int64) error {
	refs, err := schemaddl.PostgreSQLProbeOperators(desired, schema)
	if err != nil {
		return err
	}
	p := postgresProbeTypeCheck{ctx: ctx, c: c, types: map[int64]bool{}, routines: map[int64]string{}, operators: map[int64]bool{}, opclasses: map[int64]bool{}}
	for _, ref := range refs {
		namespace := ref.Schema
		if namespace == "" {
			namespace = "pg_catalog"
		}
		if namespace != "pg_catalog" {
			return errPostgresProbeDispatch
		}
		rows, err := c.QueryContext(ctx, `SELECT o.oid::bigint FROM pg_catalog.pg_operator o JOIN pg_catalog.pg_namespace n ON n.oid=o.oprnamespace WHERE n.nspname=$1 AND o.oprname=$2`, namespace, ref.Name)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var oid int64
			if err = rows.Scan(&oid); err != nil {
				rows.Close()
				return err
			}
			if len(ids) >= 512 {
				rows.Close()
				return errPostgresProbeDispatch
			}
			ids = append(ids, oid)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return errPostgresProbeDispatch
		}
		for _, oid := range ids {
			if err = p.operatorID(oid, 0); err != nil {
				return errors.Join(errPostgresProbeDispatch, err)
			}
		}
	}
	tableMethod := ""
	if desired.Postgres != nil {
		tableMethod = desired.Postgres.AccessMethod
	}
	if tableMethod == "" {
		if err = c.QueryRowContext(ctx, "SHOW default_table_access_method").Scan(&tableMethod); err != nil {
			return err
		}
	}
	if _, err = postgresProbeAccessMethod(ctx, c, tableMethod, "t"); err != nil {
		return err
	}
	// Include actual declared/cast/literal types and their structural closure
	// when checking automatic default-class selection. Search_path does not
	// prevent a default class in another namespace from being selected by type.
	types, err := schemaddl.PostgreSQLProbeTypes(desired, schema)
	if err != nil {
		return err
	}
	for _, ref := range types {
		namespace := ref.Schema
		if namespace == "" {
			namespace = "pg_catalog"
		}
		var oid, array int64
		err = c.QueryRowContext(ctx, `SELECT t.oid::bigint,t.typarray::bigint FROM pg_catalog.pg_type t JOIN pg_catalog.pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname=$1 AND t.typname=$2`, namespace, ref.Name).Scan(&oid, &array)
		if err != nil {
			return errors.Join(errPostgresProbeDispatch, err)
		}
		if err = p.typeID(oid, 0); err != nil {
			return err
		}
		if ref.Array {
			if array == 0 {
				return errPostgresProbeDispatch
			}
			if err = p.typeID(array, 0); err != nil {
				return err
			}
		}
	}
	for _, oid := range extraTypes {
		if err := p.typeID(oid, 0); err != nil {
			return err
		}
	}
	var typeIDs []string
	for oid := range p.types {
		typeIDs = append(typeIDs, strconv.FormatInt(oid, 10))
	}
	array := "{" + strings.Join(typeIDs, ",") + "}"
	checkedMethods := map[int64]bool{}
	checkDefaults := func(method int64) error {
		if checkedMethods[method] {
			return nil
		}
		if err := postgresProbeDefaultClasses(&p, method, array); err != nil {
			return err
		}
		checkedMethods[method] = true
		return nil
	}
	hasIndex := false
	for _, definition := range desired.Definitions {
		if definition.Kind != "index" {
			continue
		}
		hasIndex = true
		layout, err := schemaddl.PostgreSQLIndexProbeLayout(definition, schema, desired.Name)
		if err != nil {
			return err
		}
		method, err := postgresProbeAccessMethod(ctx, c, layout.Method, "i")
		if err != nil {
			return err
		}
		for _, key := range layout.Keys {
			if key.Class.Name == "" {
				continue
			}
			namespace := key.Class.Schema
			if namespace == "" {
				namespace = "pg_catalog"
			}
			var oid int64
			err = c.QueryRowContext(ctx, `SELECT c.oid::bigint FROM pg_catalog.pg_opclass c JOIN pg_catalog.pg_namespace n ON n.oid=c.opcnamespace WHERE n.nspname=$1 AND c.opcname=$2 AND c.opcmethod=$3`, namespace, key.Class.Name, method).Scan(&oid)
			if err != nil {
				return errors.Join(errPostgresProbeDispatch, err)
			}
			if err = p.opclassID(oid, 0); err != nil {
				return errors.Join(errPostgresProbeDispatch, err)
			}
		}
		if err = checkDefaults(method); err != nil {
			return err
		}
	}
	// Core array/record/comparison implementations can dispatch through a
	// nested type's default comparison/hash class even without an index.
	if len(refs) > 0 || hasIndex {
		for _, name := range []string{"btree", "hash"} {
			method, err := postgresProbeAccessMethod(ctx, c, name, "i")
			if err != nil {
				return err
			}
			if err = checkDefaults(method); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func postgresProbeDefaultClasses(p *postgresProbeTypeCheck, method int64, array string) error {
	rows, err := p.c.QueryContext(p.ctx, `SELECT c.oid::bigint FROM pg_catalog.pg_opclass c WHERE c.opcmethod=$1 AND c.opcdefault AND
(c.opcintype=ANY($2::oid[]) OR c.opcintype IN (SELECT t.oid FROM pg_catalog.pg_type t JOIN pg_catalog.pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname='pg_catalog' AND t.typname IN ('anyarray','anyenum','record','anyrange','anymultirange')))`, method, array)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var oid int64
		if err = rows.Scan(&oid); err != nil {
			rows.Close()
			return err
		}
		if len(ids) >= 4096 {
			rows.Close()
			return errPostgresProbeDispatch
		}
		ids = append(ids, oid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, oid := range ids {
		if err = p.opclassID(oid, 0); err != nil {
			return errors.Join(errPostgresProbeDispatch, err)
		}
	}
	return p.ctx.Err()
}

func postgresProbeAccessMethod(ctx context.Context, c *sql.Conn, name, kind string) (int64, error) {
	handler := ""
	if kind == "t" && name == "heap" {
		handler = "heap_tableam_handler"
	}
	if kind == "i" {
		handler = map[string]string{"btree": "bthandler", "hash": "hashhandler", "gist": "gisthandler", "gin": "ginhandler", "spgist": "spghandler", "brin": "brinhandler"}[name]
	}
	if handler == "" {
		return 0, errPostgresProbeDispatch
	}
	var oid, routine, returns int64
	var methodKind, routineName, namespace, language, prokind, returnName, returnNamespace string
	err := c.QueryRowContext(ctx, `SELECT a.oid::bigint,a.amtype::text,p.oid::bigint,p.proname::text,n.nspname::text,l.lanname::text,p.prokind::text,t.oid::bigint,t.typname::text,tn.nspname::text
FROM pg_catalog.pg_am a JOIN pg_catalog.pg_proc p ON p.oid=a.amhandler
JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace JOIN pg_catalog.pg_language l ON l.oid=p.prolang
JOIN pg_catalog.pg_type t ON t.oid=p.prorettype JOIN pg_catalog.pg_namespace tn ON tn.oid=t.typnamespace WHERE a.amname=$1`, name).Scan(&oid, &methodKind, &routine, &routineName, &namespace, &language, &prokind, &returns, &returnName, &returnNamespace)
	if err != nil {
		return 0, errors.Join(errPostgresProbeDispatch, err)
	}
	returnType := "index_am_handler"
	if kind == "t" {
		returnType = "table_am_handler"
	}
	// A core factory can legitimately be volatile. Its exact core identity and
	// handler kind govern permission; a user-created internal alias is rejected.
	if oid <= 0 || oid >= 16384 || methodKind != kind || routine <= 0 || routine >= 16384 || routineName != handler || namespace != "pg_catalog" || prokind != "f" || language != "internal" && language != "c" || returns <= 0 || returns >= 16384 || returnName != returnType || returnNamespace != "pg_catalog" {
		return 0, errPostgresProbeDispatch
	}
	return oid, nil
}
