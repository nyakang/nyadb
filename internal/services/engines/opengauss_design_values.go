package engines

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// Reverse projections compare complete retained values in their original
// native types. A server-accepted varchar truncation or numeric rounding must
// fail this check, including NULLs, repeated rows and values beyond previews.
// Newly requested fields need a separate independent after-value proof.
func openGaussRetainedDesignDigest(ctx context.Context, c *sql.Conn, o contract.ObjectRef, original, desired schemaddl.Table, after bool) (postgresDesignDigest, error) {
	next := map[string]schemaddl.Clause{}
	for _, col := range desired.Columns() {
		next[col.ID] = col
	}
	var expressions []string
	for _, old := range original.Columns() {
		col, retained := next[old.ID]
		if !retained {
			continue
		}
		name := old.Name
		if after {
			name = col.Name
		}
		f, err := schemaddl.ParseOpenGaussField(old.SQL)
		if err != nil || f.Unsupported {
			return postgresDesignDigest{}, errors.Join(err, schemaddl.ErrUnsupported)
		}
		// Always cast both baselines, preserving declared typmods and dimensions
		// independently of the changed field's physical storage layout.
		expressions = append(expressions, "CAST("+quote(name)+" AS "+f.Type+")")
	}
	return openGaussBinaryDigest(ctx, c, o, expressions)
}
