package engines

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

var errPostgresProbeType = errors.New("PostgreSQL definition probe requires supported type and cast callbacks")

// All state is request-local. A previous successful read never authorizes a
// changed definition in another transaction, profile or session generation.
type postgresProbeTypeCheck struct {
	ctx       context.Context
	c         *sql.Conn
	types     map[int64]bool
	routines  map[int64]string
	operators map[int64]bool
	opclasses map[int64]bool
}

func postgresCheckProbeTypes(ctx context.Context, c *sql.Conn, desired schemaddl.Table, schema string) error {
	refs, err := schemaddl.PostgreSQLProbeTypes(desired, schema)
	if err != nil {
		return err
	}
	check := postgresProbeTypeCheck{ctx: ctx, c: c, types: map[int64]bool{}, routines: map[int64]string{}, operators: map[int64]bool{}, opclasses: map[int64]bool{}}
	for _, ref := range refs {
		namespace := ref.Schema
		if namespace == "" {
			namespace = "pg_catalog"
		} // Matches the CREATE probe's search_path.
		var oid, array int64
		// Do not use to_regtype, regtype casts, PREPARE or EXPLAIN with user
		// declarations: resolving typmods or literals can already run callbacks.
		err = c.QueryRowContext(ctx, `SELECT t.oid::bigint,t.typarray::bigint FROM pg_catalog.pg_type t
JOIN pg_catalog.pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname=$1 AND t.typname=$2`, namespace, ref.Name).Scan(&oid, &array)
		if err != nil {
			return errors.Join(errPostgresProbeType, err)
		}
		if err = check.typeID(oid, 0); err != nil {
			return err
		}
		if ref.Array {
			if array == 0 {
				return errPostgresProbeType
			}
			if err = check.typeID(array, 0); err != nil {
				return err
			}
		}
	}
	// Check explicit/implicit cast candidates between the actual encountered
	// type identities. An unrelated extension cast touching text does not make
	// every text declaration unavailable. No cast implementation is invoked.
	ids := make([]int64, 0, len(check.types))
	for oid := range check.types {
		ids = append(ids, oid)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, len(ids))
	for i, oid := range ids {
		parts[i] = strconv.FormatInt(oid, 10)
	}
	array := "{" + strings.Join(parts, ",") + "}"
	rows, err := c.QueryContext(ctx, `SELECT castfunc::bigint,castmethod::text FROM pg_catalog.pg_cast
WHERE castsource=ANY($1::oid[]) AND casttarget=ANY($1::oid[])`, array)
	if err != nil {
		return err
	}
	type cast struct {
		routine int64
		method  string
	}
	var casts []cast
	for rows.Next() {
		var value cast
		if err = rows.Scan(&value.routine, &value.method); err != nil {
			rows.Close()
			return err
		}
		if len(casts) >= 4096 {
			rows.Close()
			return errPostgresProbeType
		}
		casts = append(casts, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, value := range casts {
		switch value.method {
		case "f":
			if value.routine == 0 {
				return errPostgresProbeType
			}
			if _, err = check.routineID(value.routine); err != nil {
				return err
			}
		case "b", "i":
			if value.routine != 0 {
				return errPostgresProbeType
			}
		default:
			return errPostgresProbeType
		}
	}
	return ctx.Err()
}

func (p *postgresProbeTypeCheck) routineID(oid int64) (string, error) {
	if oid == 0 {
		return "", nil
	}
	if name, ok := p.routines[oid]; ok {
		return name, nil
	}
	if oid <= 0 || oid >= 16384 || len(p.routines) >= 4096 {
		return "", errPostgresProbeType
	}
	var name, namespace, kind, volatility, language string
	err := p.c.QueryRowContext(p.ctx, `SELECT p.proname::text,n.nspname::text,p.prokind::text,p.provolatile::text,l.lanname::text
FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
JOIN pg_catalog.pg_language l ON l.oid=p.prolang WHERE p.oid=$1`, oid).Scan(&name, &namespace, &kind, &volatility, &language)
	if err != nil {
		return "", errors.Join(errPostgresProbeType, err)
	}
	// Core I/O includes stable timezone-sensitive routines. IMMUTABLE user
	// code never gains trust; volatile implementations are not type callbacks.
	if namespace != "pg_catalog" || kind != "f" || volatility != "i" && volatility != "s" || language != "internal" && language != "c" {
		return "", errPostgresProbeType
	}
	p.routines[oid] = name
	return name, nil
}

func (p *postgresProbeTypeCheck) typeID(oid int64, depth int) error {
	if p.types[oid] {
		return nil
	}
	if oid <= 0 || depth >= 64 || len(p.types) >= 4096 {
		return errPostgresProbeType
	}
	var namespace, kind, category string
	var element, base, relation int64
	var callbacks [8]int64
	err := p.c.QueryRowContext(p.ctx, `SELECT n.nspname::text,t.typtype::text,t.typcategory::text,
t.typelem::bigint,t.typbasetype::bigint,t.typrelid::bigint,t.typinput::bigint,t.typoutput::bigint,
t.typreceive::bigint,t.typsend::bigint,t.typmodin::bigint,t.typmodout::bigint,t.typanalyze::bigint,t.typsubscript::bigint
FROM pg_catalog.pg_type t JOIN pg_catalog.pg_namespace n ON n.oid=t.typnamespace WHERE t.oid=$1`, oid).Scan(&namespace, &kind, &category, &element, &base, &relation, &callbacks[0], &callbacks[1], &callbacks[2], &callbacks[3], &callbacks[4], &callbacks[5], &callbacks[6], &callbacks[7])
	if err != nil {
		return errors.Join(errPostgresProbeType, err)
	}
	if callbacks[0] == 0 || callbacks[1] == 0 {
		return errPostgresProbeType
	}
	names := make([]string, len(callbacks))
	for i, routine := range callbacks {
		if names[i], err = p.routineID(routine); err != nil {
			return err
		}
	}
	core := oid < 16384 && namespace == "pg_catalog"
	if !core {
		// Structural user types can use PostgreSQL's known I/O machinery.
		// Arbitrary user-defined base representations remain unsupported.
		switch {
		case kind == "e":
			if names[0] != "enum_in" || names[1] != "enum_out" {
				return errPostgresProbeType
			}
		case kind == "d":
			if names[0] != "domain_in" || base == 0 {
				return errPostgresProbeType
			}
		case kind == "c":
			if names[0] != "record_in" || names[1] != "record_out" || relation == 0 {
				return errPostgresProbeType
			}
		case kind == "r":
			if names[0] != "range_in" || names[1] != "range_out" {
				return errPostgresProbeType
			}
		case kind == "m":
			if names[0] != "multirange_in" || names[1] != "multirange_out" {
				return errPostgresProbeType
			}
		case kind == "b" && category == "A" && element != 0:
			if names[0] != "array_in" || names[1] != "array_out" {
				return errPostgresProbeType
			}
		default:
			return errPostgresProbeType
		}
	}
	p.types[oid] = true // Structural composite/array references may form cycles.
	for _, nested := range []int64{element, base} {
		if nested != 0 {
			if err = p.typeID(nested, depth+1); err != nil {
				return err
			}
		}
	}
	if kind == "c" {
		rows, err := p.c.QueryContext(p.ctx, `SELECT atttypid::bigint FROM pg_catalog.pg_attribute WHERE attrelid=$1 AND attnum>0 AND NOT attisdropped`, relation)
		if err != nil {
			return err
		}
		var nested []int64
		for rows.Next() {
			var value int64
			if err = rows.Scan(&value); err != nil {
				rows.Close()
				return err
			}
			if len(nested) >= 1600 {
				rows.Close()
				return errPostgresProbeType
			}
			nested = append(nested, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, value := range nested {
			if err = p.typeID(value, depth+1); err != nil {
				return err
			}
		}
	}
	if kind == "d" {
		// Dependency reads inspect CHECK and inherited DEFAULT implementations
		// without evaluating VALUE or rendering user expressions into a query.
		rows, err := p.c.QueryContext(p.ctx, `SELECT d.refclassid::bigint,d.refobjid::bigint,
d.refclassid='pg_catalog.pg_proc'::regclass,d.refclassid='pg_catalog.pg_operator'::regclass,d.refclassid='pg_catalog.pg_type'::regclass
FROM pg_catalog.pg_depend d WHERE (d.classid='pg_catalog.pg_type'::regclass AND d.objid=$1)
OR (d.classid='pg_catalog.pg_constraint'::regclass AND d.objid IN
(SELECT oid FROM pg_catalog.pg_constraint WHERE contypid=$1 AND contype='c'))`, oid)
		if err != nil {
			return err
		}
		type dependency struct {
			class, oid             int64
			routine, operator, typ bool
		}
		var dependencies []dependency
		for rows.Next() {
			var value dependency
			if err = rows.Scan(&value.class, &value.oid, &value.routine, &value.operator, &value.typ); err != nil {
				rows.Close()
				return err
			}
			if len(dependencies) >= 4096 {
				rows.Close()
				return errPostgresProbeType
			}
			dependencies = append(dependencies, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, value := range dependencies {
			if value.routine {
				_, err = p.routineID(value.oid)
			}
			if value.operator {
				err = p.operatorID(value.oid, depth+1)
			}
			if value.typ {
				err = p.typeID(value.oid, depth+1)
			}
			if err != nil {
				return err
			}
		}
	}
	if kind == "r" || kind == "m" {
		var subtype, canonical, difference, opclass int64
		err = p.c.QueryRowContext(p.ctx, `SELECT rngsubtype::bigint,rngcanonical::bigint,rngsubdiff::bigint,rngsubopc::bigint
FROM pg_catalog.pg_range WHERE rngtypid=$1 OR rngmultitypid=$1`, oid).Scan(&subtype, &canonical, &difference, &opclass)
		if err != nil {
			return errors.Join(errPostgresProbeType, err)
		}
		if err = p.typeID(subtype, depth+1); err != nil {
			return err
		}
		for _, routine := range []int64{canonical, difference} {
			if _, err = p.routineID(routine); err != nil {
				return err
			}
		}
		if err = p.opclassID(opclass, depth+1); err != nil {
			return err
		}
	}
	return p.ctx.Err()
}

func (p *postgresProbeTypeCheck) operatorID(oid int64, depth int) error {
	if p.operators[oid] {
		return nil
	}
	if oid <= 0 || oid >= 16384 || len(p.operators) >= 4096 {
		return errPostgresProbeType
	}
	var namespace string
	var routine, left, right, result int64
	err := p.c.QueryRowContext(p.ctx, `SELECT n.nspname::text,o.oprcode::bigint,o.oprleft::bigint,o.oprright::bigint,o.oprresult::bigint
FROM pg_catalog.pg_operator o JOIN pg_catalog.pg_namespace n ON n.oid=o.oprnamespace WHERE o.oid=$1`, oid).Scan(&namespace, &routine, &left, &right, &result)
	if err != nil {
		return errors.Join(errPostgresProbeType, err)
	}
	if namespace != "pg_catalog" || routine == 0 {
		return errPostgresProbeType
	}
	if _, err = p.routineID(routine); err != nil {
		if err = p.operatorForwarder(routine, left, right, result); err != nil {
			return err
		}
	}
	p.operators[oid] = true
	for _, nested := range []int64{left, right} {
		if nested != 0 {
			if err = p.typeID(nested, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *postgresProbeTypeCheck) opclassID(oid int64, depth int) error {
	if p.opclasses[oid] {
		return nil
	}
	if oid <= 0 || oid >= 16384 || len(p.opclasses) >= 4096 {
		return errPostgresProbeType
	}
	var namespace string
	var family int64
	err := p.c.QueryRowContext(p.ctx, `SELECT n.nspname::text,c.opcfamily::bigint FROM pg_catalog.pg_opclass c JOIN pg_catalog.pg_namespace n ON n.oid=c.opcnamespace WHERE c.oid=$1`, oid).Scan(&namespace, &family)
	if err != nil {
		return errors.Join(errPostgresProbeType, err)
	}
	if namespace != "pg_catalog" {
		return errPostgresProbeType
	}
	p.opclasses[oid] = true
	rows, err := p.c.QueryContext(p.ctx, `SELECT amproc::bigint FROM pg_catalog.pg_amproc WHERE amprocfamily=$1`, family)
	if err != nil {
		return err
	}
	var routines []int64
	for rows.Next() {
		var value int64
		if err = rows.Scan(&value); err != nil {
			rows.Close()
			return err
		}
		if len(routines) >= 4096 {
			rows.Close()
			return errPostgresProbeType
		}
		routines = append(routines, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, value := range routines {
		if _, err = p.routineID(value); err != nil {
			return err
		}
	}
	rows, err = p.c.QueryContext(p.ctx, `SELECT amopopr::bigint FROM pg_catalog.pg_amop WHERE amopfamily=$1`, family)
	if err != nil {
		return err
	}
	var operators []int64
	for rows.Next() {
		var value int64
		if err = rows.Scan(&value); err != nil {
			rows.Close()
			return err
		}
		if len(operators) >= 4096 {
			rows.Close()
			return errPostgresProbeType
		}
		operators = append(operators, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, value := range operators {
		if err = p.operatorID(value, depth+1); err != nil {
			return err
		}
	}
	return nil
}
