package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// This executor stays private while native reference/callback concurrency and
// full lifecycle/UI verification are developed. No product capability, runtime
// registration or engine-aware form grants access to it.
func (s *Session) openGaussDesignApply(ctx context.Context, request contract.SchemaDesignRequest) (out contract.SchemaDesignOutcome) {
	out.State = "not_executed"
	r := request.Clone()
	target := r.Original.Target
	if err := s.checkOpenGaussTable(target); err != nil {
		out.Error = err
		return
	}
	prior := r.Original.Schema.OpenGauss
	if prior == nil || r.Original.Schema.Table.Name != target.Object.Name || prior.Database != target.Object.Catalog || prior.Namespace != target.Object.Schema || prior.Name != target.Object.Name || !r.Original.Schema.Exists && r.Desired.Name != target.Object.Name {
		out.Error = schemaddl.ErrConflict
		return
	}
	if _, err := schemaddl.PlanOpenGauss(r.Original.Schema, r.Desired, target.Object.Schema, r.AllowColumnDrop); err != nil {
		out.Error = err
		return
	}
	if err := openGaussCheckDesignExecutionScope(r); err != nil {
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
	if _, err = c.ExecContext(ctx, "BEGIN ISOLATION LEVEL READ COMMITTED"); err != nil {
		discardDesign(c)
		out.Error = errors.Join(err, ctx.Err())
		return
	}
	settled, commitAttempted := false, false
	defer func() {
		if settled {
			return
		}
		out.Error = errors.Join(out.Error, ctx.Err())
		rollbackErr := rollbackDesign(c)
		out.Verified, out.Actual = false, nil
		if rollbackErr != nil || commitAttempted || errors.Is(out.Error, ErrUncertain) {
			discardDesign(c)
			out.State = "uncertain"
			out.Error = errors.Join(out.Error, ErrUncertain, rollbackErr, s.Close())
		} else {
			out.State = "rolled_back"
		}
	}()
	if _, err = c.ExecContext(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
		out.Error = err
		return
	}
	if r.Original.Schema.Exists {
		if _, err = c.ExecContext(ctx, "LOCK TABLE "+qualified(target.Object)+" IN ACCESS EXCLUSIVE MODE"); err != nil {
			out.Error = err
			return
		}
	}
	// The fresh READ COMMITTED statement follows lock acknowledgement. A DDL
	// commit or relation replacement made while waiting must be visible here.
	fresh, err := openGaussReadDesign(ctx, c, target)
	if err != nil || fresh.Schema.Revision != r.Original.Schema.Revision || fresh.Schema.Exists != r.Original.Schema.Exists || schemaddl.Signature(fresh.Schema.Table) != schemaddl.Signature(r.Original.Schema.Table) || fresh.Schema.DDL != r.Original.Schema.DDL {
		out.Error = errors.Join(err, schemaddl.ErrConflict)
		return
	}
	r.Original = fresh.Clone()
	if err = openGaussCheckDesignExecutionScope(r); err != nil {
		out.Error = err
		return
	}
	plan, err := schemaddl.PlanOpenGauss(fresh.Schema, r.Desired, target.Object.Schema, r.AllowColumnDrop)
	if err != nil {
		out.Error = err
		return
	}
	references, err := openGaussLockDesignReferences(ctx, c, r)
	if err != nil {
		out.Error = err
		return
	}
	if fresh.Schema.Exists {
		if err = openGaussCheckDesignDeclaration(ctx, c, fresh.Schema.Table, target.Object.Schema); err == nil {
			err = openGaussCopyPrograms(ctx, c, fresh.Schema.OpenGauss)
		}
		if err != nil {
			out.Error = err
			return
		}
		if err = openGaussCheckDesignSelfReferences(ctx, c, fresh.Schema.OpenGauss); err != nil {
			out.Error = err
			return
		}
	}
	definition, err := openGaussDesignOriginalDefinition(fresh)
	if err != nil {
		out.Error = err
		return
	}
	sequences, err := openGaussCopySequenceStates(ctx, c, definition)
	if err != nil {
		out.Error = err
		return
	}
	canonical, err := openGaussCanonicalDesign(ctx, c, r)
	if err != nil {
		out.Error = err
		return
	}
	plan, err = openGaussEffectiveDesignPlan(fresh, r.Desired, canonical, r.AllowColumnDrop)
	if err != nil {
		out.Error = err
		return
	}
	var before postgresDesignDigest
	if fresh.Schema.Exists {
		before, err = openGaussRetainedDesignDigest(ctx, c, target.Object, fresh.Schema.Table, r.Desired, false)
		if err != nil {
			out.Error = err
			return
		}
	}
	proof, err := openGaussCaptureValueProof(ctx, c, r, canonical, before.Rows)
	if err != nil {
		out.Error = err
		return
	}
	if err = openGaussVerifyOriginalDesignSequences(ctx, c, r, definition, sequences, false); err != nil {
		out.Error = err
		return
	}
	if err = openGaussVerifyDesignReferences(ctx, c, r, references, canonical, nil); err != nil {
		out.Error = err
		return
	}
	if err = openGaussVerifyDesignIncomingUnchanged(ctx, c, r, canonical.Incoming); err != nil {
		out.Error = err
		return
	}
	for _, statement := range plan.SQL {
		if _, err = c.ExecContext(ctx, statement); err != nil {
			out.Error = err
			return
		}
	}
	resultTarget := target
	resultTarget.Object.Name = r.Desired.Name
	actual, err := openGaussReadDesign(ctx, c, resultTarget)
	if err != nil {
		out.Error = err
		return
	}
	incomingActual, err := openGaussReadDesignIncomingActual(ctx, c, fresh, canonical)
	if err != nil {
		out.Error = err
		return
	}
	incomingActual, err = openGaussRestoreDesignIncomingComments(ctx, c, r, canonical, actual, incomingActual)
	if err != nil {
		out.Error = err
		return
	}
	if err = openGaussVerifyDesignCatalogWithIncoming(fresh, r.Desired, canonical, actual, incomingActual); err != nil {
		out.Error = err
		return
	}
	if err = openGaussCopyPrograms(ctx, c, actual.Schema.OpenGauss); err != nil {
		out.Error = err
		return
	}
	if err = openGaussCheckDesignSelfReferences(ctx, c, actual.Schema.OpenGauss); err != nil {
		out.Error = err
		return
	}
	after, err := openGaussRetainedDesignDigest(ctx, c, resultTarget.Object, fresh.Schema.Table, r.Desired, true)
	if err != nil || before != after {
		out.Error = errors.Join(err, schemaddl.ErrDataChanged)
		return
	}
	if err = openGaussVerifyValueProof(ctx, c, r, canonical, actual, proof); err != nil {
		out.Error = err
		return
	}
	if err = openGaussVerifyOriginalDesignSequences(ctx, c, r, definition, sequences, true); err != nil {
		out.Error = err
		return
	}
	if err = openGaussVerifyDesignReferences(ctx, c, r, references, canonical, &actual); err != nil {
		out.Error = err
		return
	}
	incomingActual, err = openGaussReadDesignIncomingActual(ctx, c, fresh, canonical)
	if err != nil {
		out.Error = err
		return
	}
	if err = openGaussVerifyDesignIncomingTransition(ctx, c, r, references, canonical, actual, incomingActual); err != nil {
		out.Error = err
		return
	}
	if err = s.checkOpenGaussTable(target); err != nil {
		out.Error = err
		return
	}
	commitAttempted = true
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		out.Error = err
		return
	}
	settled = true
	out.State, out.Verified, out.Actual, out.Rows = "committed", true, &actual, before.Rows
	return
}

func openGaussDesignOriginalDefinition(original contract.SchemaDesignSnapshot) (schemaddl.OpenGaussCopyDefinition, error) {
	if !original.Schema.Exists {
		return schemaddl.OpenGaussCopyDefinition{}, nil
	}
	return original.Schema.OpenGauss.CopyDefinition()
}

// External references use independent locks; self references are part of the
// locked original table and its complete independent declaration graph.
// Incoming transitions use independent empty children and retain native locks
// for key type changes. Policies and new rule actions need separate proofs.
func openGaussCheckDesignExecutionScope(r contract.SchemaDesignRequest) error {
	for _, entry := range r.Original.Schema.OpenGauss.Entries {
		if entry.Kind == "policy" {
			return errors.New("native table design row policies require independent verification")
		}
	}
	for _, definition := range r.Desired.Definitions {
		if definition.Kind != "rule" {
			continue
		}
		retained := false
		for _, original := range r.Original.Schema.Table.Definitions {
			retained = retained || original.Kind == "rule" && original.Name == definition.Name && original.SQL == definition.SQL
		}
		if !retained {
			return errors.New("native table design new rule actions require independent callback verification")
		}
	}
	return nil
}

func openGaussVerifyOriginalDesignSequences(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest, original schemaddl.OpenGaussCopyDefinition, states map[uint64]openGaussCopySequenceState, after bool) error {
	retained := map[int]bool{}
	for _, col := range r.Desired.Columns() {
		for _, old := range r.Original.Schema.Table.Columns() {
			if old.ID == col.ID && old.OpenGauss != nil {
				retained[old.OpenGauss.Number] = true
			}
		}
	}
	var selected schemaddl.OpenGaussCopyDefinition
	for _, sequence := range original.Sequences {
		if after && !retained[sequence.Column] {
			if !r.AllowColumnDrop {
				return schemaddl.ErrConflict
			}
			var exists bool
			if err := c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_class WHERE oid=$1::oid)", sequence.OID).Scan(&exists); err != nil || exists {
				return errors.Join(err, fmt.Errorf("removed native owned sequence %d survived design: %w", sequence.OID, schemaddl.ErrConflict))
			}
			continue
		}
		selected.Sequences = append(selected.Sequences, sequence)
	}
	current, err := openGaussCopySequenceStates(ctx, c, selected)
	if err != nil {
		return err
	}
	for _, sequence := range selected.Sequences {
		if current[sequence.OID] != states[sequence.OID] {
			return errors.New("original native sequence changed during table design")
		}
	}
	return ctx.Err()
}
