package engines

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

var errPostgresValueRepeatability = errors.New("PostgreSQL new default has no repeatable row-value proof")

// Original rows and supplied after-write ordinary values remain separate.
// Generated values are never supplied to the independent projection. New
// identity columns are verified as individual multisets: separate ALTERs
// assign no stable mapping between identities or the old physical scan order.
type postgresDesignValueProof struct {
	request                   string
	ordinary, identities, all []string
	rows                      int64
	generated                 bool
	ordinaryDigest            postgresDesignDigest
	identityDigests           map[string]postgresDesignDigest
	sequences                 map[string]postgresValueSequenceState
}

type postgresValueSequenceState struct {
	Last   int64
	Called bool
}

// Sequence names/OIDs are private to each independent relation. Stable owner
// clause IDs bind new identity state; original sequences keep their separate
// original-OID/current-state preservation check.
func postgresValueSequenceOwners(catalog *schemaddl.PostgreSQLCatalog, names schemaddl.PostgreSQLGraphNames, wanted map[string]bool) (map[int64]string, error) {
	if catalog == nil {
		return nil, schemaddl.ErrConflict
	}
	b, err := schemaddl.BuildPostgreSQLGraphBindings(*catalog, names)
	if err != nil {
		return nil, err
	}
	out := map[int64]string{}
	seen := map[string]bool{}
	for object, identity := range b.Objects {
		if object.ClassOID != b.Classes["pg_class"] || !strings.HasPrefix(identity, "@sequence:@column:") {
			continue
		}
		owner := strings.TrimPrefix(identity, "@sequence:@column:")
		if !wanted[owner] {
			continue
		}
		if seen[owner] {
			return nil, schemaddl.ErrConflict
		}
		seen[owner] = true
		out[object.ObjectOID] = owner
	}
	if len(seen) != len(wanted) {
		return nil, schemaddl.ErrConflict
	}
	return out, nil
}

func postgresValueSequenceStates(ctx context.Context, c *sql.Conn, catalog *schemaddl.PostgreSQLCatalog, names schemaddl.PostgreSQLGraphNames, wanted map[string]bool) (map[string]postgresValueSequenceState, error) {
	if len(wanted) == 0 {
		return map[string]postgresValueSequenceState{}, ctx.Err()
	}
	owners, err := postgresValueSequenceOwners(catalog, names, wanted)
	if err != nil {
		return nil, err
	}
	states, err := postgresSequenceStates(ctx, c, catalog)
	if err != nil {
		return nil, err
	}
	out := map[string]postgresValueSequenceState{}
	for oid, owner := range owners {
		state, ok := states[oid]
		if !ok {
			return nil, schemaddl.ErrConflict
		}
		out[owner] = postgresValueSequenceState{state.LastValue, state.Called}
	}
	return out, nil
}

func postgresValueProofColumns(original, desired schemaddl.Table) (ordinary, identities, all []string, generated bool, err error) {
	prior := map[string]bool{}
	for _, c := range original.Columns() {
		prior[c.ID] = true
	}
	for _, c := range desired.Columns() {
		f, e := schemaddl.ParsePostgreSQLField(c.SQL)
		if e != nil || f.Unsupported {
			return nil, nil, nil, false, schemaddl.ErrUnsupported
		}
		all = append(all, c.Name)
		if f.Generation != "" {
			generated = true
			continue
		}
		if f.Identity != "" && !prior[c.ID] {
			identities = append(identities, c.Name)
		} else {
			ordinary = append(ordinary, c.Name)
		}
	}
	return
}

// This examines actual captured default trees, including implicit function
// dispatch, before any projection INSERT can evaluate a new default.
func postgresCheckValueRepeatability(ctx context.Context, c *sql.Conn, original schemaddl.Table, canonical postgresDesiredBaseline) error {
	prior := map[string]bool{}
	for _, col := range original.Columns() {
		prior[col.ID] = true
	}
	columns := map[int]string{}
	for _, e := range canonical.Probe.Postgres.Entries {
		if e.Kind != "column" || e.OID != canonical.Probe.Postgres.RelationOID {
			continue
		}
		var col struct {
			Name string `json:"attname"`
		}
		if err := json.Unmarshal(e.Data, &col); err != nil {
			return err
		}
		columns[e.SubID] = col.Name
	}
	newDefaults := map[string]bool{}
	for _, col := range canonical.Table.Columns() {
		f, err := schemaddl.ParsePostgreSQLField(col.SQL)
		if err != nil {
			return err
		}
		if !prior[col.ID] && f.HasDefault && f.Generation == "" && f.Identity == "" {
			newDefaults[col.Name] = true
		}
	}
	seen := map[string]bool{}
	routines := map[int64]bool{}
	for _, e := range canonical.Probe.Postgres.Entries {
		if e.Kind != "default" {
			continue
		}
		var d struct {
			Owner  json.RawMessage `json:"adrelid"`
			Number int             `json:"adnum"`
			Tree   string          `json:"adbin"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return err
		}
		owner, err := postgresOriginalOID(d.Owner)
		if err != nil {
			return err
		}
		if owner != canonical.Probe.Postgres.RelationOID || !newDefaults[columns[d.Number]] {
			continue
		}
		name := columns[d.Number]
		if seen[name] {
			return schemaddl.ErrConflict
		}
		seen[name] = true
		ids, err := schemaddl.PostgreSQLValueExpressionRoutines(d.Tree)
		if err != nil {
			return fmt.Errorf("new default %q: %w", name, errors.Join(errPostgresValueRepeatability, err))
		}
		for _, id := range ids {
			routines[id] = true
		}
	}
	if len(seen) != len(newDefaults) {
		return schemaddl.ErrConflict
	}
	for oid := range routines {
		var namespace, name, volatility string
		err := c.QueryRowContext(ctx, `SELECT n.nspname,p.proname,p.provolatile::text FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE p.oid=$1`, oid).Scan(&namespace, &name, &volatility)
		if err != nil {
			return err
		}
		if namespace != "pg_catalog" || volatility != "i" && volatility != "s" || name == "statement_timestamp" {
			return errPostgresValueRepeatability
		}
	}
	return ctx.Err()
}

func (s *Session) postgresCaptureValueProof(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest, canonical postgresDesiredBaseline, rows int64) (out postgresDesignValueProof, err error) {
	out.request = schemaddl.Signature(r)
	out.rows = rows
	out.ordinary, out.identities, out.all, out.generated, err = postgresValueProofColumns(r.Original.Schema.Table, canonical.Table)
	if err != nil {
		return out, err
	}
	wanted := map[string]bool{}
	identityNames := map[string]bool{}
	for _, name := range out.identities {
		identityNames[name] = true
	}
	for _, col := range canonical.Table.Columns() {
		if identityNames[col.Name] {
			wanted[col.ID] = true
		}
	}
	owners, err := postgresValueSequenceOwners(canonical.Probe.Postgres, canonical.GraphNames, wanted)
	if err != nil {
		return postgresDesignValueProof{}, err
	}
	out.sequences = map[string]postgresValueSequenceState{}
	for _, entry := range canonical.Probe.Postgres.Entries {
		owner := owners[entry.OID]
		if entry.Kind != "sequence" || owner == "" {
			continue
		}
		var sequence struct {
			Start json.RawMessage `json:"seqstart"`
		}
		if err := json.Unmarshal(entry.Data, &sequence); err != nil {
			return postgresDesignValueProof{}, err
		}
		start, err := strconv.ParseInt(strings.Trim(string(sequence.Start), `"`), 10, 64)
		if err != nil {
			return postgresDesignValueProof{}, err
		}
		out.sequences[owner] = postgresValueSequenceState{Last: start, Called: false}
	}
	if len(out.sequences) != len(wanted) {
		return postgresDesignValueProof{}, schemaddl.ErrConflict
	}
	if rows == 0 {
		return out, nil
	}
	if err = postgresCheckValueRepeatability(ctx, c, r.Original.Schema.Table, canonical); err != nil {
		return out, err
	}
	groups := [][]string{out.ordinary}
	for _, name := range out.identities {
		groups = append(groups, []string{name})
	}
	digests, states, err := s.postgresProjectValues(ctx, c, r.Original.Target, r.Original.Schema.Table, canonical.Table, rows, groups, wanted)
	if err != nil {
		return postgresDesignValueProof{}, err
	}
	out.ordinaryDigest = digests[0]
	out.identityDigests = map[string]postgresDesignDigest{}
	for i, name := range out.identities {
		out.identityDigests[name] = digests[i+1]
	}
	out.sequences = states
	return out, nil
}

func (s *Session) postgresVerifyValueProof(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest, canonical postgresDesiredBaseline, actual contract.SchemaDesignSnapshot, proof postgresDesignValueProof) error {
	if proof.request != schemaddl.Signature(r) || actual.Target != r.Original.Target || actual.Schema.Table.Name != canonical.Table.Name {
		return schemaddl.ErrConflict
	}
	wanted := map[string]bool{}
	for id := range proof.sequences {
		wanted[id] = true
	}
	states, err := postgresValueSequenceStates(ctx, c, actual.Schema.Postgres, postgresDesignGraphNames(canonical.Table), wanted)
	if err != nil {
		return err
	}
	if schemaddl.Signature(states) != schemaddl.Signature(proof.sequences) {
		return schemaddl.ErrDataChanged
	}
	groups := [][]string{proof.ordinary}
	for _, name := range proof.identities {
		groups = append(groups, []string{name})
	}
	for i, columns := range groups {
		digest, err := postgresValueDigest(ctx, c, actual.Target.Object, columns, actual.Schema.Postgres)
		if err != nil {
			return err
		}
		if digest.Rows != proof.rows {
			return schemaddl.ErrDataChanged
		}
		if proof.rows == 0 {
			continue
		}
		want := proof.ordinaryDigest
		if i > 0 {
			want = proof.identityDigests[columns[0]]
		}
		if digest != want {
			return schemaddl.ErrDataChanged
		}
	}
	if !proof.generated || proof.rows == 0 {
		return ctx.Err()
	}
	// All ordinary values have their before-write/identity proof. Supply those
	// captured actual values, then independently recompute every generated
	// expression. This includes generated expressions referring to new identity
	// columns without guessing the server's original heap traversal order.
	digests, _, err := s.postgresProjectValues(ctx, c, actual.Target, canonical.Table, canonical.Table, proof.rows, [][]string{proof.all}, nil)
	if err != nil {
		return err
	}
	actualDigest, err := postgresValueDigest(ctx, c, actual.Target.Object, proof.all, actual.Schema.Postgres)
	if err != nil {
		return err
	}
	if actualDigest != digests[0] {
		return schemaddl.ErrDataChanged
	}
	return ctx.Err()
}

// A rollback-only independent relation holds projected rows. It omits CHECK,
// FK, indexes and triggers, because NOT VALID constraints need not hold on old
// rows. Fields keep full native types/defaults/generated/identity options.
// Server resources are owned by this savepoint and are released on cancellation
// acknowledgement or rollback. No source value is rendered into SQL text.
func (s *Session) postgresProjectValues(ctx context.Context, c *sql.Conn, target contract.SchemaDesignTarget, source, desired schemaddl.Table, rows int64, groups [][]string, wanted map[string]bool) (out []postgresDesignDigest, states map[string]postgresValueSequenceState, err error) {
	if err = s.checkPostgresDesign(target); err != nil {
		return nil, nil, err
	}
	if rows < 0 || source.Name != target.Object.Name {
		return nil, nil, schemaddl.ErrConflict
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, nil, err
	}
	name := "__nyadb_values_" + hex.EncodeToString(random[:])
	shadow := schemaddl.Table{Name: name, Postgres: &schemaddl.PostgreSQLTableProperties{Kind: "r", Persistence: "p"}}
	for _, col := range desired.Columns() {
		f, e := schemaddl.ParsePostgreSQLField(col.SQL)
		if e != nil || f.Unsupported {
			return nil, nil, schemaddl.ErrUnsupported
		}
		if f.Identity == "" {
			f.NotNull = false
		}
		if f.Identity != "" {
			_, _, f.IdentityOptions, err = schemaddl.PostgreSQLSequenceName(f.IdentityOptions)
			if err != nil {
				return nil, nil, err
			}
		}
		col.Postgres = nil
		col.SQL, err = f.SQL()
		if err != nil {
			return nil, nil, err
		}
		shadow.Clauses = append(shadow.Clauses, col)
	}
	if err = postgresCheckDeclarationProbeRoutines(ctx, c, shadow, target.Object.Schema); err != nil {
		return nil, nil, err
	}
	if err = postgresCheckProbeTypes(ctx, c, shadow, target.Object.Schema); err != nil {
		return nil, nil, err
	}
	if _, err = c.ExecContext(ctx, "SAVEPOINT nyadb_value_projection"); err != nil {
		return nil, nil, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_, rollbackErr := c.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT nyadb_value_projection")
		var releaseErr error
		if rollbackErr == nil {
			_, releaseErr = c.ExecContext(cleanup, "RELEASE SAVEPOINT nyadb_value_projection")
		}
		if rollbackErr != nil || releaseErr != nil {
			discardDesign(c)
			err = errors.Join(err, ErrUncertain, rollbackErr, releaseErr)
		}
		if err != nil {
			out, states = nil, nil
		}
	}()
	shadowTarget := target
	shadowTarget.Object.Name = name
	missing, err := postgresCatalogSnapshot(ctx, c, shadowTarget)
	if err != nil {
		return nil, nil, err
	}
	if missing.Schema.Exists {
		return nil, nil, schemaddl.ErrConflict
	}
	plan, err := schemaddl.PlanPostgreSQL(missing.Schema, shadow, target.Object.Schema, false)
	if err != nil {
		return nil, nil, err
	}
	for _, statement := range plan.SQL {
		if _, err = c.ExecContext(ctx, statement); err != nil {
			return nil, nil, err
		}
	}
	byID := map[string]schemaddl.Clause{}
	for _, col := range source.Columns() {
		byID[col.ID] = col
	}
	var destinations, inputs []string
	for _, col := range desired.Columns() {
		prior, ok := byID[col.ID]
		if !ok {
			continue
		}
		f, e := schemaddl.ParsePostgreSQLField(col.SQL)
		if e != nil {
			return nil, nil, e
		}
		if f.Generation != "" {
			continue
		}
		destinations = append(destinations, quote(col.Name))
		inputs = append(inputs, quote(prior.Name))
	}
	if len(inputs) > 0 {
		statement := "INSERT INTO " + qualified(shadowTarget.Object) + " (" + strings.Join(destinations, ",") + ") OVERRIDING SYSTEM VALUE SELECT " + strings.Join(inputs, ",") + " FROM ONLY " + qualified(target.Object)
		result, e := c.ExecContext(ctx, statement)
		if e != nil {
			return nil, nil, e
		}
		inserted, e := result.RowsAffected()
		if e != nil || inserted != rows {
			return nil, nil, errors.Join(schemaddl.ErrDataChanged, e)
		}
	} else {
		// With no retained ordinary inputs, every source row has the same
		// declaration input. Bounded DEFAULT batches avoid a 1601st carrier
		// column and retain support for the full PostgreSQL attribute limit.
		values := make([]string, len(shadow.Columns()))
		for i := range values {
			values[i] = "DEFAULT"
		}
		value := "(" + strings.Join(values, ",") + ")"
		batch := 128
		if n := (1 << 20) / len(value); n < batch {
			batch = n
		}
		if batch <= 0 {
			return nil, nil, schemaddl.ErrUnsupported
		}
		for remaining := rows; remaining > 0; {
			n := int64(batch)
			if remaining < n {
				n = remaining
			}
			parts := make([]string, int(n))
			for i := range parts {
				parts[i] = value
			}
			if _, err = c.ExecContext(ctx, "INSERT INTO "+qualified(shadowTarget.Object)+" VALUES "+strings.Join(parts, ",")); err != nil {
				return nil, nil, err
			}
			remaining -= n
		}
	}
	captured, err := postgresCatalogSnapshot(ctx, c, shadowTarget)
	if err != nil {
		return nil, nil, err
	}
	for _, columns := range groups {
		digest, e := postgresValueDigest(ctx, c, shadowTarget.Object, columns, captured.Schema.Postgres)
		if e != nil {
			return nil, nil, e
		}
		if digest.Rows != rows {
			return nil, nil, schemaddl.ErrDataChanged
		}
		out = append(out, digest)
	}
	states, err = postgresValueSequenceStates(ctx, c, captured.Schema.Postgres, postgresDesignGraphNames(shadow), wanted)
	if err != nil {
		return nil, nil, err
	}
	return out, states, ctx.Err()
}
