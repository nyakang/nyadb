package engines

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type postgresDesiredBaseline struct {
	Table schemaddl.Table
	// Probe retains independent server catalog semantics for later verification.
	// Its ephemeral identities never become workbench documents or stored assets.
	Probe schemaddl.Snapshot
	// Explicit joins survive raw probe names and normalized desired names.
	GraphNames schemaddl.PostgreSQLGraphNames
}

// postgresCanonicalDesired uses a rollback-only savepoint to ask PostgreSQL to
// normalize a complete independent CREATE declaration. It does not apply the
// ALTER plan or write table rows. Existing ACL/options outside the declaration
// stay separate for comparison with the original complete catalog.
func (s *Session) postgresCanonicalDesired(ctx context.Context, c *sql.Conn, request contract.SchemaDesignRequest) (out postgresDesiredBaseline, err error) {
	r := request.Clone()
	target := r.Original.Target
	if err = s.checkPostgresDesign(target); err != nil {
		return out, err
	}
	if r.Original.Schema.Postgres == nil || r.Original.Schema.Postgres.Database != target.Object.Catalog || r.Original.Schema.Postgres.Namespace != target.Object.Schema || r.Original.Schema.Table.Name != target.Object.Name {
		return out, schemaddl.ErrConflict
	}
	plan, err := schemaddl.PlanPostgreSQL(r.Original.Schema, r.Desired, target.Object.Schema, r.AllowColumnDrop)
	if err != nil {
		return out, err
	}
	if plan.Mode != "create" && plan.Mode != "alter" {
		return out, schemaddl.ErrUnsupported
	}
	ctx, done := s.requestContext(ctx)
	defer done()
	var hooks bool
	if err = c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_event_trigger WHERE evtenabled<>'D')").Scan(&hooks); err != nil {
		return out, err
	}
	if hooks {
		return out, errors.New("definition validation requires no enabled PostgreSQL event triggers")
	}
	if err = postgresCheckIndexProbeRoutines(ctx, c, r.Desired, target.Object.Schema); err != nil {
		return out, err
	}
	if err = postgresCheckProbeTypes(ctx, c, r.Desired, target.Object.Schema); err != nil {
		return out, err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return out, err
	}
	name := "__nyadb_probe_" + hex.EncodeToString(random[:])
	if _, err = c.ExecContext(ctx, "SAVEPOINT nyadb_definition_probe"); err != nil {
		return out, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_, rollbackErr := c.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT nyadb_definition_probe")
		var releaseErr error
		if rollbackErr == nil {
			_, releaseErr = c.ExecContext(cleanup, "RELEASE SAVEPOINT nyadb_definition_probe")
		}
		if rollbackErr != nil || releaseErr != nil {
			discardDesign(c)
			err = errors.Join(err, ErrUncertain, rollbackErr, releaseErr)
		}
		if err != nil {
			out = postgresDesiredBaseline{}
		}
	}()
	for _, setting := range []string{"SET LOCAL search_path=pg_catalog", "SET LOCAL standard_conforming_strings=on"} {
		if _, err = c.ExecContext(ctx, setting); err != nil {
			return out, err
		}
	}
	probe := r.Desired.Clone()
	probe.Name = name
	graphNames := schemaddl.PostgreSQLGraphNames{Columns: map[string]string{}, Constraints: map[string]string{}, Indexes: map[string]string{}, Triggers: map[string]string{}}
	if probe.Postgres != nil {
		probe.Postgres.Owner = ""
		probe.Postgres.ACL = nil
		if probe.Postgres.ReplicaIdentity == "i" {
			probe.Postgres.ReplicaIdentity = "d"
		}
	}
	columns := map[string]schemaddl.Clause{}
	constraintNames, definitionNames := map[string]schemaddl.Clause{}, map[string]schemaddl.Definition{}
	for i := range probe.Clauses {
		clause := probe.Clauses[i]
		if clause.Kind == "constraint" {
			newName := fmt.Sprintf("%s_c_%d", name, i)
			constraintNames[newName] = clause
			graphNames.Constraints[newName] = clause.ID
			probe.Clauses[i], err = schemaddl.RebindPostgreSQLConstraint(clause, target.Object.Schema, r.Desired.Name, name, newName)
			if err != nil {
				return out, err
			}
			continue
		}
		columns[clause.Name] = r.Desired.Clauses[i]
		graphNames.Columns[clause.Name] = clause.ID
		field, e := schemaddl.ParsePostgreSQLField(clause.SQL)
		if e != nil {
			return out, e
		}
		if field.Identity != "" {
			_, _, field.IdentityOptions, err = schemaddl.PostgreSQLSequenceName(field.IdentityOptions)
			if err != nil {
				return out, err
			}
		}
		probe.Clauses[i].SQL, err = field.SQL()
		if err != nil {
			return out, err
		}
		if properties := probe.Clauses[i].Postgres; properties != nil {
			properties.ACL = nil
			properties.Parameters = nil
			properties.ForeignOptions = nil
		}
	}
	for i, definition := range probe.Definitions {
		newName := fmt.Sprintf("%s_d_%d", name, i)
		definitionNames[newName] = definition
		if definition.Kind == "index" {
			graphNames.Indexes[newName] = definition.Name
		} else if definition.Kind == "trigger" {
			graphNames.Triggers[newName] = definition.Name
		}
		probe.Definitions[i], err = schemaddl.RebindPostgreSQLDefinition(definition, target.Object.Schema, r.Desired.Name, name, newName)
		if err != nil {
			return out, err
		}
	}
	// CREATE on an empty table marks NOT VALID constraints validated. ALTER
	// must retain the requested validation state, so normalize those complete
	// declarations through independent ADD CONSTRAINT statements instead.
	var deferredConstraints []schemaddl.Clause
	if plan.Mode == "alter" {
		clauses := make([]schemaddl.Clause, 0, len(probe.Clauses))
		for _, clause := range probe.Clauses {
			if clause.Kind == "constraint" {
				unvalidated, e := schemaddl.PostgreSQLConstraintUnvalidated(clause, target.Object.Schema)
				if e != nil {
					return out, e
				}
				if unvalidated {
					deferredConstraints = append(deferredConstraints, clause)
					continue
				}
			}
			clauses = append(clauses, clause)
		}
		probe.Clauses = clauses
	}
	probeTarget := target
	probeTarget.Object.Name = name
	missing, err := postgresCatalogSnapshot(ctx, c, probeTarget)
	if err != nil {
		return out, err
	}
	if missing.Schema.Exists {
		return out, schemaddl.ErrConflict
	}
	create, err := schemaddl.PlanPostgreSQL(missing.Schema, probe, target.Object.Schema, false)
	if err != nil {
		return out, err
	}
	for _, statement := range create.SQL {
		if _, err = c.ExecContext(ctx, statement); err != nil {
			return out, err
		}
	}
	for _, constraint := range deferredConstraints {
		if _, err = c.ExecContext(ctx, "ALTER TABLE ONLY "+qualified(probeTarget.Object)+" ADD "+constraint.SQL+"\n"); err != nil {
			return out, err
		}
	}
	actual, err := postgresCatalogSnapshot(ctx, c, probeTarget)
	if err != nil {
		return out, err
	}
	if !actual.Schema.Exists {
		return out, errors.New("complete PostgreSQL definition probe missing")
	}
	out.Probe = actual.Schema.Clone()
	out.GraphNames = graphNames
	out.Table = actual.Schema.Table.Clone()
	out.Table.Name = r.Desired.Name
	if desired := r.Desired.Postgres; desired != nil {
		if desired.Owner != "" {
			out.Table.Postgres.Owner = desired.Owner
		}
		out.Table.Postgres.ACL = desired.Clone().ACL
		if desired.ReplicaIdentity == "i" {
			out.Table.Postgres.ReplicaIdentity = "i"
		}
	}
	for i := range out.Table.Clauses {
		clause := &out.Table.Clauses[i]
		if clause.Kind == "constraint" {
			desired, ok := constraintNames[clause.Name]
			if !ok {
				return out, errors.New("unexpected canonical PostgreSQL constraint")
			}
			*clause, err = schemaddl.RebindPostgreSQLConstraint(*clause, target.Object.Schema, name, r.Desired.Name, desired.Name)
			if err != nil {
				return out, err
			}
			clause.ID = desired.ID
			continue
		}
		desired, ok := columns[clause.Name]
		if !ok {
			return out, errors.New("unexpected canonical PostgreSQL column")
		}
		clause.ID = desired.ID
		field, e := schemaddl.ParsePostgreSQLField(clause.SQL)
		if e != nil {
			return out, e
		}
		if field.Identity != "" {
			_, _, remaining, e := schemaddl.PostgreSQLSequenceName(field.IdentityOptions)
			if e != nil {
				return out, e
			}
			requested, e := schemaddl.ParsePostgreSQLField(desired.SQL)
			if e != nil {
				return out, e
			}
			schema, sequence, _, e := schemaddl.PostgreSQLSequenceName(requested.IdentityOptions)
			if e != nil {
				return out, e
			}
			field.IdentityOptions = remaining
			if sequence != "" {
				if schema == "" {
					schema = target.Object.Schema
				}
				field.IdentityOptions = "SEQUENCE NAME " + schemaddl.Quote(schema) + "." + schemaddl.Quote(sequence) + " " + remaining
			}
		}
		clause.SQL, err = field.SQL()
		if err != nil {
			return out, err
		}
		if properties := desired.Postgres; properties != nil {
			clone := properties.Clone()
			clause.Postgres.ACL = clone.ACL
			clause.Postgres.Parameters = clone.Parameters
			clause.Postgres.ForeignOptions = clone.ForeignOptions
		}
	}
	for i := range out.Table.Definitions {
		definition := &out.Table.Definitions[i]
		desired, ok := definitionNames[definition.Name]
		if !ok {
			return out, errors.New("unexpected canonical PostgreSQL definition")
		}
		*definition, err = schemaddl.RebindPostgreSQLDefinition(*definition, target.Object.Schema, name, r.Desired.Name, desired.Name)
		if err != nil {
			return out, err
		}
	}
	if err = s.checkPostgresDesign(target); err != nil {
		return out, err
	}
	return out, nil
}
