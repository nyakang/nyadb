package engines

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type openGaussIncomingEvidence struct {
	Baseline schemaddl.OpenGaussIncomingBaseline
	Fields   map[string]schemaddl.PostgreSQLField
}

type openGaussIncomingProbe struct {
	Table   schemaddl.Table
	Names   schemaddl.OpenGaussDesignNames
	Catalog *schemaddl.OpenGaussCatalog
	Proof   schemaddl.OpenGaussIncomingTransitionProbe
}

// Read raw child metadata without requiring its management, dumping programs
// or reading sequence state. Permissions for actual native parent ALTER are
// enforced by that statement; independent declarations grant no such rights.
func openGaussCaptureDesignIncoming(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest) (map[uint64]openGaussIncomingEvidence, error) {
	oids, err := schemaddl.OpenGaussIncomingRelations(r.Original.Schema)
	if err != nil {
		return nil, err
	}
	out := map[uint64]openGaussIncomingEvidence{}
	for _, oid := range oids {
		target := r.Original.Target
		if err = c.QueryRowContext(ctx, "SELECT n.nspname,c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=$1::oid", oid).Scan(&target.Object.Schema, &target.Object.Name); err != nil {
			return nil, err
		}
		child, err := openGaussReadDesignReference(ctx, c, target)
		if err != nil || child.RelationOID != oid {
			return nil, errors.Join(err, schemaddl.ErrConflict)
		}
		baseline, err := schemaddl.CaptureOpenGaussIncomingBaseline(r.Original.Schema, r.Desired, child)
		if err != nil {
			return nil, err
		}
		for _, entry := range child.Entries {
			if entry.Kind == "policy" {
				return nil, errors.New("native incoming row policies require independent callback verification")
			}
		}
		if err = openGaussCheckDesignReferenceCallbacks(ctx, c, child, r.Original.Schema.OpenGauss); err != nil {
			return nil, err
		}
		fields := map[string]schemaddl.PostgreSQLField{}
		for _, constraint := range baseline.Constraints() {
			for i, name := range constraint.Columns {
				if _, ok := fields[name]; ok {
					continue
				}
				var field schemaddl.PostgreSQLField
				var native bool
				// CASE must guard format_type: custom typmod output is an
				// executable callback, even in a metadata-only SELECT.
				err = c.QueryRowContext(ctx, `SELECT n.nspname='pg_catalog' AND t.typtype='b' AND t.oid<16384,
 CASE WHEN n.nspname='pg_catalog' AND t.typtype='b' AND t.oid<16384 THEN format_type(a.atttypid,a.atttypmod) ELSE '' END,
 COALESCE((SELECT quote_ident(ns.nspname)||'.'||quote_ident(co.collname)
 FROM pg_collation co JOIN pg_namespace ns ON ns.oid=co.collnamespace WHERE co.oid=a.attcollation AND co.oid<>100),'')
 FROM pg_attribute a JOIN pg_type t ON t.oid=a.atttypid JOIN pg_namespace n ON n.oid=t.typnamespace
 WHERE a.attrelid=$1::oid AND a.attnum=$2 AND a.attname=$3 AND NOT a.attisdropped`, oid, constraint.ColumnNumbers[i], name).Scan(&native, &field.Type, &field.Collation)
				if err != nil || !native {
					return nil, errors.Join(err, errOpenGaussDesignDeclaration)
				}
				fields[name] = field
			}
		}
		out[oid] = openGaussIncomingEvidence{Baseline: baseline, Fields: fields}
	}
	return out, ctx.Err()
}

func openGaussVerifyDesignIncomingUnchanged(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest, incoming map[uint64]openGaussIncomingEvidence) error {
	for oid, evidence := range incoming {
		before := evidence.Baseline.Catalog()
		target := r.Original.Target
		target.Object.Schema, target.Object.Name = before.Namespace, before.Name
		after, err := openGaussReadDesignReference(ctx, c, target)
		if err == nil {
			err = schemaddl.VerifyOpenGaussIncomingUnchanged(evidence.Baseline, r.Original.Schema, r.Desired, after)
		}
		if err != nil {
			return fmt.Errorf("native incoming child %d: %w", oid, err)
		}
	}
	return ctx.Err()
}

// This runs inside the canonical parent's private savepoint. Selected empty
// child declarations point only to that private parent, never to the original
// table. The caller captures the parent again, then acknowledges rollback and
// independently verifies every original child catalog remains unchanged.
func openGaussCreateDesignIncomingProbes(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest, parent contract.SchemaDesignSnapshot, incoming map[uint64]openGaussIncomingEvidence) (map[uint64]openGaussIncomingProbe, error) {
	out := map[uint64]openGaussIncomingProbe{}
	for oid, evidence := range incoming {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		target := r.Original.Target
		target.Object.Name = "__nyadb_gauss_in_" + hex.EncodeToString(nonce[:])
		absent, err := openGaussReadDesign(ctx, c, target)
		if err != nil || absent.Schema.Exists {
			return nil, errors.Join(err, schemaddl.ErrConflict)
		}
		table, names, err := schemaddl.OpenGaussIncomingProbeTable(evidence.Baseline, r.Original.Schema, r.Desired, target.Object.Name, parent.Target.Object.Name, evidence.Fields)
		if err != nil {
			return nil, err
		}
		if err = openGaussCheckDesignDeclaration(ctx, c, table, target.Object.Schema); err != nil {
			return nil, err
		}
		plan, err := schemaddl.PlanOpenGauss(absent.Schema, table, target.Object.Schema, true)
		if err != nil {
			return nil, err
		}
		for _, statement := range plan.SQL {
			if _, err = c.ExecContext(ctx, statement); err != nil {
				return nil, err
			}
		}
		child, err := openGaussReadDesign(ctx, c, target)
		if err != nil {
			return nil, err
		}
		actualParent, err := openGaussReadDesign(ctx, c, parent.Target)
		if err != nil {
			return nil, err
		}
		if err = openGaussCheckDesignReferenceCallbacks(ctx, c, child.Schema.OpenGauss, actualParent.Schema.OpenGauss); err != nil {
			return nil, err
		}
		out[oid] = openGaussIncomingProbe{Table: table.Clone(), Names: names, Catalog: child.Schema.OpenGauss.Clone()}
	}
	return out, ctx.Err()
}
