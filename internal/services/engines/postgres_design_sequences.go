package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// Sequence state is deliberately separate from schema revisions: nextval and
// setval are not MVCC data and cannot be undone by rolling back a table edit.
// Keep exact native integers and the uncalled/called distinction. This does
// not claim to observe another backend's currval or unused cached values.
type postgresDesignSequence struct {
	OID             int64
	Namespace, Name string
	LastValue       int64
	Called          bool
}

func postgresSequenceSources(catalog *schemaddl.PostgreSQLCatalog) ([]postgresDesignSequence, error) {
	if catalog == nil {
		return nil, schemaddl.ErrUnsupported
	}
	if _, err := catalog.Fingerprint(); err != nil {
		return nil, err
	}
	relations := map[int64]schemaddl.PostgreSQLCatalogEntry{}
	for _, e := range catalog.Entries {
		if e.Kind == "relation" {
			if e.SubID != 0 {
				return nil, errors.New("invalid PostgreSQL sequence relation identity")
			}
			relations[e.OID] = e
		}
	}
	var sources []postgresDesignSequence
	for _, e := range catalog.Entries {
		if e.Kind != "sequence" {
			continue
		}
		var sequence struct {
			OID json.RawMessage `json:"seqrelid"`
		}
		var relation struct {
			OID       json.RawMessage `json:"oid"`
			Namespace string          `json:"namespace"`
			Name      string          `json:"relname"`
			Kind      string          `json:"relkind"`
		}
		r, found := relations[e.OID]
		if !found || r.SubID != 0 || e.SubID != 0 || json.Unmarshal(e.Data, &sequence) != nil || json.Unmarshal(r.Data, &relation) != nil {
			return nil, errors.New("incomplete PostgreSQL sequence identity evidence")
		}
		a, ae := postgresOriginalOID(sequence.OID)
		b, be := postgresOriginalOID(relation.OID)
		if ae != nil || be != nil || a != e.OID || b != e.OID || relation.Kind != "S" || schemaddl.PostgreSQLIdentifier(relation.Namespace) != nil || schemaddl.PostgreSQLIdentifier(relation.Name) != nil {
			return nil, errors.New("invalid PostgreSQL sequence identity evidence")
		}
		sources = append(sources, postgresDesignSequence{OID: e.OID, Namespace: relation.Namespace, Name: relation.Name})
	}
	// A captured sequence relation without pg_sequence must not silently escape
	// verification, including a detached or externally referenced sequence.
	seen := map[int64]bool{}
	for _, source := range sources {
		seen[source.OID] = true
	}
	for oid, e := range relations {
		var kind struct {
			Kind string `json:"relkind"`
		}
		if json.Unmarshal(e.Data, &kind) != nil || kind.Kind == "S" && !seen[oid] {
			return nil, errors.New("incomplete PostgreSQL sequence catalog")
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].OID < sources[j].OID })
	return sources, nil
}

// Use the captured OID AND qualified name. A same-named replacement or another
// physical database never supplies state for an originally captured sequence.
// A failure returns no partial evidence; service errors do not expose values.
func postgresSequenceStates(ctx context.Context, c *sql.Conn, catalog *schemaddl.PostgreSQLCatalog) (states map[int64]postgresDesignSequence, err error) {
	defer func() {
		if err != nil {
			states = nil
		}
	}()
	sources, err := postgresSequenceSources(catalog)
	if err != nil {
		return nil, err
	}
	states = make(map[int64]postgresDesignSequence, len(sources))
	for _, source := range sources {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		var database, namespace, name, kind string
		var oid int64
		err = c.QueryRowContext(ctx, `SELECT current_database(),c.oid::bigint,n.nspname,c.relname,c.relkind::text FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=$1`, source.OID).Scan(&database, &oid, &namespace, &name, &kind)
		if err != nil {
			return nil, postgresSequenceReadError(ctx, err)
		}
		if database != catalog.Database || oid != source.OID || namespace != source.Namespace || name != source.Name || kind != "S" {
			return nil, schemaddl.ErrConflict
		}
		var actualOID int64
		err = c.QueryRowContext(ctx, "SELECT tableoid::oid::bigint,last_value,is_called FROM "+quote(source.Namespace)+"."+quote(source.Name)).Scan(&actualOID, &source.LastValue, &source.Called)
		if err != nil {
			return nil, postgresSequenceReadError(ctx, err)
		}
		// The name may have been replaced after the preceding catalog read.
		// tableoid belongs to the actual scanned relation, under its read lock.
		if actualOID != source.OID {
			return nil, schemaddl.ErrConflict
		}
		states[source.OID] = source
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return states, nil
}

func postgresSequenceReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, sql.ErrNoRows) {
		return schemaddl.ErrConflict
	}
	return errors.Join(schemaddl.ErrUnsupported, errors.New("PostgreSQL sequence state unavailable"))
}

func postgresCompareSequenceStates(before, after map[int64]postgresDesignSequence, removed map[int64]bool) error {
	if before == nil || after == nil {
		return errors.New("missing PostgreSQL sequence state evidence")
	}
	for _, states := range []map[int64]postgresDesignSequence{before, after} {
		if len(states) > 10000 {
			return errors.New("PostgreSQL sequence state exceeds record limit")
		}
		for oid, state := range states {
			if oid <= 0 || oid > 1<<32-1 || state.OID != oid || schemaddl.PostgreSQLIdentifier(state.Namespace) != nil || schemaddl.PostgreSQLIdentifier(state.Name) != nil {
				return errors.New("invalid PostgreSQL sequence state identity")
			}
		}
	}
	for oid, allowed := range removed {
		if !allowed {
			return errors.New("invalid PostgreSQL sequence removal permission")
		}
		if _, exists := before[oid]; !exists {
			return errors.New("unknown PostgreSQL sequence removal identity")
		}
	}
	for oid, original := range before {
		current, exists := after[oid]
		if removed[oid] {
			if exists {
				return schemaddl.ErrConflict
			}
			continue
		}
		if !exists || original.OID != oid || current.OID != oid || original.Namespace != current.Namespace || original.Name != current.Name {
			return schemaddl.ErrConflict
		}
		if original.LastValue != current.LastValue || original.Called != current.Called {
			return schemaddl.ErrDataChanged
		}
	}
	return nil
}

// Only a validated column drop or removal of a retained column's identity may
// delete its originally OWNED sequence. Removing a serial default, changing a
// name, or no longer referencing an external sequence grants no such permission.
func postgresSequenceRemovals(original schemaddl.Snapshot, desired schemaddl.Table, schema string, allowDrop bool) (map[int64]bool, error) {
	if _, err := schemaddl.PlanPostgreSQL(original, desired, schema, allowDrop); err != nil {
		return nil, err
	}
	removed := map[int64]bool{}
	if !original.Exists {
		return removed, nil
	}
	if original.Postgres == nil {
		return nil, schemaddl.ErrUnsupported
	}
	bindings, err := schemaddl.BuildPostgreSQLGraphBindings(*original.Postgres, postgresDesignGraphNames(original.Table))
	if err != nil {
		return nil, err
	}
	old, next := map[string]schemaddl.Clause{}, map[string]schemaddl.Clause{}
	for _, c := range original.Table.Columns() {
		old["@sequence:@column:"+c.ID] = c
	}
	for _, c := range desired.Columns() {
		next["@sequence:@column:"+c.ID] = c
	}
	for object, identity := range bindings.Objects {
		if object.ClassOID != bindings.Classes["pg_class"] || !strings.HasPrefix(identity, "@sequence:@column:") {
			continue
		}
		previous, found := old[identity]
		if !found {
			return nil, schemaddl.ErrConflict
		}
		current, retained := next[identity]
		if !retained {
			removed[object.ObjectOID] = true
			continue
		}
		a, ae := schemaddl.ParsePostgreSQLField(previous.SQL)
		b, be := schemaddl.ParsePostgreSQLField(current.SQL)
		if errors.Join(ae, be) != nil {
			return nil, errors.Join(ae, be)
		}
		if a.Identity != "" && b.Identity == "" {
			removed[object.ObjectOID] = true
		}
	}
	return removed, nil
}

func postgresDesignGraphNames(table schemaddl.Table) schemaddl.PostgreSQLGraphNames {
	names := schemaddl.PostgreSQLGraphNames{Columns: map[string]string{}, Constraints: map[string]string{}, Indexes: map[string]string{}, Triggers: map[string]string{}}
	for _, clause := range table.Clauses {
		switch clause.Kind {
		case "column":
			names.Columns[clause.Name] = clause.ID
		case "constraint":
			names.Constraints[clause.Name] = clause.ID
		}
	}
	for _, definition := range table.Definitions {
		switch definition.Kind {
		case "index":
			names.Indexes[definition.Name] = definition.Name
		case "trigger":
			names.Triggers[definition.Name] = definition.Name
		}
	}
	return names
}
