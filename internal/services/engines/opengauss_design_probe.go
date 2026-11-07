package engines

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type openGaussDesignBaseline struct {
	Table          schemaddl.Table
	Catalog        *schemaddl.OpenGaussCatalog
	Names          schemaddl.OpenGaussDesignNames
	References     map[schemaddl.PostgreSQLTableReference]schemaddl.OpenGaussReferenceProbe
	Incoming       map[uint64]openGaussIncomingEvidence
	IncomingProbes map[uint64]openGaussIncomingProbe
}

// An independent, empty declaration asks the native parser to normalize
// aliases, generated expressions and constraints. It never executes the ALTER
// plan or inserts rows. The entire probe and its private sequences/indexes are
// rolled back before the actual original table may be changed.
func openGaussCanonicalDesign(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest) (out openGaussDesignBaseline, err error) {
	r = r.Clone()
	target := r.Original.Target
	if r.Original.Schema.OpenGauss == nil || r.Original.Schema.Engine != "opengauss" || r.Original.Schema.Table.Name != target.Object.Name {
		return out, schemaddl.ErrConflict
	}
	if !r.Original.Schema.OpenGauss.CanCreate {
		return out, errors.New("native declaration validation requires schema CREATE")
	}
	if r.Original.Schema.OpenGauss.Hooks {
		return out, errors.New("native declaration validation requires no enabled DDL event triggers")
	}
	requestedPlan, err := schemaddl.PlanOpenGauss(r.Original.Schema, r.Desired, target.Object.Schema, r.AllowColumnDrop)
	if err != nil {
		return out, err
	}
	if err = openGaussCheckDesignDeclaration(ctx, c, r.Desired, target.Object.Schema); err != nil {
		return out, err
	}
	incoming, err := openGaussCaptureDesignIncoming(ctx, c, r)
	if err != nil {
		return out, err
	}
	// Reference context is captured before an independent FK installs its
	// temporary parent-side triggers. These probes do not establish locks that
	// survive savepoint rollback; the actual executor must hold separate locks.
	refs, err := openGaussDesignExternalReferences(r, r.Desired)
	if err != nil {
		return out, err
	}
	referenceBefore := map[schemaddl.PostgreSQLTableReference]schemaddl.OpenGaussReferenceBaseline{}
	referenceCatalogs := map[schemaddl.PostgreSQLTableReference]*schemaddl.OpenGaussCatalog{}
	for _, ref := range refs {
		refTarget := target
		refTarget.Object.Schema, refTarget.Object.Name = ref.Schema, ref.Name
		catalog, readErr := openGaussReadDesignReference(ctx, c, refTarget)
		if readErr != nil {
			return out, readErr
		}
		evidence, captureErr := schemaddl.CaptureOpenGaussReferenceBaseline(r.Original.Schema, r.Desired, catalog)
		if captureErr != nil {
			return out, captureErr
		}
		referenceBefore[ref], referenceCatalogs[ref] = evidence, catalog.Clone()
	}
	referenceProbes := map[schemaddl.PostgreSQLTableReference]schemaddl.OpenGaussReferenceProbe{}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return out, err
	}
	name := "__nyadb_gauss_design_" + hex.EncodeToString(random[:])
	probeTarget := target
	probeTarget.Object.Name = name
	absent, err := openGaussReadDesign(ctx, c, probeTarget)
	if err != nil || absent.Schema.Exists {
		return out, errors.Join(err, schemaddl.ErrConflict)
	}
	probe := r.Desired.Clone()
	probe.Name = name
	probe.OpenGauss.Owner = ""
	from := r.Desired.Name
	if requestedPlan.Mode == "rename" {
		from = r.Original.Schema.Table.Name
	}
	constraints := map[string]schemaddl.Clause{}
	definitions := map[string]schemaddl.Definition{}
	columns := map[string]schemaddl.Clause{}
	names := schemaddl.OpenGaussDesignNames{Columns: map[string]string{}, Constraints: map[string]string{}, Definitions: map[string]string{}}
	if len(referenceCatalogs) != 0 {
		names.ReferenceRelations = map[uint64]string{}
		for ref, catalog := range referenceCatalogs {
			names.ReferenceRelations[catalog.RelationOID] = "@reference:" + schemaddl.Quote(ref.Schema) + "." + schemaddl.Quote(ref.Name)
		}
	}
	if r.Original.Schema.Exists {
		before, err := r.Original.Schema.OpenGauss.CopyDefinition()
		if err != nil {
			return out, err
		}
		if names.ReferenceRelations == nil {
			names.ReferenceRelations = map[uint64]string{}
		}
		for _, seq := range before.Sequences {
			for _, col := range r.Original.Schema.Table.Columns() {
				if col.OpenGauss != nil && col.OpenGauss.Number == seq.Column {
					names.ReferenceRelations[seq.OID] = "@sequence:@column:" + col.ID
					names.ReferenceSequences = append(names.ReferenceSequences, seq)
				}
			}
		}
	}
	for i, col := range probe.Clauses {
		if col.OpenGauss != nil {
			probe.Clauses[i].OpenGauss.Number, probe.Clauses[i].OpenGauss.OID = 0, 0
		}
		if col.Kind == "column" {
			columns[col.Name] = r.Desired.Clauses[i]
			names.Columns[col.Name] = r.Desired.Clauses[i].ID
			if requestedPlan.Mode == "rename_column" {
				for _, prior := range r.Original.Schema.Table.Columns() {
					if prior.ID == col.ID {
						probe.Clauses[i].Name = prior.Name
					}
				}
			}
			continue
		}
		private := fmt.Sprintf("%s_c_%d", name, i)
		constraints[private] = r.Desired.Clauses[i]
		names.Constraints[private] = r.Desired.Clauses[i].ID
		probe.Clauses[i], err = schemaddl.RebindPostgreSQLConstraint(probe.Clauses[i], target.Object.Schema, from, name, private)
		if err != nil {
			return out, err
		}
	}
	var probeDefinitions []schemaddl.Definition
	for i, d := range probe.Definitions {
		// Existing rules are verified against the original context, separately
		// from the independent ordinary declaration. Never compile their action
		// bodies as an expression-safety probe.
		if d.Kind == "rule" {
			continue
		}
		private := fmt.Sprintf("%s_d_%d", name, i)
		definitions[private] = r.Desired.Definitions[i]
		names.Definitions[d.Kind+":"+private] = r.Desired.Definitions[i].Name
		d, err = schemaddl.RebindPostgreSQLDefinition(d, target.Object.Schema, from, name, private)
		if err != nil {
			return out, err
		}
		if d.OpenGauss != nil {
			d.OpenGauss.OID = 0
		}
		probeDefinitions = append(probeDefinitions, d)
	}
	probe.Definitions = probeDefinitions
	plan, err := schemaddl.PlanOpenGauss(absent.Schema, probe, target.Object.Schema, true)
	if err != nil {
		return out, err
	}
	if _, err = c.ExecContext(ctx, "SAVEPOINT nyadb_gauss_design_probe"); err != nil {
		return out, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, rollbackErr := c.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT nyadb_gauss_design_probe")
		var releaseErr error
		if rollbackErr == nil {
			_, releaseErr = c.ExecContext(cleanup, "RELEASE SAVEPOINT nyadb_gauss_design_probe")
		}
		if rollbackErr != nil || releaseErr != nil {
			discardDesign(c)
			err = errors.Join(err, ErrUncertain, rollbackErr, releaseErr)
		} else {
			err = errors.Join(err, openGaussVerifyDesignIncomingUnchanged(cleanup, c, r, incoming))
			for ref, evidence := range referenceBefore {
				refTarget := target
				refTarget.Object.Schema, refTarget.Object.Name = ref.Schema, ref.Name
				after, readErr := openGaussReadDesignReference(cleanup, c, refTarget)
				var verifyErr error
				if readErr == nil {
					verifyErr = schemaddl.VerifyOpenGaussReferenceUnchanged(evidence, r.Original.Schema, r.Desired, after)
				}
				err = errors.Join(err, readErr, verifyErr)
			}
		}
		if err != nil {
			out = openGaussDesignBaseline{}
		}
	}()
	for _, q := range plan.SQL {
		if _, err = c.ExecContext(ctx, q); err != nil {
			return out, err
		}
	}
	if requestedPlan.Mode == "rename_column" {
		for _, prior := range r.Original.Schema.Table.Columns() {
			for _, next := range r.Desired.Columns() {
				if prior.ID == next.ID && prior.Name != next.Name {
					if _, err = c.ExecContext(ctx, "ALTER TABLE "+qualified(probeTarget.Object)+" RENAME COLUMN "+quote(prior.Name)+" TO "+quote(next.Name)); err != nil {
						return out, err
					}
				}
			}
		}
	}
	snapshot, err := openGaussReadDesign(ctx, c, probeTarget)
	if err != nil || !snapshot.Schema.Exists {
		return out, errors.Join(err, schemaddl.ErrConflict)
	}
	if err = openGaussCopyPrograms(ctx, c, snapshot.Schema.OpenGauss); err != nil {
		return out, err
	}
	if err = openGaussCheckDesignSelfReferences(ctx, c, snapshot.Schema.OpenGauss); err != nil {
		return out, err
	}
	for ref, evidence := range referenceBefore {
		refTarget := target
		refTarget.Object.Schema, refTarget.Object.Name = ref.Schema, ref.Name
		parent, readErr := openGaussReadDesignReference(ctx, c, refTarget)
		if readErr != nil {
			return out, readErr
		}
		if err = openGaussCheckDesignReferenceCallbacks(ctx, c, snapshot.Schema.OpenGauss, parent); err != nil {
			return out, err
		}
		proof, captureErr := schemaddl.CaptureOpenGaussReferenceProbe(evidence, r.Original.Schema, r.Desired, snapshot.Schema.OpenGauss, names, parent)
		if captureErr != nil {
			return out, captureErr
		}
		referenceProbes[ref] = proof
	}
	// Native normalized text is bound back to original stable declaration IDs.
	// These private object OIDs remain solely in the probe's immutable catalog.
	canonical := snapshot.Schema.Table.Clone()
	canonical.Name = r.Desired.Name
	canonical.OpenGauss.Owner = r.Desired.OpenGauss.Owner
	for i, col := range canonical.Clauses {
		if col.Kind == "column" {
			wanted, ok := columns[col.Name]
			if !ok {
				return out, schemaddl.ErrConflict
			}
			canonical.Clauses[i].ID = wanted.ID
			if wanted.OpenGauss != nil {
				canonical.Clauses[i].OpenGauss = wanted.OpenGauss.Clone()
			} else {
				canonical.Clauses[i].OpenGauss = nil
			}
			continue
		}
		wanted, ok := constraints[col.Name]
		if !ok {
			return out, schemaddl.ErrConflict
		}
		canonical.Clauses[i], err = schemaddl.RebindPostgreSQLConstraint(col, target.Object.Schema, name, r.Desired.Name, wanted.Name)
		if err != nil {
			return out, err
		}
		canonical.Clauses[i].ID = wanted.ID
		canonical.Clauses[i].OpenGauss = wanted.OpenGauss.Clone()
	}
	for i, d := range canonical.Definitions {
		wanted, ok := definitions[d.Name]
		if !ok {
			return out, schemaddl.ErrConflict
		}
		canonical.Definitions[i], err = schemaddl.RebindPostgreSQLDefinition(d, target.Object.Schema, name, r.Desired.Name, wanted.Name)
		if err != nil {
			return out, err
		}
		canonical.Definitions[i].OpenGauss = wanted.OpenGauss.Clone()
	}
	for _, d := range r.Desired.Definitions {
		if d.Kind == "rule" {
			canonical.Definitions = append(canonical.Definitions, d)
		}
	}
	// Detect accidental private-name leakage before the baseline is returned.
	for _, col := range canonical.Columns() {
		f, e := schemaddl.ParseOpenGaussField(col.SQL)
		if e != nil {
			return out, e
		}
		if f.HasDefault && strings.Contains(f.Default, name) {
			// A new serial's sequence is intentionally private in the prototype.
			// The eventual executor binds it by column ownership, never this name.
			wanted := columns[col.Name]
			wf, e := schemaddl.ParseOpenGaussField(wanted.SQL)
			if e != nil {
				return out, e
			}
			kind := strings.ToLower(wf.Type)
			if kind != "serial" && kind != "serial4" && kind != "bigserial" && kind != "serial8" && kind != "smallserial" && kind != "serial2" {
				return out, schemaddl.ErrConflict
			}
		}
	}
	incomingProbes, err := openGaussCreateDesignIncomingProbes(ctx, c, r, snapshot, incoming)
	if err != nil {
		return out, err
	}
	if len(incomingProbes) != 0 {
		snapshot, err = openGaussReadDesign(ctx, c, probeTarget)
		if err != nil {
			return out, err
		}
		for oid, probe := range incomingProbes {
			probe.Proof, err = schemaddl.CaptureOpenGaussIncomingTransitionProbe(incoming[oid].Baseline, r.Original.Schema, r.Desired, snapshot.Schema.OpenGauss, probe.Catalog, names, probe.Names)
			if err != nil {
				return out, err
			}
			incomingProbes[oid] = probe
		}
	}
	out = openGaussDesignBaseline{Table: canonical, Catalog: snapshot.Schema.OpenGauss.Clone(), Names: names, References: referenceProbes, Incoming: incoming, IncomingProbes: incomingProbes}
	return out, nil
}
