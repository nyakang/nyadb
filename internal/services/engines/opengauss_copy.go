package engines

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func (s *Session) openGaussCopyTable(ctx context.Context, request contract.SchemaCopyRequest) (out contract.SchemaDesignOutcome) {
	out.State = "not_executed"
	r := request.Clone()
	t := r.Original.Target
	if err := s.checkOpenGaussTable(t); err != nil {
		out.Error = err
		return
	}
	if r.Original.Schema.OpenGauss == nil || r.Original.Schema.OpenGauss.Database != t.Object.Catalog || r.Original.Schema.OpenGauss.Namespace != t.Object.Schema || r.Original.Schema.Table.Name != t.Object.Name || r.PreserveRowID {
		out.Error = schemaddl.ErrUnsupported
		return
	}
	plan, err := schemaddl.PlanOpenGaussCopy(r.Original.Schema, r.Destination, r.WithData, r.RebindSelfReferences)
	if err != nil {
		out.Error = err
		return
	}
	destination := t
	destination.Object.Name = r.Destination
	if err = s.checkOpenGaussTable(destination); err != nil {
		out.Error = err
		return
	}
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	c, err := s.db.Conn(ctx)
	if err != nil {
		out.Error = err
		return
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN"); err != nil {
		discardDesign(c)
		out.Error = errors.Join(err, ctx.Err())
		return
	}
	settled, commit := false, false
	defer func() {
		if !settled {
			out.Error = errors.Join(out.Error, ctx.Err())
			rb := rollbackDesign(c)
			if commit || rb != nil {
				discardDesign(c)
				out.State = "uncertain"
				out.Error = errors.Join(out.Error, rb, s.Close())
			} else {
				out.State = "rolled_back"
			}
		}
	}()
	if _, err = c.ExecContext(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err == nil {
		_, err = c.ExecContext(ctx, "LOCK TABLE "+qualified(t.Object)+" IN SHARE MODE")
	}
	if err != nil {
		out.Error = err
		return
	}
	before, err := openGaussReadTable(ctx, c, t)
	if err != nil || !before.Schema.Exists || before.Schema.Revision != r.Original.Schema.Revision {
		out.Error = errors.Join(err, schemaddl.ErrConflict)
		return
	}
	// New READ COMMITTED statements follow lock acquisition; a stale catalog
	// captured while another transaction held the lock is never accepted.
	absent, err := openGaussReadTable(ctx, c, destination)
	if err != nil || absent.Schema.Exists {
		out.Error = errors.Join(err, errors.New("copy destination already exists; no object will be overwritten"))
		return
	}
	if err = openGaussCopyPrograms(ctx, c, before.Schema.OpenGauss); err != nil {
		out.Error = err
		return
	}
	definition, err := before.Schema.OpenGauss.CopyDefinition()
	if err != nil {
		out.Error = err
		return
	}
	var originalDigest postgresDesignDigest
	if r.WithData {
		originalDigest, err = openGaussCopyDigest(ctx, c, t.Object, definition)
		if err != nil {
			out.Error = err
			return
		}
	}
	states, err := openGaussCopySequenceStates(ctx, c, definition)
	if err != nil {
		out.Error = err
		return
	}
	for _, q := range plan.Before {
		if _, err = c.ExecContext(ctx, q); err != nil {
			out.Error = err
			return
		}
	}
	if plan.DataSQL != "" {
		result, err := c.ExecContext(ctx, plan.DataSQL)
		if err != nil {
			out.Error = err
			return
		}
		out.Rows, err = result.RowsAffected()
		if err != nil || out.Rows != originalDigest.Rows {
			out.Error = errors.Join(err, schemaddl.ErrConflict)
			return
		}
	}
	// Only new sequences are advanced. Source sequence values are captured once
	// and checked again; a concurrent nextval is a conflict, never overwritten.
	if r.WithData {
		for _, seq := range definition.Sequences {
			state := states[seq.OID]
			name := quote(t.Object.Schema) + "." + quote(plan.SequenceNames[seq.OID])
			if _, err = c.ExecContext(ctx, "SELECT pg_catalog.setval($1::regclass,$2::numeric,"+fmt.Sprint(state.Called)+")", name, state.Last); err != nil {
				out.Error = err
				return
			}
		}
	}
	for _, q := range plan.After {
		if _, err = c.ExecContext(ctx, q); err != nil {
			out.Error = err
			return
		}
	}
	actual, err := openGaussReadTable(ctx, c, destination)
	if err != nil || !actual.Schema.Exists {
		out.Error = errors.Join(err, schemaddl.ErrConflict)
		return
	}
	want, err := schemaddl.OpenGaussCopyShape(before.Schema, r.Destination, r.RebindSelfReferences)
	if err != nil {
		out.Error = err
		return
	}
	got, err := schemaddl.OpenGaussCopyShape(actual.Schema, "", false)
	if err != nil || want != got {
		out.Error = errors.Join(err, errors.New("native copied structure differs from the original blueprint"))
		return
	}
	actualDefinition, err := actual.Schema.OpenGauss.CopyDefinition()
	if err != nil {
		out.Error = err
		return
	}
	if r.WithData {
		digest, err := openGaussCopyDigest(ctx, c, destination.Object, actualDefinition)
		if err != nil || digest != originalDigest {
			out.Error = errors.Join(err, errors.New("complete native copied values failed verification"))
			return
		}
	} else {
		var count int64
		if err = c.QueryRowContext(ctx, "SELECT count(*) FROM "+qualified(destination.Object)).Scan(&count); err != nil || count != 0 {
			out.Error = errors.Join(err, schemaddl.ErrConflict)
			return
		}
	}
	afterStates, err := openGaussCopySequenceStates(ctx, c, definition)
	if err != nil || schemaddl.Signature(states) != schemaddl.Signature(afterStates) {
		out.Error = errors.Join(err, errors.New("source sequence changed during copying"))
		return
	}
	newStates, err := openGaussCopySequenceStates(ctx, c, actualDefinition)
	if err != nil {
		out.Error = err
		return
	}
	for _, seq := range definition.Sequences {
		matched := false
		columnName := ""
		for _, col := range definition.Columns {
			if col.Number == seq.Column {
				columnName = col.Name
			}
		}
		for _, next := range actualDefinition.Sequences {
			for _, col := range actualDefinition.Columns {
				if col.Name != columnName || col.Number != next.Column {
					continue
				}
				matched = true
				want := states[seq.OID]
				if !r.WithData {
					want = openGaussCopySequenceState{Last: seq.Start}
				}
				if newStates[next.OID] != want {
					out.Error = errors.New("copied sequence state failed verification")
					return
				}
			}
		}
		if !matched {
			out.Error = schemaddl.ErrConflict
			return
		}
	}
	unchanged, err := openGaussReadTable(ctx, c, t)
	if err != nil || !openGaussCopySourcePreserved(before.Schema.OpenGauss, unchanged.Schema.OpenGauss, actual.Schema.OpenGauss) {
		out.Error = errors.Join(err, errors.New("source catalog changed during copying"))
		return
	}
	if err = s.checkOpenGaussTable(t); err != nil {
		out.Error = err
		return
	}
	commit = true
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		out.Error = err
		return
	}
	settled = true
	out.State, out.Verified, out.Actual = "committed", true, &actual
	return
}

// Only dependency edges owned by the verified destination may be added to the
// original. Retained self-FKs deliberately point at the original table. Any
// other source definition, grant, dependency or unknown property must match.
func openGaussCopySourcePreserved(before, after, destination *schemaddl.OpenGaussCatalog) bool {
	if before == nil || after == nil || destination == nil {
		return false
	}
	owned := map[uint64]bool{destination.RelationOID: true}
	constraints := map[uint64]bool{}
	for _, e := range destination.Entries {
		switch e.Kind {
		case "relation", "type", "partition", "constraint", "trigger", "rule", "default", "policy", "index":
			owned[e.OID] = true
		}
		if e.Kind == "constraint" {
			constraints[e.OID] = true
		}
	}
	addedTriggers := map[uint64]bool{}
	for _, e := range after.Entries {
		if e.Kind == "trigger" {
			var tr struct {
				Internal   bool   `json:"tgisinternal"`
				Constraint uint64 `json:"tgconstraint"`
			}
			if json.Unmarshal(e.Data, &tr) != nil {
				return false
			}
			if tr.Internal && constraints[tr.Constraint] {
				addedTriggers[e.OID] = true
				owned[e.OID] = true
			}
		}
	}
	x := after.Clone()
	var entries []schemaddl.OpenGaussCatalogEntry
	for _, e := range x.Entries {
		if e.Kind == "incoming_constraint" && owned[e.OID] {
			continue
		}
		if e.Kind == "trigger" && addedTriggers[e.OID] {
			continue
		}
		if e.Kind == "copy_definition" && len(addedTriggers) > 0 {
			var data map[string]json.RawMessage
			if json.Unmarshal(e.Data, &data) != nil {
				return false
			}
			var rows []json.RawMessage
			if json.Unmarshal(data["triggers"], &rows) != nil {
				return false
			}
			var keep []json.RawMessage
			for _, raw := range rows {
				var tr struct{ OID uint64 }
				if json.Unmarshal(raw, &tr) != nil {
					return false
				}
				if !addedTriggers[tr.OID] {
					keep = append(keep, raw)
				}
			}
			data["triggers"], _ = json.Marshal(keep)
			e.Data, _ = json.Marshal(data)
		}
		if e.Kind == "relation" && e.OID == before.RelationOID && len(addedTriggers) > 0 {
			var original json.RawMessage
			for _, old := range before.Entries {
				if old.Kind == "relation" && old.OID == before.RelationOID {
					original = old.Data
				}
			}
			var a, b map[string]json.RawMessage
			if json.Unmarshal(original, &a) != nil || json.Unmarshal(e.Data, &b) != nil {
				return false
			}
			b["relhastriggers"] = a["relhastriggers"]
			e.Data, _ = json.Marshal(b)
		}
		if e.Kind == "dependencies" {
			var data map[string]json.RawMessage
			if json.Unmarshal(e.Data, &data) != nil {
				return false
			}
			var rows []json.RawMessage
			if json.Unmarshal(data["rows"], &rows) != nil {
				return false
			}
			var keep []json.RawMessage
			for _, raw := range rows {
				var d struct {
					Object uint64 `json:"objid"`
				}
				if json.Unmarshal(raw, &d) != nil {
					return false
				}
				if !owned[d.Object] {
					keep = append(keep, raw)
				}
			}
			data["rows"], _ = json.Marshal(keep)
			e.Data, _ = json.Marshal(data)
		}
		entries = append(entries, e)
	}
	x.Entries = entries
	a, err := before.Fingerprint()
	if err != nil {
		return false
	}
	b, err := x.Fingerprint()
	return err == nil && a == b
}

type openGaussCopySequenceState struct {
	Last   string
	Called bool
}

func openGaussCopySequenceStates(ctx context.Context, c *sql.Conn, d schemaddl.OpenGaussCopyDefinition) (map[uint64]openGaussCopySequenceState, error) {
	out := map[uint64]openGaussCopySequenceState{}
	for _, s := range d.Sequences {
		var oid uint64
		var v openGaussCopySequenceState
		if err := c.QueryRowContext(ctx, "SELECT last_value::text,is_called,$1::regclass::oid::bigint FROM "+quote(s.Schema)+"."+quote(s.Name), quote(s.Schema)+"."+quote(s.Name)).Scan(&v.Last, &v.Called, &oid); err != nil {
			return nil, err
		}
		if oid != s.OID {
			return nil, schemaddl.ErrConflict
		}
		out[s.OID] = v
	}
	return out, nil
}

// Validate executable expressions, operator implementations and binary send
// functions before any copy DDL. Trigger/rule bodies are installed only after
// insertion; arbitrary user code is never evaluated as a verification probe.
func openGaussCopyPrograms(ctx context.Context, c *sql.Conn, catalog *schemaddl.OpenGaussCatalog) error {
	var ids []string
	seen := map[int64]bool{}
	for _, e := range catalog.Entries {
		var raw map[string]json.RawMessage
		if json.Unmarshal(e.Data, &raw) != nil {
			return schemaddl.ErrConflict
		}
		var expressions []string
		switch e.Kind {
		case "default":
			var generated string
			if json.Unmarshal(raw["adgencol"], &generated) != nil {
				return schemaddl.ErrConflict
			}
			if generated != "s" {
				continue
			}
			expressions = []string{"adbin"}
		case "constraint":
			expressions = []string{"conbin"}
		case "index":
			expressions = []string{"indexprs", "indpred"}
		default:
			continue
		}
		for _, key := range expressions {
			var node string
			if json.Unmarshal(raw[key], &node) != nil {
				return schemaddl.ErrConflict
			}
			if node == "" {
				continue
			}
			calls, err := schemaddl.OpenGaussCopyExpressionRoutines(node)
			if err != nil {
				return errors.New("native copy expression has unsupported dispatch")
			}
			for _, id := range calls {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, strconv.FormatInt(id, 10))
				}
			}
		}
	}
	if len(ids) > 0 {
		var count int
		if err := c.QueryRowContext(ctx, "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE p.oid IN("+strings.Join(ids, ",")+") AND n.nspname='pg_catalog' AND p.provolatile='i' AND p.prolang IN(12,13)").Scan(&count); err != nil {
			return err
		}
		if count != len(ids) {
			return errors.New("native copy expressions require immutable built-in implementations")
		}
	}
	var unsafe bool
	err := c.QueryRowContext(ctx, `WITH RECURSIVE value_types(oid) AS (
 SELECT atttypid FROM pg_attribute WHERE attrelid=$1::oid AND attnum>0 AND NOT attisdropped
 UNION SELECT sub.oid FROM value_types v JOIN pg_type t ON t.oid=v.oid JOIN pg_type sub ON sub.oid=t.typelem
 ), objects(classid,objid) AS (
 SELECT 'pg_attrdef'::regclass,d.oid FROM pg_attrdef d WHERE d.adrelid=$1::oid AND d.adgencol='s'
 UNION ALL SELECT 'pg_constraint'::regclass,oid FROM pg_constraint WHERE conrelid=$1::oid
 UNION ALL SELECT 'pg_class'::regclass,indexrelid FROM pg_index WHERE indrelid=$1::oid
 ), routines(oid) AS (
 SELECT d.refobjid FROM pg_depend d JOIN objects o ON o.classid=d.classid AND o.objid=d.objid WHERE d.refclassid='pg_proc'::regclass
 UNION SELECT op.oprcode::oid FROM pg_depend d JOIN objects o ON o.classid=d.classid AND o.objid=d.objid JOIN pg_operator op ON op.oid=d.refobjid WHERE d.refclassid='pg_operator'::regclass
 ) SELECT EXISTS(SELECT 1 FROM routines r JOIN pg_proc p ON p.oid=r.oid JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname<>'pg_catalog' OR p.provolatile<>'i')
 OR EXISTS(SELECT 1 FROM value_types v JOIN pg_type t ON t.oid=v.oid LEFT JOIN pg_proc p ON p.oid=t.typsend LEFT JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE p.oid IS NULL OR n.nspname<>'pg_catalog' OR p.prolang NOT IN(12,13) OR t.typtype NOT IN('b','e'))
 OR EXISTS(SELECT 1 FROM pg_index i JOIN pg_opclass oc ON oc.oid=ANY(i.indclass) JOIN pg_namespace n ON n.oid=oc.opcnamespace WHERE i.indrelid=$1::oid AND n.nspname<>'pg_catalog')`, catalog.RelationOID).Scan(&unsafe)
	if err != nil {
		return err
	}
	if unsafe {
		return errors.New("copy expressions and value types require verified native built-in implementations")
	}
	return nil
}

// Stream complete binary native rows, including generated fields, null tags,
// type IDs and duplicate counts. No text conversion, collation or preview cap
// participates in the comparison. Each verification row is bounded to 16 MiB.
func openGaussCopyDigest(ctx context.Context, c *sql.Conn, o contract.ObjectRef, d schemaddl.OpenGaussCopyDefinition) (out postgresDesignDigest, err error) {
	var names []string
	for _, col := range d.Columns {
		names = append(names, quote(col.Name))
	}
	return openGaussBinaryDigest(ctx, c, o, names)
}

// Callers supply quoted columns or previously validated native projections.
// Copy and table design share one complete binary multiset implementation.
func openGaussBinaryDigest(ctx context.Context, c *sql.Conn, o contract.ObjectRef, names []string) (out postgresDesignDigest, err error) {
	q := "SELECT CASE WHEN octet_length(v.payload)<=16777216 THEN v.payload END FROM (SELECT record_send(ROW(" + strings.Join(names, ",") + ")) AS payload FROM " + qualified(o) + ") v"
	rows, err := c.QueryContext(ctx, q)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		if raw == nil || len(raw) > 16<<20 {
			return out, errors.New("native verification row exceeds 16 MiB")
		}
		sum := sha256.Sum256(raw)
		carry := 0
		for i := len(sum) - 1; i >= 0; i-- {
			n := int(out.Sum[i]) + int(sum[i]) + carry
			out.Sum[i], carry = byte(n), n>>8
		}
		out.Rows++
	}
	return out, rows.Err()
}
