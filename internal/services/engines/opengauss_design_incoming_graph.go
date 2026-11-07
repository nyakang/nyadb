package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func openGaussReadDesignIncomingActual(ctx context.Context, c *sql.Conn, original contract.SchemaDesignSnapshot, baseline openGaussDesignBaseline) (map[uint64]*schemaddl.OpenGaussCatalog, error) {
	out := map[uint64]*schemaddl.OpenGaussCatalog{}
	for oid, evidence := range baseline.Incoming {
		before := evidence.Baseline.Catalog()
		target := original.Target
		target.Object.Schema, target.Object.Name = before.Namespace, before.Name
		child, err := openGaussReadDesignReference(ctx, c, target)
		if err != nil || child.RelationOID != oid {
			return nil, errors.Join(err, schemaddl.ErrConflict)
		}
		out[oid] = child.Clone()
	}
	return out, ctx.Err()
}

func openGaussRestoreDesignIncomingComments(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest, baseline openGaussDesignBaseline, actual contract.SchemaDesignSnapshot, children map[uint64]*schemaddl.OpenGaussCatalog) (map[uint64]*schemaddl.OpenGaussCatalog, error) {
	changed := false
	for oid, probe := range baseline.IncomingProbes {
		statements, err := schemaddl.OpenGaussIncomingCommentRestoration(probe.Proof, r.Original.Schema, r.Desired, actual.Schema.OpenGauss, children[oid])
		if err != nil {
			return nil, fmt.Errorf("native incoming child %d comment preservation: %w", oid, err)
		}
		for _, statement := range statements {
			// Native COMMENT checks the child owner's rights. REFERENCES alone
			// is enough for an uncommented FK rebind; it cannot grant permission
			// to restore a lost comment. Any denial rolls back the parent ALTER.
			if _, err = c.ExecContext(ctx, statement); err != nil {
				return nil, fmt.Errorf("native incoming child %d comment restoration: %w", oid, err)
			}
			changed = true
		}
	}
	if changed {
		return openGaussReadDesignIncomingActual(ctx, c, r.Original, baseline)
	}
	return children, ctx.Err()
}

func openGaussDesignIncomingGraphNames(names schemaddl.OpenGaussDesignNames, original contract.SchemaDesignSnapshot, desired schemaddl.Table, baseline openGaussDesignBaseline, parent *schemaddl.OpenGaussCatalog, children map[uint64]*schemaddl.OpenGaussCatalog, mode string) (schemaddl.OpenGaussDesignNames, error) {
	for oid, evidence := range baseline.Incoming {
		var child *schemaddl.OpenGaussCatalog
		var privateNames *schemaddl.OpenGaussDesignNames
		switch mode {
		case "original":
			child = evidence.Baseline.Catalog()
		case "probe":
			probe, ok := baseline.IncomingProbes[oid]
			if !ok {
				return names, schemaddl.ErrConflict
			}
			child, privateNames = probe.Catalog, &probe.Names
		case "actual":
			child = children[oid]
		default:
			return names, schemaddl.ErrConflict
		}
		var err error
		names, err = schemaddl.BindOpenGaussIncomingGraph(names, evidence.Baseline, original.Schema, desired, parent, child, privateNames)
		if err != nil {
			return names, fmt.Errorf("native incoming graph %s child %d: %w", mode, oid, err)
		}
	}
	return names, nil
}

func openGaussVerifyDesignIncomingTransition(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest, references openGaussDesignReferences, baseline openGaussDesignBaseline, actual contract.SchemaDesignSnapshot, children map[uint64]*schemaddl.OpenGaussCatalog) error {
	for oid, probe := range baseline.IncomingProbes {
		child := children[oid]
		if child == nil {
			return schemaddl.ErrConflict
		}
		var err error
		ref := schemaddl.PostgreSQLTableReference{Schema: child.Namespace, Name: child.Name}
		if before, cycle := references[ref]; cycle {
			// Use this final fresh child for both halves of the proof. Include
			// original-only references when the desired outgoing FK is removed;
			// their empty probe proves absence, not a weaker retained-context check.
			var reference schemaddl.OpenGaussReferenceBaseline
			reference, err = schemaddl.CaptureOpenGaussReferenceBaseline(r.Original.Schema, r.Desired, before)
			if err == nil {
				err = schemaddl.VerifyOpenGaussReferenceTransitionWithIncoming(reference, r.Original.Schema, r.Desired, baseline.References[ref], actual.Schema.OpenGauss, child, probe.Proof)
			}
		} else {
			err = schemaddl.VerifyOpenGaussIncomingTransition(probe.Proof, r.Original.Schema, r.Desired, actual.Schema.OpenGauss, child)
		}
		if err != nil {
			return fmt.Errorf("native incoming child %d: %w", oid, err)
		}
		if err := openGaussCheckDesignReferenceCallbacks(ctx, c, child, actual.Schema.OpenGauss); err != nil {
			return err
		}
		needed, err := schemaddl.OpenGaussIncomingNeedsChildLock(probe.Proof, r.Original.Schema, r.Desired, actual.Schema.OpenGauss)
		if err != nil {
			return err
		}
		if needed {
			var held bool
			if err = c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND relation=$1::oid AND mode='AccessExclusiveLock' AND granted)", oid).Scan(&held); err != nil || !held {
				return errors.Join(err, fmt.Errorf("native incoming child %d type transition lacks transaction AccessExclusiveLock: %w", oid, schemaddl.ErrConflict))
			}
		}
	}
	return ctx.Err()
}
