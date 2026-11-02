package engines

import (
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// postgresDesignMetadataApply remains private until engine-aware forms and a
// general PostgreSQL planner exist. In particular it must not enable SQLite's
// create/rebuild controls via Session.SchemaDesign().
func (s *Session) postgresDesignMetadataApply(ctx context.Context, request contract.SchemaDesignRequest) (out contract.SchemaDesignOutcome) {
	return s.postgresDesignTransaction(ctx, request, true)
}

// postgresDesignRenameApply uses the complete original catalog proof. It is
// private until PostgreSQL's designer forms and general Apply are bound.
func (s *Session) postgresDesignRenameApply(ctx context.Context, request contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
	return s.postgresDesignTransaction(ctx, request, false)
}

func (s *Session) postgresDesignTransaction(ctx context.Context, request contract.SchemaDesignRequest, metadata bool) (out contract.SchemaDesignOutcome) {
	out.State = "not_executed"
	r := request.Clone()
	t := r.Original.Target
	if err := s.checkPostgresDesign(t); err != nil {
		out.Error = err
		return
	}
	if r.Original.Schema.Table.Name != t.Object.Name || r.Original.Schema.Postgres == nil || r.Original.Schema.Postgres.Database != t.Object.Catalog {
		out.Error = schemaddl.ErrConflict
		return
	}
	var plan schemaddl.Plan
	var err error
	if metadata {
		plan, err = schemaddl.PlanPostgreSQLMetadata(r.Original.Schema, r.Desired, t.Object.Schema)
	} else {
		plan, err = schemaddl.PlanPostgreSQL(r.Original.Schema, r.Desired, t.Object.Schema, r.AllowColumnDrop)
		if err == nil && plan.Mode != "rename_table" && plan.Mode != "rename_column" {
			err = schemaddl.ErrUnsupported
		}
	}
	if err != nil {
		out.Error = err
		return
	}
	ctx, done := s.requestContext(ctx)
	defer done()
	c, err := s.db.Conn(ctx)
	if err != nil {
		out.Error = err
		return
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ"); err != nil {
		out.Error = err
		return
	}
	settled, commitAttempted := false, false
	defer func() {
		if settled {
			return
		}
		rollbackErr := rollbackDesign(c)
		if rollbackErr != nil || commitAttempted {
			discardDesign(c)
		}
		// PostgreSQL accepts ROLLBACK even outside a transaction. Once COMMIT
		// was sent, successful cleanup cannot prove it did not already commit.
		if commitAttempted || rollbackErr != nil {
			out.State, out.Verified, out.Actual = "uncertain", false, nil
			out.Error = errors.Join(out.Error, ErrUncertain, rollbackErr)
		} else {
			out.State = "rolled_back"
		}
	}()
	for _, setting := range []string{"SET LOCAL search_path = pg_catalog", "SET LOCAL standard_conforming_strings = on", "SET LOCAL row_security = off"} {
		if _, err = c.ExecContext(ctx, setting); err != nil {
			out.Error = err
			return
		}
	}
	// LOCK is a utility statement: the repeatable-read snapshot is established
	// by the first catalog SELECT only after this lock is acquired. A concurrent
	// committed ALTER while waiting must therefore fail the original revision.
	if _, err = c.ExecContext(ctx, "LOCK TABLE ONLY "+qualified(t.Object)+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		out.Error = err
		return
	}
	actual, err := postgresCatalogSnapshot(ctx, c, t)
	if err != nil {
		out.Error = err
		return
	}
	if actual.Schema.Revision != r.Original.Schema.Revision || schemaddl.Signature(actual.Schema) != schemaddl.Signature(r.Original.Schema) {
		out.Error = schemaddl.ErrConflict
		return
	}
	// Event triggers can mutate data or unrelated schemas even during a
	// metadata-only operation or rename. Both executors refuse enabled hooks.
	var hooks bool
	if err = c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_event_trigger WHERE evtenabled<>'D')").Scan(&hooks); err != nil || hooks {
		out.Error = errors.Join(err, errors.New("PostgreSQL design editing requires no enabled event triggers"))
		return
	}
	if plan.Mode == "noop" {
		if _, err = c.ExecContext(ctx, "ROLLBACK"); err != nil {
			out.Error = err
			return
		}
		settled = true
		out.Actual, out.Verified = &actual, true
		return
	}
	for _, statement := range plan.SQL {
		if _, err = c.ExecContext(ctx, statement); err != nil {
			out.Error = err
			return
		}
	}
	resultTarget := t
	resultTarget.Object.Name = r.Desired.Name
	verified, err := postgresCatalogSnapshot(ctx, c, resultTarget)
	if err != nil {
		out.Error = err
		return
	}
	if metadata {
		err = schemaddl.VerifyPostgreSQLMetadata(actual.Schema, r.Desired, verified.Schema, t.Object.Schema)
	} else {
		err = schemaddl.VerifyPostgreSQLRename(actual.Schema, r.Desired, verified.Schema, t.Object.Schema)
	}
	if err != nil {
		out.Error = err
		return
	}
	if err = s.checkPostgresDesign(t); err != nil {
		out.Error = err
		return
	}
	commitAttempted = true
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		out.Error = err
		return
	}
	settled = true
	out.State, out.Verified, out.Actual = "committed", true, &verified
	return
}
