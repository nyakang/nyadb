package engines

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// The native instant-default decision depends on type, volatility and the
// evaluated datum's storage size. Observe ADD on the independent empty heap,
// rather than guessing from SQL spelling or granting every DEFAULT a rewrite.
// Neither actual result metadata nor original rows are used to create proof.
type openGaussDefaultRewriteProof struct {
	Request, Catalog string
	Columns          map[string]openGaussDefaultRewriteObservation
}

type openGaussDefaultRewriteObservation struct {
	Column                          string
	Relation, BeforeFile, AfterFile uint64
}

func openGaussDefaultRewriteRequest(original contract.SchemaDesignSnapshot, desired schemaddl.Table) string {
	return schemaddl.Signature(struct {
		Original contract.SchemaDesignSnapshot
		Desired  schemaddl.Table
	}{original, desired})
}

func openGaussDefaultRewriteColumns(original contract.SchemaDesignSnapshot, desired schemaddl.Table) ([]schemaddl.Clause, error) {
	if !original.Schema.Exists {
		return nil, nil
	}
	if original.Schema.OpenGauss == nil || original.Schema.Table.OpenGauss == nil {
		return nil, schemaddl.ErrConflict
	}
	ordinary := false
	for _, entry := range original.Schema.OpenGauss.Entries {
		if entry.Kind == "relation" && entry.OID == original.Schema.OpenGauss.RelationOID {
			var row struct {
				Kind, Part string
			}
			var data map[string]json.RawMessage
			if err := json.Unmarshal(entry.Data, &data); err != nil {
				return nil, err
			}
			if json.Unmarshal(data["relkind"], &row.Kind) != nil || json.Unmarshal(data["parttype"], &row.Part) != nil {
				return nil, schemaddl.ErrConflict
			}
			ordinary = row.Kind == "r" && row.Part == "n"
		}
	}
	for _, option := range original.Schema.Table.OpenGauss.Parameters {
		if strings.EqualFold(strings.ReplaceAll(option, " ", ""), "orientation=column") {
			ordinary = false
		}
	}
	if !ordinary {
		return nil, nil
	}
	old := map[string]bool{}
	for _, col := range original.Schema.Table.Columns() {
		old[col.ID] = true
	}
	var out []schemaddl.Clause
	for _, col := range desired.Columns() {
		if old[col.ID] {
			continue
		}
		f, err := schemaddl.ParseOpenGaussField(col.SQL)
		if err != nil {
			return nil, err
		}
		if f.HasDefault && !gaussDesignSerialType(f.Type) {
			out = append(out, col)
		}
	}
	return out, nil
}

func openGaussCaptureDefaultRewrite(ctx context.Context, c *sql.Conn, original contract.SchemaDesignSnapshot, desired schemaddl.Table, catalog *schemaddl.OpenGaussCatalog) (*openGaussDefaultRewriteProof, error) {
	columns, err := openGaussDefaultRewriteColumns(original, desired)
	if err != nil || len(columns) == 0 {
		return nil, err
	}
	if catalog == nil || !strings.HasPrefix(catalog.Name, "__nyadb_gauss_design_") || catalog.RelationOID == original.Schema.OpenGauss.RelationOID || catalog.NamespaceOID != original.Schema.OpenGauss.NamespaceOID || catalog.DatabaseOID != original.Schema.OpenGauss.DatabaseOID || catalog.RoleOID != original.Schema.OpenGauss.RoleOID || catalog.Hooks {
		return nil, schemaddl.ErrConflict
	}
	fingerprint, err := catalog.Fingerprint()
	if err != nil {
		return nil, err
	}
	proof := &openGaussDefaultRewriteProof{Request: openGaussDefaultRewriteRequest(original, desired), Catalog: fingerprint, Columns: map[string]openGaussDefaultRewriteObservation{}}
	for _, col := range columns {
		observation, err := openGaussProbeDefaultRewrite(ctx, c, catalog, col)
		if err != nil {
			return nil, err
		}
		if _, duplicate := proof.Columns[col.ID]; duplicate {
			return nil, schemaddl.ErrConflict
		}
		proof.Columns[col.ID] = observation
	}
	return proof, nil
}

func openGaussProbeDefaultRewrite(ctx context.Context, c *sql.Conn, catalog *schemaddl.OpenGaussCatalog, col schemaddl.Clause) (out openGaussDefaultRewriteObservation, err error) {
	f, err := schemaddl.ParseOpenGaussField(col.SQL)
	if err != nil || !f.HasDefault || f.Unsupported {
		return out, errors.Join(err, schemaddl.ErrConflict)
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return out, err
	}
	name := "__nyadb_rw_" + hex.EncodeToString(random[:])
	table := quote(catalog.Namespace) + "." + quote(catalog.Name)
	var oid, file uint64
	var rows int64
	read := func() error {
		if err := c.QueryRowContext(ctx, "SELECT oid::bigint,relfilenode::bigint FROM pg_class WHERE oid=$1::regclass", table).Scan(&oid, &file); err != nil {
			return err
		}
		if oid != catalog.RelationOID || file == 0 {
			return schemaddl.ErrConflict
		}
		if err := c.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&rows); err != nil {
			return err
		}
		if rows != 0 {
			return schemaddl.ErrConflict
		}
		return nil
	}
	if err = read(); err != nil {
		return out, err
	}
	out = openGaussDefaultRewriteObservation{Column: schemaddl.Signature(col), Relation: oid, BeforeFile: file}
	if _, err = c.ExecContext(ctx, "SAVEPOINT nyadb_gauss_default_rewrite"); err != nil {
		return out, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, rollbackErr := c.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT nyadb_gauss_default_rewrite")
		var releaseErr error
		if rollbackErr == nil {
			_, releaseErr = c.ExecContext(cleanup, "RELEASE SAVEPOINT nyadb_gauss_default_rewrite")
		}
		if rollbackErr != nil || releaseErr != nil {
			discardDesign(c)
			err = errors.Join(err, ErrUncertain, rollbackErr, releaseErr)
		}
		if err != nil {
			out = openGaussDefaultRewriteObservation{}
		}
	}()
	if _, err = c.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+quote(name)+" "+col.SQL); err != nil {
		return out, err
	}
	if err = read(); err != nil {
		return out, err
	}
	out.AfterFile = file
	return out, nil
}

func openGaussVerifiedDefaultRewrite(original contract.SchemaDesignSnapshot, desired schemaddl.Table, baseline openGaussDesignBaseline) (bool, error) {
	proof := baseline.DefaultRewrite
	if proof == nil {
		return false, nil
	}
	columns, err := openGaussDefaultRewriteColumns(original, desired)
	if err != nil || baseline.Catalog == nil || len(columns) != len(proof.Columns) || proof.Request != openGaussDefaultRewriteRequest(original, desired) {
		return false, errors.Join(err, schemaddl.ErrConflict)
	}
	fingerprint, err := baseline.Catalog.Fingerprint()
	if err != nil || fingerprint != proof.Catalog {
		return false, errors.Join(err, schemaddl.ErrConflict)
	}
	rewritten := false
	for _, col := range columns {
		observation, ok := proof.Columns[col.ID]
		if !ok || observation.Column != schemaddl.Signature(col) || observation.Relation != baseline.Catalog.RelationOID || observation.BeforeFile == 0 || observation.AfterFile == 0 {
			return false, schemaddl.ErrConflict
		}
		rewritten = rewritten || observation.BeforeFile != observation.AfterFile
	}
	return rewritten, nil
}
