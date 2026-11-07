package engines

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type openGaussDesignReferences map[schemaddl.PostgreSQLTableReference]*schemaddl.OpenGaussCatalog

// A name-only rename retains the old self-reference spelling until native
// ALTER rewrites it. Neither spelling may become a separate parent lock.
func openGaussDesignExternalReferences(r contract.SchemaDesignRequest, table schemaddl.Table) ([]schemaddl.PostgreSQLTableReference, error) {
	refs, err := schemaddl.PostgreSQLReferencedTables(table, r.Original.Target.Object.Schema)
	if err != nil {
		return nil, err
	}
	out := refs[:0]
	for _, ref := range refs {
		if ref.Schema != r.Original.Target.Object.Schema || ref.Name != r.Original.Target.Object.Name {
			out = append(out, ref)
		}
	}
	return out, nil
}

func openGaussCheckDesignSelfReferences(ctx context.Context, c *sql.Conn, catalog *schemaddl.OpenGaussCatalog) error {
	if catalog == nil || catalog.RelationOID == 0 {
		return nil
	}
	definition, err := catalog.CopyDefinition()
	if err != nil {
		return err
	}
	for _, constraint := range definition.Constraints {
		if constraint.Kind == "f" && constraint.ReferenceOID == catalog.RelationOID {
			return openGaussCheckDesignReferenceCallbacks(ctx, c, catalog, catalog)
		}
	}
	return ctx.Err()
}

// Native FK creation acquires ShareRowExclusiveLock using REFERENCES rights.
// Create and DROP run outside any probe savepoint: native DROP removes the
// temporary objects while retaining this lock until the owning transaction
// acknowledges COMMIT/ROLLBACK. No SELECT grant on the parent is required.
func openGaussLockDesignReferences(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest) (openGaussDesignReferences, error) {
	sources := map[schemaddl.PostgreSQLTableReference]schemaddl.Table{}
	for _, table := range []schemaddl.Table{r.Original.Schema.Table, r.Desired} {
		refs, err := openGaussDesignExternalReferences(r, table)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			sources[ref] = table.Clone()
		}
	}
	refs := make([]schemaddl.PostgreSQLTableReference, 0, len(sources))
	for ref := range sources {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].Schema < refs[j].Schema || refs[i].Schema == refs[j].Schema && refs[i].Name < refs[j].Name
	})
	out := openGaussDesignReferences{}
	for _, ref := range refs {
		source := sources[ref]
		target := r.Original.Target
		target.Object.Schema, target.Object.Name = ref.Schema, ref.Name
		before, err := openGaussReadDesignReference(ctx, c, target)
		if err != nil {
			return nil, err
		}
		baseline, err := schemaddl.CaptureOpenGaussReferenceBaseline(r.Original.Schema, source, before)
		if err != nil {
			return nil, err
		}
		var nonce [12]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		anchorTarget := r.Original.Target
		anchorTarget.Object.Name = "__nyadb_gauss_ref_" + hex.EncodeToString(nonce[:])
		absent, err := openGaussReadDesign(ctx, c, anchorTarget)
		if err != nil || absent.Schema.Exists {
			return nil, errors.Join(err, schemaddl.ErrConflict)
		}
		anchor, names, err := schemaddl.OpenGaussReferenceAnchor(source, anchorTarget.Object.Schema, anchorTarget.Object.Name, ref)
		if err != nil {
			return nil, err
		}
		if err = openGaussCheckDesignDeclaration(ctx, c, anchor, anchorTarget.Object.Schema); err != nil {
			return nil, err
		}
		plan, err := schemaddl.PlanOpenGauss(absent.Schema, anchor, anchorTarget.Object.Schema, true)
		if err != nil {
			return nil, err
		}
		for _, statement := range plan.SQL {
			if _, err = c.ExecContext(ctx, statement); err != nil {
				return nil, err
			}
		}
		child, err := openGaussReadDesign(ctx, c, anchorTarget)
		if err != nil {
			return nil, err
		}
		parent, err := openGaussReadDesignReference(ctx, c, target)
		if err != nil {
			return nil, err
		}
		if err = openGaussCheckDesignReferenceCallbacks(ctx, c, child.Schema.OpenGauss, parent); err != nil {
			return nil, err
		}
		proof, err := schemaddl.CaptureOpenGaussReferenceProbe(baseline, r.Original.Schema, source, child.Schema.OpenGauss, names, parent)
		if err != nil {
			return nil, err
		}
		if _, err = c.ExecContext(ctx, "DROP TABLE "+qualified(anchorTarget.Object)+" RESTRICT"); err != nil {
			return nil, err
		}
		after, err := openGaussReadDesignReference(ctx, c, target)
		if err != nil {
			return nil, err
		}
		if err = schemaddl.VerifyOpenGaussReferenceProbeReleased(proof, r.Original.Schema, source, after, "drop"); err != nil {
			return nil, err
		}
		if err = openGaussVerifyDesignReferenceLock(ctx, c, after.RelationOID); err != nil {
			return nil, err
		}
		out[ref] = after.Clone()
	}
	return out, nil
}

func openGaussVerifyDesignReferenceLock(ctx context.Context, c *sql.Conn, oid uint64) error {
	var held bool
	err := c.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND relation=$1::oid
 AND mode IN('ShareRowExclusiveLock','AccessExclusiveLock') AND granted)`, oid).Scan(&held)
	if err != nil || !held {
		return errors.Join(err, fmt.Errorf("native reference transaction lock %d is absent: %w", oid, schemaddl.ErrConflict))
	}
	return nil
}

func openGaussVerifyDesignReferences(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest, references openGaussDesignReferences, canonical openGaussDesignBaseline, actual *contract.SchemaDesignSnapshot) error {
	for ref, before := range references {
		target := r.Original.Target
		target.Object.Schema, target.Object.Name = ref.Schema, ref.Name
		if err := openGaussVerifyDesignReferenceLock(ctx, c, before.RelationOID); err != nil {
			return err
		}
		parent, err := openGaussReadDesignReference(ctx, c, target)
		if err != nil {
			return err
		}
		baseline, err := schemaddl.CaptureOpenGaussReferenceBaseline(r.Original.Schema, r.Desired, before)
		if err != nil {
			return err
		}
		if actual == nil {
			err = schemaddl.VerifyOpenGaussReferenceUnchanged(baseline, r.Original.Schema, r.Desired, parent)
		} else {
			if incoming, ok := canonical.IncomingProbes[parent.RelationOID]; ok {
				err = schemaddl.VerifyOpenGaussReferenceTransitionWithIncoming(baseline, r.Original.Schema, r.Desired, canonical.References[ref], actual.Schema.OpenGauss, parent, incoming.Proof)
			} else {
				err = schemaddl.VerifyOpenGaussReferenceTransition(baseline, r.Original.Schema, r.Desired, canonical.References[ref], actual.Schema.OpenGauss, parent)
			}
			if err == nil {
				refs, e := schemaddl.PostgreSQLReferencedTables(r.Desired, r.Original.Target.Object.Schema)
				if e != nil {
					return e
				}
				for _, selected := range refs {
					if selected == ref {
						err = openGaussCheckDesignReferenceCallbacks(ctx, c, actual.Schema.OpenGauss, parent)
					}
				}
			}
		}
		if err != nil {
			return fmt.Errorf("native reference %s.%s: %w", ref.Schema, ref.Name, err)
		}
	}
	return ctx.Err()
}
