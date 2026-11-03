package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

var errPostgresProbeRoutine = errors.New("PostgreSQL definition probe requires supported core expression routines")
var errPostgresSequenceDefault = errors.New("PostgreSQL new sequence default cannot populate existing rows safely")

// Explicit declarations are checked before CREATE, which can evaluate constant
// immutable expressions. This remains a limited preflight, not a concurrent
// catalog stability proof or authorization to publish the general designer.
func postgresCheckDeclarationProbeRoutines(ctx context.Context, c *sql.Conn, desired schemaddl.Table, schema string) error {
	_, err := postgresDeclarationProbeRoutineTypes(ctx, c, desired, schema)
	return err
}

func postgresDeclarationProbeRoutineTypes(ctx context.Context, c *sql.Conn, desired schemaddl.Table, schema string) ([]int64, error) {
	calls, err := schemaddl.PostgreSQLDeclarationProbeCalls(desired, schema)
	if err != nil {
		return nil, err
	}
	p := postgresExpressionRoutineCheck{ctx: ctx, c: c, checked: map[string]bool{}, types: map[int64]bool{}}
	for _, call := range calls {
		if err := p.call(call.PostgreSQLProbeCall, call.Scope); err != nil {
			return nil, err
		}
	}
	types := make([]int64, 0, len(p.types))
	for oid := range p.types {
		types = append(types, oid)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	return types, ctx.Err()
}

type postgresExpressionRoutineCheck struct {
	ctx     context.Context
	c       *sql.Conn
	checked map[string]bool
	types   map[int64]bool
}

func postgresProbeRoutinePolicy(name, scope string) bool {
	common := "abs round trunc ceil ceiling floor sign sqrt power mod lower upper length char_length character_length octet_length bit_length substring substr left right btrim ltrim rtrim replace translate encode decode md5 json_build_object jsonb_build_object json_build_array jsonb_build_array json_typeof jsonb_typeof json_array_length jsonb_array_length to_json to_jsonb"
	if strings.Contains(" "+common+" ", " "+name+" ") {
		return true
	}
	if scope == "index" || scope == "generated" {
		return false
	}
	stable := "date_trunc date_part extract timezone now transaction_timestamp statement_timestamp"
	if strings.Contains(" "+stable+" ", " "+name+" ") {
		return true
	}
	return scope == "default" && (name == "nextval" || name == "gen_random_uuid" || name == "random" || name == "clock_timestamp")
}

func (p *postgresExpressionRoutineCheck) call(call schemaddl.PostgreSQLProbeCall, scope string) error {
	if call.Schema != "" && call.Schema != "pg_catalog" || !postgresProbeRoutinePolicy(call.Name, scope) {
		return errPostgresProbeRoutine
	}
	key := scope + ":" + call.Name
	if p.checked[key] {
		return nil
	}
	// Every overload is checked: a user overload sharing a trusted name must
	// not get executed merely because the builtin exists. No expression is
	// sent to the server to infer its selected overload.
	rows, err := p.c.QueryContext(p.ctx, `SELECT p.oid::bigint FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='pg_catalog' AND p.proname=$1`, call.Name)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var oid int64
		if err := rows.Scan(&oid); err != nil {
			rows.Close()
			return err
		}
		if len(ids) >= 64 {
			rows.Close()
			return errPostgresProbeRoutine
		}
		ids = append(ids, oid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return errPostgresProbeRoutine
	}
	for _, oid := range ids {
		if _, err := p.routine(oid, scope, true); err != nil {
			return fmt.Errorf("PostgreSQL %s routine %q OID %d: %w", scope, call.Name, oid, err)
		}
	}
	p.checked[key] = true
	return nil
}

type postgresExpressionRoutine struct {
	arguments []int64
	result    int64
}

func (p *postgresExpressionRoutineCheck) routine(oid int64, scope string, allowSQL bool) (postgresExpressionRoutine, error) {
	var out postgresExpressionRoutine
	if oid <= 0 || oid >= 16384 {
		return out, errPostgresProbeRoutine
	}
	var namespace, name, kind, volatility, language, source, body, arguments string
	var configured, returnsSet bool
	err := p.c.QueryRowContext(p.ctx, `SELECT n.nspname::text,p.proname::text,p.prokind::text,p.provolatile::text,l.lanname::text,p.prosrc,COALESCE(p.prosqlbody::text,''),p.proargtypes::text,p.prorettype::bigint,p.proconfig IS NOT NULL,p.proretset
FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace JOIN pg_catalog.pg_language l ON l.oid=p.prolang WHERE p.oid=$1`, oid).Scan(&namespace, &name, &kind, &volatility, &language, &source, &body, &arguments, &out.result, &configured, &returnsSet)
	if err != nil {
		return out, errors.Join(errPostgresProbeRoutine, err)
	}
	// The core length(text) overload is immutable, but the same name also has
	// length(bytea,name), which reads the requested encoding and is stable. It
	// cannot execute user code and must not disable an unrelated text overload.
	// Check its fixed native identity/body/signature; PostgreSQL still rejects
	// an actually selected stable expression in an index or generated column.
	encodingLength := oid == 1713 && name == "length" && volatility == "s" && language == "internal" && source == "length_in_encoding" && arguments == "17 19" && out.result == 23
	if namespace != "pg_catalog" || kind != "f" || configured || returnsSet || (volatility != "i" && !encodingLength && (scope == "index" || scope == "generated" || volatility != "s" && scope != "default")) || volatility != "i" && volatility != "s" && volatility != "v" {
		return out, fmt.Errorf("core routine language %q, volatility %q, argument types %q: %w", language, volatility, arguments, errPostgresProbeRoutine)
	}
	if volatility == "v" && (!allowSQL || name != "nextval" && name != "gen_random_uuid" && name != "random" && name != "clock_timestamp") {
		return out, errPostgresProbeRoutine
	}
	// SQL wrappers may forward only to this feature's curated core functions.
	// similar_to_escape is the nested terminal of core substring(text,text,text).
	if !allowSQL && name != "similar_to_escape" && !postgresProbeRoutinePolicy(name, scope) {
		return out, errPostgresProbeRoutine
	}
	for _, value := range strings.Fields(arguments) {
		typ, err := strconv.ParseInt(value, 10, 64)
		if err != nil || typ <= 0 || typ >= 16384 {
			return out, errPostgresProbeRoutine
		}
		out.arguments = append(out.arguments, typ)
	}
	if out.result <= 0 || out.result >= 16384 {
		return out, errPostgresProbeRoutine
	}
	if p.types != nil {
		p.types[out.result] = true
		for _, typ := range out.arguments {
			p.types[typ] = true
		}
	}
	if language == "internal" || language == "c" {
		return out, nil
	}
	if language != "sql" || !allowSQL || source != "" {
		return out, errPostgresProbeRoutine
	}
	forwarders, err := schemaddl.PostgreSQLProbeRoutineForwarder(body, out.arguments, out.result)
	if err != nil {
		return out, errors.Join(errPostgresProbeRoutine, err)
	}
	for _, call := range forwarders {
		if call.OID == oid {
			return out, errPostgresProbeRoutine
		}
		actual, err := p.routine(call.OID, scope, false)
		if err != nil {
			return out, err
		}
		if actual.result != call.Result || schemaddl.Signature(actual.arguments) != schemaddl.Signature(call.Arguments) {
			return out, errPostgresProbeRoutine
		}
	}
	return out, nil
}

// Called with the original ACCESS EXCLUSIVE lock already held. Declaring a
// trusted nextval default does not execute it on the empty probe, but ADD COLUMN
// evaluates it for old rows and cannot undo sequence increments on rollback.
func postgresCheckNewSequenceDefaults(ctx context.Context, c *sql.Conn, original schemaddl.Snapshot, desired schemaddl.Table, schema string) error {
	if !original.Exists {
		return nil
	}
	calls, err := schemaddl.PostgreSQLDeclarationProbeCalls(desired, schema)
	if err != nil {
		return err
	}
	prior := map[string]bool{}
	for _, clause := range original.Table.Clauses {
		if clause.Kind == "column" {
			prior[clause.ID] = true
		}
	}
	for _, call := range calls {
		if call.Scope != "default" || call.Name != "nextval" || prior[call.ClauseID] {
			continue
		}
		var rows bool
		if err := c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM ONLY "+schemaddl.Quote(schema)+"."+schemaddl.Quote(original.Table.Name)+" LIMIT 1)").Scan(&rows); err != nil {
			return err
		}
		if rows {
			return errPostgresSequenceDefault
		}
		break
	}
	return ctx.Err()
}
