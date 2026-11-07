package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

var errOpenGaussValueRepeatability = errors.New("native new default has no repeatable full-value proof")

type openGaussDesignValueProof struct {
	request                string
	rows                   int64
	ordinary, serials, all []string
	generated              bool
	digests                []postgresDesignDigest
	sequences              map[string]openGaussCopySequenceState
}

func gaussDesignSerialType(raw string) bool {
	switch strings.ToLower(raw) {
	case "serial", "serial4", "bigserial", "serial8", "smallserial", "serial2":
		return true
	}
	return false
}

func gaussDesignValueColumns(r contract.SchemaDesignRequest, canonical schemaddl.Table) (ordinary, serials, all []string, generated bool, wanted map[string]bool, err error) {
	wanted = map[string]bool{}
	old := map[string]bool{}
	for _, col := range r.Original.Schema.Table.Columns() {
		old[col.ID] = true
	}
	for _, col := range r.Desired.Columns() {
		f, e := schemaddl.ParseOpenGaussField(col.SQL)
		if e != nil {
			err = e
			return
		}
		if !old[col.ID] && gaussDesignSerialType(f.Type) {
			wanted[col.ID] = true
		}
	}
	for _, col := range canonical.Columns() {
		f, e := schemaddl.ParseOpenGaussField(col.SQL)
		if e != nil {
			err = e
			return
		}
		all = append(all, col.Name)
		if f.Generation != "" {
			generated = true
			continue
		}
		if wanted[col.ID] {
			serials = append(serials, col.Name)
		} else {
			ordinary = append(ordinary, col.Name)
		}
	}
	return
}

// Only new ordinary defaults will be evaluated during the before projection.
// Actual native dispatch, including implicit casts, must be repeatable. New
// owned serials have a separate sequence/value proof and never use this path.
func openGaussCheckValueRepeatability(ctx context.Context, c *sql.Conn, r contract.SchemaDesignRequest, baseline openGaussDesignBaseline, wanted map[string]bool) error {
	old := map[string]bool{}
	for _, col := range r.Original.Schema.Table.Columns() {
		old[col.ID] = true
	}
	fields := map[int]string{}
	for _, entry := range baseline.Catalog.Entries {
		if entry.Kind == "column" && entry.OID == baseline.Catalog.RelationOID {
			var v struct {
				Name string `json:"attname"`
			}
			if err := json.Unmarshal(entry.Data, &v); err != nil {
				return err
			}
			fields[entry.SubID] = baseline.Names.Columns[v.Name]
		}
	}
	defaults := map[string]bool{}
	for _, col := range baseline.Table.Columns() {
		f, err := schemaddl.ParseOpenGaussField(col.SQL)
		if err != nil {
			return err
		}
		if !old[col.ID] && !wanted[col.ID] && f.HasDefault && f.Generation == "" {
			defaults[col.ID] = true
		}
	}
	seen := map[string]bool{}
	for _, entry := range baseline.Catalog.Entries {
		if entry.Kind != "default" {
			continue
		}
		var d struct {
			Owner  uint64 `json:"adrelid"`
			Number int    `json:"adnum"`
			Tree   string `json:"adbin"`
		}
		if err := json.Unmarshal(entry.Data, &d); err != nil {
			return err
		}
		id := fields[d.Number]
		if d.Owner != baseline.Catalog.RelationOID || !defaults[id] {
			continue
		}
		if seen[id] {
			return schemaddl.ErrConflict
		}
		seen[id] = true
		calls, err := schemaddl.OpenGaussCopyExpressionRoutines(d.Tree)
		if err != nil {
			return errors.Join(errOpenGaussValueRepeatability, err)
		}
		for _, oid := range calls {
			var trusted bool
			err = c.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE p.oid=$1::oid AND n.nspname='pg_catalog' AND p.prolang IN(12,13) AND p.provolatile IN('i','s') AND p.proname<>'statement_timestamp' AND NOT p.proisagg AND NOT p.proiswindow)`, oid).Scan(&trusted)
			if err != nil || !trusted {
				return errors.Join(errOpenGaussValueRepeatability, err)
			}
		}
	}
	if len(seen) != len(defaults) {
		return schemaddl.ErrConflict
	}
	return ctx.Err()
}

func gaussDesignOwnedSequenceStates(ctx context.Context, c *sql.Conn, catalog *schemaddl.OpenGaussCatalog, names schemaddl.OpenGaussDesignNames, wanted map[string]bool) (map[string]openGaussCopySequenceState, error) {
	out := map[string]openGaussCopySequenceState{}
	if len(wanted) == 0 {
		return out, ctx.Err()
	}
	d, err := catalog.CopyDefinition()
	if err != nil {
		return nil, err
	}
	var selected schemaddl.OpenGaussCopyDefinition
	owners := map[uint64]string{}
	for _, seq := range d.Sequences {
		for _, col := range d.Columns {
			id := names.Columns[col.Name]
			if col.Number == seq.Column && wanted[id] {
				if owners[seq.OID] != "" {
					return nil, schemaddl.ErrConflict
				}
				owners[seq.OID] = id
				selected.Sequences = append(selected.Sequences, seq)
			}
		}
	}
	states, err := openGaussCopySequenceStates(ctx, c, selected)
	if err != nil {
		return nil, err
	}
	for oid, state := range states {
		id := owners[oid]
		if _, exists := out[id]; exists {
			return nil, schemaddl.ErrConflict
		}
		out[id] = state
	}
	if len(out) != len(wanted) {
		return nil, schemaddl.ErrConflict
	}
	return out, nil
}

func openGaussCaptureValueProof(ctx context.Context, c *sql.Conn, request contract.SchemaDesignRequest, baseline openGaussDesignBaseline, rows int64) (out openGaussDesignValueProof, err error) {
	r := request.Clone()
	if rows < 0 || baseline.Catalog == nil {
		return out, schemaddl.ErrConflict
	}
	out.request, out.rows = schemaddl.Signature(r), rows
	var wanted map[string]bool
	out.ordinary, out.serials, out.all, out.generated, wanted, err = gaussDesignValueColumns(r, baseline.Table)
	if err != nil {
		return out, err
	}
	out.sequences = map[string]openGaussCopySequenceState{}
	d, err := baseline.Catalog.CopyDefinition()
	if err != nil {
		return out, err
	}
	for _, seq := range d.Sequences {
		for _, col := range d.Columns {
			id := baseline.Names.Columns[col.Name]
			if col.Number == seq.Column && wanted[id] {
				out.sequences[id] = openGaussCopySequenceState{Last: seq.Start}
			}
		}
	}
	if len(out.sequences) != len(wanted) {
		return out, schemaddl.ErrConflict
	}
	if rows == 0 {
		return out, ctx.Err()
	}
	if err = openGaussCheckValueRepeatability(ctx, c, r, baseline, wanted); err != nil {
		return out, err
	}
	groups := [][]string{out.ordinary}
	for _, name := range out.serials {
		groups = append(groups, []string{name})
	}
	out.digests, out.sequences, err = openGaussProjectDesignValues(ctx, c, r.Original.Target, r.Original.Schema.Table, r.Desired, baseline.Table, rows, groups, wanted)
	return out, err
}

func openGaussVerifyValueProof(ctx context.Context, c *sql.Conn, request contract.SchemaDesignRequest, baseline openGaussDesignBaseline, actual contract.SchemaDesignSnapshot, proof openGaussDesignValueProof) error {
	r := request.Clone()
	target := r.Original.Target
	target.Object.Name = baseline.Table.Name
	if proof.request != schemaddl.Signature(r) || actual.Target != target || actual.Schema.OpenGauss == nil {
		return schemaddl.ErrConflict
	}
	wanted := map[string]bool{}
	for id := range proof.sequences {
		wanted[id] = true
	}
	states, err := gaussDesignOwnedSequenceStates(ctx, c, actual.Schema.OpenGauss, schemaddl.OpenGaussNamesForTable(baseline.Table), wanted)
	if err != nil {
		return err
	}
	if schemaddl.Signature(states) != schemaddl.Signature(proof.sequences) {
		return schemaddl.ErrDataChanged
	}
	groups := [][]string{proof.ordinary}
	for _, name := range proof.serials {
		groups = append(groups, []string{name})
	}
	for i, names := range groups {
		var expressions []string
		for _, name := range names {
			expressions = append(expressions, quote(name))
		}
		digest, err := openGaussBinaryDigest(ctx, c, actual.Target.Object, expressions)
		if err != nil {
			return err
		}
		if digest.Rows != proof.rows || proof.rows > 0 && (i >= len(proof.digests) || digest != proof.digests[i]) {
			return schemaddl.ErrDataChanged
		}
	}
	if !proof.generated || proof.rows == 0 {
		return ctx.Err()
	}
	digests, _, err := openGaussProjectDesignValues(ctx, c, actual.Target, baseline.Table, r.Desired, baseline.Table, proof.rows, [][]string{proof.all}, nil)
	if err != nil {
		return err
	}
	var expressions []string
	for _, name := range proof.all {
		expressions = append(expressions, quote(name))
	}
	actualDigest, err := openGaussBinaryDigest(ctx, c, actual.Target.Object, expressions)
	if err != nil {
		return err
	}
	if len(digests) != 1 || actualDigest != digests[0] {
		return schemaddl.ErrDataChanged
	}
	return ctx.Err()
}
