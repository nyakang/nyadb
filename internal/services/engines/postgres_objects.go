package engines

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func (s *Session) postgresObjectTransaction(ctx context.Context, r contract.SchemaDesignSnapshot, apply func(context.Context, *sql.Conn) (contract.SchemaDesignTarget, error)) (out contract.SchemaDesignOutcome) {
	out.State = "not_executed"
	r = r.Clone()
	t := r.Target
	if e := s.checkPostgresDesign(t); e != nil {
		out.Error = e
		return
	}
	if !r.Schema.Exists || r.Schema.Postgres == nil || r.Schema.Table.Name != t.Object.Name {
		out.Error = schemaddl.ErrConflict
		return
	}
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	c, e := s.db.Conn(ctx)
	if e != nil {
		out.Error = e
		return
	}
	defer c.Close()
	if _, e = c.ExecContext(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on; SET LOCAL row_security=off"); e != nil {
		out.Error = e
		return
	}
	settled, commit := false, false
	defer func() {
		if !settled {
			rb := rollbackDesign(c)
			if commit || rb != nil {
				discardDesign(c)
				out.State = "uncertain"
				out.Error = errors.Join(out.Error, rb)
			} else {
				out.State = "rolled_back"
			}
		}
	}()
	if _, e = c.ExecContext(ctx, "LOCK TABLE ONLY "+qualified(t.Object)+" IN ACCESS EXCLUSIVE MODE"); e != nil {
		out.Error = e
		return
	}
	actual, e := postgresCatalogSnapshot(ctx, c, t)
	if e != nil {
		out.Error = e
		return
	}
	if schemaddl.Signature(actual.Schema) != schemaddl.Signature(r.Schema) {
		out.Error = schemaddl.ErrConflict
		return
	}
	props := actual.Schema.Table.Postgres
	if props == nil || props.Kind != "r" || props.IsPartition || len(props.Parents) > 0 {
		out.Error = contract.ErrUnsupported
		return
	}
	var hooks bool
	if e = c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_event_trigger WHERE evtenabled<>'D')").Scan(&hooks); e != nil || hooks {
		out.Error = errors.Join(e, contract.ErrUnsupported)
		return
	}
	result, e := apply(ctx, c)
	if e != nil {
		out.Error = e
		return
	}
	verified, e := postgresCatalogSnapshot(ctx, c, result)
	if e != nil {
		out.Error = e
		return
	}
	commit = true
	if _, e = c.ExecContext(ctx, "COMMIT"); e != nil {
		out.Error = e
		return
	}
	settled = true
	out.State, out.Verified, out.Actual = "committed", true, &verified
	return
}
func (s *Session) postgresDrop(ctx context.Context, r contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
	return s.postgresObjectTransaction(ctx, r, func(ctx context.Context, c *sql.Conn) (contract.SchemaDesignTarget, error) {
		_, e := c.ExecContext(ctx, "DROP TABLE "+qualified(r.Target.Object)+" RESTRICT")
		return r.Target, e
	})
}
func (s *Session) postgresCopy(ctx context.Context, r contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
	r = r.Clone()
	plan, e := schemaddl.PlanPostgreSQLCopy(r.Original.Schema, r.Destination, r.WithData, r.RebindSelfReferences)
	if e != nil {
		return contract.SchemaDesignOutcome{State: "not_executed", Error: e}
	}
	return s.postgresObjectTransaction(ctx, r.Original, func(ctx context.Context, c *sql.Conn) (contract.SchemaDesignTarget, error) {
		target := r.Original.Target
		target.Object.Name = r.Destination
		var hasStatistics bool
		if e := c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_statistic_ext WHERE stxrelid=to_regclass($1))", qualified(r.Original.Target.Object)).Scan(&hasStatistics); e != nil {
			return target, e
		}
		if hasStatistics {
			return target, errors.New("extended statistics definitions cannot be represented by copying")
		}
		for _, q := range plan.SQL {
			if _, e := c.ExecContext(ctx, q); e != nil {
				return target, e
			}
		}
		return target, nil
	})
}
