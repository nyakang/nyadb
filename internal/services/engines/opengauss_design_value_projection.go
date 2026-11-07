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

// Project full rows into an independent native heap table. Constraints,
// triggers, rules and indexes are omitted; ordinary inputs are supplied by
// stable declaration ID, while defaults/serials and generated fields remain
// independent native expressions. Source values never become SQL text.
func openGaussProjectDesignValues(ctx context.Context, c *sql.Conn, target contract.SchemaDesignTarget, source, requested, canonical schemaddl.Table, rows int64, groups [][]string, wanted map[string]bool) (out []postgresDesignDigest, states map[string]openGaussCopySequenceState, err error) {
	if rows < 0 || source.Name != target.Object.Name || canonical.OpenGauss == nil {
		return nil, nil, schemaddl.ErrConflict
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, nil, err
	}
	name := "__nyadb_gauss_values_" + hex.EncodeToString(random[:])
	shadow := schemaddl.Table{Name: name, OpenGauss: &schemaddl.OpenGaussTableProperties{Persistence: "p"}}
	requestedFields := map[string]schemaddl.Clause{}
	for _, col := range requested.Columns() {
		requestedFields[col.ID] = col
	}
	for _, col := range canonical.Columns() {
		f, e := schemaddl.ParseOpenGaussField(col.SQL)
		if e != nil || f.Unsupported {
			return nil, nil, errors.Join(e, schemaddl.ErrUnsupported)
		}
		raw, e := schemaddl.ParseOpenGaussField(requestedFields[col.ID].SQL)
		if e != nil {
			return nil, nil, e
		}
		if gaussDesignSerialType(raw.Type) {
			// The declaration prototype's serial was rolled back. CREATE must
			// allocate a fresh owned sequence instead of using that private name.
			f.Type, f.HasDefault, f.Default = raw.Type, false, ""
		}
		f.NotNull = false
		col.OpenGauss, col.Postgres = nil, nil
		col.SQL, err = f.SQL()
		if err != nil {
			return nil, nil, err
		}
		shadow.Clauses = append(shadow.Clauses, col)
	}
	// Include original input types in the cast closure, without compiling old
	// defaults or generated expressions merely to read their stored values.
	casts := shadow.Clone()
	for i, col := range source.Columns() {
		f, e := schemaddl.ParseOpenGaussField(col.SQL)
		if e != nil {
			return nil, nil, e
		}
		f.HasDefault, f.Default, f.Generation, f.GenerationStorage = false, "", "", ""
		body, e := f.SQL()
		if e != nil {
			return nil, nil, e
		}
		casts.Clauses = append(casts.Clauses, schemaddl.Clause{ID: fmt.Sprintf("input:%d", i), Kind: "column", Name: fmt.Sprintf("%s_input_%d", name, i), SQL: body})
	}
	if err = openGaussCheckDesignDeclaration(ctx, c, casts, target.Object.Schema); err != nil {
		return nil, nil, err
	}
	shadowTarget := target
	shadowTarget.Object.Name = name
	missing, err := openGaussReadDesign(ctx, c, shadowTarget)
	if err != nil || missing.Schema.Exists {
		return nil, nil, errors.Join(err, schemaddl.ErrConflict)
	}
	plan, err := schemaddl.PlanOpenGauss(missing.Schema, shadow, target.Object.Schema, false)
	if err != nil {
		return nil, nil, err
	}
	if _, err = c.ExecContext(ctx, "SAVEPOINT nyadb_gauss_value_projection"); err != nil {
		return nil, nil, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_, rb := c.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT nyadb_gauss_value_projection")
		var release error
		if rb == nil {
			_, release = c.ExecContext(cleanup, "RELEASE SAVEPOINT nyadb_gauss_value_projection")
		}
		if rb != nil || release != nil {
			discardDesign(c)
			err = errors.Join(err, ErrUncertain, rb, release)
		}
		if err != nil {
			out, states = nil, nil
		}
	}()
	for _, q := range plan.SQL {
		if _, err = c.ExecContext(ctx, q); err != nil {
			return nil, nil, err
		}
	}
	captured, err := openGaussReadDesign(ctx, c, shadowTarget)
	if err != nil {
		return nil, nil, err
	}
	if err = openGaussCopyPrograms(ctx, c, captured.Schema.OpenGauss); err != nil {
		return nil, nil, err
	}
	parsed := captured.Schema.Table.Clone()
	shadowNames := schemaddl.OpenGaussNamesForTable(shadow)
	for i, col := range parsed.Clauses {
		parsed.Clauses[i].ID = shadowNames.Columns[col.Name]
	}
	projectionBaseline := openGaussDesignBaseline{Table: parsed, Catalog: captured.Schema.OpenGauss, Names: shadowNames}
	projectionRequest := contract.SchemaDesignRequest{Original: contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Table: source}}}
	if err = openGaussCheckValueRepeatability(ctx, c, projectionRequest, projectionBaseline, wanted); err != nil {
		return nil, nil, err
	}
	byID := map[string]schemaddl.Clause{}
	for _, col := range source.Columns() {
		byID[col.ID] = col
	}
	var destinations, inputs []string
	for _, col := range canonical.Columns() {
		prior, retained := byID[col.ID]
		if !retained {
			continue
		}
		f, e := schemaddl.ParseOpenGaussField(col.SQL)
		if e != nil {
			return nil, nil, e
		}
		if f.Generation != "" {
			continue
		}
		destinations = append(destinations, quote(col.Name))
		inputs = append(inputs, "CAST("+quote(prior.Name)+" AS "+f.Type+")")
	}
	if len(inputs) > 0 {
		q := "INSERT INTO " + qualified(shadowTarget.Object) + " (" + strings.Join(destinations, ",") + ") SELECT " + strings.Join(inputs, ",") + " FROM " + qualified(target.Object)
		result, e := c.ExecContext(ctx, q)
		if e != nil {
			return nil, nil, e
		}
		n, e := result.RowsAffected()
		if e != nil || n != rows {
			return nil, nil, errors.Join(e, schemaddl.ErrDataChanged)
		}
	} else {
		values := make([]string, len(shadow.Columns()))
		for i := range values {
			values[i] = "DEFAULT"
		}
		value := "(" + strings.Join(values, ",") + ")"
		batch := min(128, (1<<20)/len(value))
		if batch < 1 {
			return nil, nil, schemaddl.ErrUnsupported
		}
		for remaining := rows; remaining > 0; {
			n := min(int64(batch), remaining)
			parts := make([]string, int(n))
			for i := range parts {
				parts[i] = value
			}
			result, e := c.ExecContext(ctx, "INSERT INTO "+qualified(shadowTarget.Object)+" VALUES "+strings.Join(parts, ","))
			if e != nil {
				return nil, nil, e
			}
			inserted, e := result.RowsAffected()
			if e != nil || inserted != n {
				return nil, nil, errors.Join(e, schemaddl.ErrDataChanged)
			}
			remaining -= n
		}
	}
	captured, err = openGaussReadDesign(ctx, c, shadowTarget)
	if err != nil {
		return nil, nil, err
	}
	if err = openGaussCopyPrograms(ctx, c, captured.Schema.OpenGauss); err != nil {
		return nil, nil, err
	}
	for _, columns := range groups {
		var names []string
		for _, column := range columns {
			names = append(names, quote(column))
		}
		digest, e := openGaussBinaryDigest(ctx, c, shadowTarget.Object, names)
		if e != nil || digest.Rows != rows {
			return nil, nil, errors.Join(e, schemaddl.ErrDataChanged)
		}
		out = append(out, digest)
	}
	states, err = gaussDesignOwnedSequenceStates(ctx, c, captured.Schema.OpenGauss, schemaddl.OpenGaussNamesForTable(shadow), wanted)
	return out, states, err
}
