package engines

import (
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// postgresDesignDDLApply owns the complete native CREATE/ALTER transaction,
// including the independent declaration and original-context verification.
// It remains private until referenced-type concurrency and the remaining
// implicit callback scopes are settled. Engine-aware forms never grant Apply.
func (s *Session) postgresDesignDDLApply(ctx context.Context, request contract.SchemaDesignRequest) (out contract.SchemaDesignOutcome) {
	out.State = "not_executed"
	r := request.Clone()
	t := r.Original.Target
	if err := s.checkPostgresDesign(t); err != nil {
		out.Error = err
		return
	}
	if r.Original.Schema.Table.Name != t.Object.Name || r.Original.Schema.Postgres == nil || r.Original.Schema.Postgres.Database != t.Object.Catalog || r.Original.Schema.Postgres.Namespace != t.Object.Schema {
		out.Error = schemaddl.ErrConflict
		return
	}
	plan, err := schemaddl.PlanPostgreSQL(r.Original.Schema, r.Desired, t.Object.Schema, r.AllowColumnDrop)
	if err != nil {
		out.Error = err
		return
	}
	switch plan.Mode {
	case "noop":
		return s.postgresDesignMetadataApply(ctx, r)
	case "rename_table", "rename_column":
		return s.postgresDesignRenameApply(ctx, r)
	case "create", "alter":
	default:
		out.Error = schemaddl.ErrUnsupported
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
			out.State = "uncertain"
			out.Error = errors.Join(out.Error, ErrUncertain, rollbackErr)
		} else {
			out.State = "rolled_back"
		}
		out.Verified, out.Actual = false, nil
	}()
	for _, setting := range []string{"SET LOCAL search_path=pg_catalog", "SET LOCAL standard_conforming_strings=on", "SET LOCAL row_security=off"} {
		if _, err = c.ExecContext(ctx, setting); err != nil {
			out.Error = err
			return
		}
	}
	// Acquire the original relation lock before the first catalog SELECT sets
	// the repeatable-read snapshot. A writer committing during this wait must
	// be visible to the original revision check, not to just later DDL parsing.
	if r.Original.Schema.Exists {
		if _, err = c.ExecContext(ctx, "LOCK TABLE ONLY "+qualified(t.Object)+" IN ACCESS EXCLUSIVE MODE"); err != nil {
			out.Error = err
			return
		}
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
	// Use the captured request only after its complete original before-image
	// has been checked. Neither the caller's preview SQL nor its current profile
	// may replace the plan for this locked original scope.
	plan, err = schemaddl.PlanPostgreSQL(actual.Schema, r.Desired, t.Object.Schema, r.AllowColumnDrop)
	if err != nil {
		out.Error = err
		return
	}
	sequences, err := postgresSequenceStates(ctx, c, actual.Schema.Postgres)
	if err != nil {
		out.Error = err
		return
	}
	removedSequences, err := postgresSequenceRemovals(actual.Schema, r.Desired, t.Object.Schema, r.AllowColumnDrop)
	if err != nil {
		out.Error = err
		return
	}
	if err = postgresCheckNewSequenceDefaults(ctx, c, actual.Schema, r.Desired, t.Object.Schema); err != nil {
		out.Error = err
		return
	}
	canonical, err := s.postgresCanonicalDesired(ctx, c, r)
	if err != nil {
		out.Error = err
		return
	}
	var beforeReferences []schemaddl.PostgreSQLReferenceBaseline
	verificationContext := actual.Schema.Postgres
	if actual.Schema.Exists {
		beforeWrite, err := postgresCatalogSnapshotPreserving(ctx, c, t, canonical.Probe.Postgres)
		if err != nil {
			out.Error = err
			return
		}
		reference, err := schemaddl.CapturePostgreSQLReferenceBaseline(actual.Schema, r.Desired, canonical.Probe, beforeWrite.Schema)
		if err != nil {
			out.Error = err
			return
		}
		beforeReferences = []schemaddl.PostgreSQLReferenceBaseline{reference}
		verificationContext = beforeWrite.Schema.Postgres.Clone()
	}
	probeSequences, err := postgresSequenceStates(ctx, c, actual.Schema.Postgres)
	if err != nil {
		out.Error = err
		return
	}
	if err = postgresCompareSequenceStates(sequences, probeSequences, nil); err != nil {
		out.Error = err
		return
	}
	beforeNames, afterNames, err := postgresRetainedValues(actual.Schema.Table, r.Desired)
	if err != nil {
		out.Error = err
		return
	}
	var before postgresDesignDigest
	if actual.Schema.Exists {
		before, err = postgresValueDigest(ctx, c, t.Object, beforeNames, actual.Schema.Postgres)
		if err != nil {
			out.Error = err
			return
		}
	}
	valueProof, err := s.postgresCaptureValueProof(ctx, c, r, canonical, before.Rows)
	if err != nil {
		out.Error = err
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
	if !verified.Schema.Exists {
		out.Error = schemaddl.ErrConflict
		return
	}
	if err = schemaddl.VerifyPostgreSQLShape(verified.Schema.Table, canonical.Table, t.Object.Schema); err != nil {
		out.Error = err
		return
	}
	var references []schemaddl.PostgreSQLGraphReference
	if actual.Schema.Exists {
		references = []schemaddl.PostgreSQLGraphReference{{Catalog: *actual.Schema.Postgres, Names: postgresDesignGraphNames(actual.Schema.Table)}}
	}
	got, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*verified.Schema.Postgres, postgresDesignGraphNames(canonical.Table))
	if err != nil {
		out.Error = err
		return
	}
	want, err := schemaddl.ProjectPostgreSQLDeclarationGraph(*canonical.Probe.Postgres, canonical.GraphNames, references...)
	if err != nil {
		out.Error = err
		return
	}
	if actual.Schema.Exists {
		want, err = schemaddl.PostgreSQLRetainedSerialDeclaration(actual.Schema, r.Desired, want)
		if err != nil {
			out.Error = err
			return
		}
	}
	if err = schemaddl.ComparePostgreSQLDeclarationGraphs(got, want); err != nil {
		out.Error = err
		return
	}
	if actual.Schema.Exists {
		preservation, err := postgresCatalogSnapshotPreserving(ctx, c, resultTarget, verificationContext)
		if err != nil {
			out.Error = err
			return
		}
		if err = schemaddl.VerifyPostgreSQLRetainedCatalog(actual.Schema, r.Desired, preservation.Schema, want, t.Object.Schema, r.AllowColumnDrop); err != nil {
			out.Error = err
			return
		}
		storage, err := schemaddl.VerifyPostgreSQLStorage(actual.Schema, r.Desired, preservation.Schema, canonical.Probe, t.Object.Schema, r.AllowColumnDrop, beforeReferences...)
		if err != nil {
			out.Error = err
			return
		}
		if err = schemaddl.VerifyPostgreSQLUnselectedCatalog(actual.Schema, r.Desired, preservation.Schema, want, t.Object.Schema, r.AllowColumnDrop, storage); err != nil {
			out.Error = err
			return
		}
	}
	after, err := postgresValueDigest(ctx, c, resultTarget.Object, afterNames, verified.Schema.Postgres)
	if err != nil {
		out.Error = err
		return
	}
	if before != after {
		out.Error = schemaddl.ErrDataChanged
		return
	}
	if err = s.postgresVerifyValueProof(ctx, c, r, canonical, verified, valueProof); err != nil {
		out.Error = err
		return
	}
	sequenceCatalog, err := postgresCatalogSnapshotPreserving(ctx, c, resultTarget, actual.Schema.Postgres)
	if err != nil {
		out.Error = err
		return
	}
	currentSequences, err := postgresSequenceStates(ctx, c, sequenceCatalog.Schema.Postgres)
	if err != nil {
		out.Error = err
		return
	}
	if err = postgresCompareSequenceStates(sequences, currentSequences, removedSequences); err != nil {
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
