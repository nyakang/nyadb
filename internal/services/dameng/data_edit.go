package dameng

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func editableTableAt(ctx context.Context, q metadataQueryer, t *contract.TableStructure) ([]string, error) {
	if t.Object.Kind != "table" || len(t.Columns) > 512 {
		return nil, contract.ErrDataReadOnly
	}
	if _, err := dataProjection(t, 1024); err != nil {
		return nil, err
	}
	var status, partitioned, temporary, locking, longRow string
	err := q.QueryRowContext(ctx, `SELECT STATUS,PARTITIONED,TEMPORARY,TABLE_LOCK,LONG_ROW FROM SYS.ALL_TABLES WHERE OWNER=? AND TABLE_NAME=?`, t.Object.Schema, t.Object.Name).Scan(&status, &partitioned, &temporary, &locking, &longRow)
	if err != nil {
		return nil, err
	}
	if status != "VALID" || partitioned != "NO" || temporary != "N" || locking != "ENABLED" || longRow != "DISABLED" {
		return nil, contract.ErrDataReadOnly
	}
	for _, c := range t.Columns {
		if c.DataType == "CLOB" || c.DataType == "TEXT" || c.DataType == "BLOB" || c.DataType == "IMAGE" {
			// On this native kernel a trigger's locator WRITEAPPEND survives
			// ROLLBACK, even while the scalar UPDATE rolls back. Do not infer
			// rollback safety from trigger source or a successful driver receipt.
			var triggers int64
			if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM SYS.ALL_TRIGGERS WHERE TABLE_OWNER=? AND TABLE_NAME=?`, t.Object.Schema, t.Object.Name).Scan(&triggers); err != nil {
				return nil, err
			}
			if triggers != 0 {
				return nil, contract.ErrDataReadOnly
			}
			break
		}
	}
	key, err := primaryKeyAt(ctx, q, t)
	if err == nil && len(key) == 0 {
		err = contract.ErrDataReadOnly
	}
	return key, err
}

func editAssignments(t *contract.TableStructure, e contract.DataEdit) ([]string, []string, []any, error) {
	cols := dataColumns(t)
	for name, v := range e.Values {
		c, ok := cols[name]
		if !ok {
			return nil, nil, nil, connections.ErrInvalid
		}
		if c.Generated && !(e.Kind == "insert" && c.Extra == "IDENTITY" && defaultValueValid(v)) {
			return nil, nil, nil, connections.ErrInvalid
		}
		if v.Default && !defaultValueValid(v) {
			return nil, nil, nil, connections.ErrInvalid
		}
	}
	var names, expressions []string
	var args []any
	for _, c := range t.Columns {
		v, ok := e.Values[c.Name]
		if c.Generated {
			continue
		}
		if !ok {
			if e.Kind == "insert" && !c.Nullable && c.Default == nil {
				return nil, nil, nil, connections.ErrInvalid
			}
			continue
		}
		names = append(names, quote(c.Name))
		if v.Default {
			expressions = append(expressions, "DEFAULT")
			continue
		}
		p, err := editParameter(c, v)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("DM8 edit column %s: %w", c.Name, err)
		}
		expressions = append(expressions, "?")
		args = append(args, p)
	}
	if e.Kind == "insert" && len(names) == 0 {
		// DM8 rejects DEFAULT VALUES and explicit DEFAULT on an IDENTITY.
		// Resolve a native empty insert through a writable default/nullable column.
		for _, c := range t.Columns {
			if !c.Generated && (c.Default != nil || c.Nullable) {
				names = []string{quote(c.Name)}
				expressions = []string{"DEFAULT"}
				break
			}
		}
	}
	if e.Kind != "delete" && len(names) == 0 {
		return nil, nil, nil, contract.ErrUnsupported
	}
	return names, expressions, args, nil
}

func validateEditBatch(ctx context.Context, s *Session, q metadataQueryer, t *contract.TableStructure, key []string, identity string, edits []contract.DataEdit) error {
	seen := map[string]bool{}
	for _, e := range edits {
		switch e.Kind {
		case "insert":
			if e.Original != nil {
				return connections.ErrInvalid
			}
		case "update", "delete":
			if err := validateDataSnapshot(s, t, key, identity, e.Original); err != nil {
				return err
			}
			refKey := snapshotRefKey(e.Original)
			if seen[refKey] {
				return connections.ErrInvalid
			}
			seen[refKey] = true
			row, err := readFullRowAt(ctx, q, t, key, e.Original.Ref)
			if err != nil {
				return err
			}
			if !sameDataRow(row, e.Original.Original) {
				return contract.ErrDataConflict
			}
			if e.Kind == "delete" && len(e.Values) != 0 {
				return connections.ErrInvalid
			}
		default:
			return connections.ErrInvalid
		}
		if e.Kind != "delete" {
			if _, _, _, err := editAssignments(t, e); err != nil {
				return err
			}
		}
	}
	return nil
}

func returnedDataRef(t *contract.TableStructure, key []string, values []sql.NullString) (contract.RowRef, error) {
	ref := contract.RowRef{Object: t.Object, Database: t.Object.Schema, Table: t.Object.Name, Columns: append([]string(nil), key...)}
	cols := dataColumns(t)
	for i, k := range key {
		if !values[i].Valid {
			return ref, contract.ErrDataConflict
		}
		v := contract.Cell{Kind: cols[k].ValueKind, Text: values[i].String}
		if _, err := dataParameter(v, v.Kind); err != nil {
			return ref, err
		}
		b, _ := json.Marshal(v)
		ref.TypedValues = append(ref.TypedValues, v)
		ref.Values = append(ref.Values, b)
	}
	return ref, nil
}

func executeDataEdit(ctx context.Context, c *sql.Conn, t *contract.TableStructure, key []string, e contract.DataEdit) (contract.RowRef, error) {
	var text string
	var args []any
	if e.Kind == "delete" {
		where, bound, err := rowWhere(t, key, e.Original.Ref)
		if err != nil {
			return contract.RowRef{}, err
		}
		text = "DELETE FROM " + qualified(t.Object) + " WHERE " + where
		args = bound
	} else {
		names, expressions, bound, err := editAssignments(t, e)
		if err != nil {
			return contract.RowRef{}, err
		}
		args = bound
		if e.Kind == "insert" {
			text = "INSERT INTO " + qualified(t.Object) + " (" + strings.Join(names, ",") + ") VALUES (" + strings.Join(expressions, ",") + ")"
		} else {
			var terms []string
			for i, n := range names {
				terms = append(terms, n+"="+expressions[i])
			}
			where, bound, err := rowWhere(t, key, e.Original.Ref)
			if err != nil {
				return contract.RowRef{}, err
			}
			text = "UPDATE " + qualified(t.Object) + " SET " + strings.Join(terms, ",") + " WHERE " + where
			args = append(args, bound...)
		}
	}
	values := make([]sql.NullString, len(key))
	if e.Kind != "delete" {
		var names, outputs []string
		for i, k := range key {
			names = append(names, quote(k))
			outputs = append(outputs, "?")
			args = append(args, sql.Out{Dest: &values[i]})
		}
		text += " RETURNING " + strings.Join(names, ",") + " INTO " + strings.Join(outputs, ",")
	}
	var affected int64
	args = append(args, sql.Out{Dest: &affected})
	_, err := c.ExecContext(ctx, "BEGIN "+text+"; ? := SQL%ROWCOUNT; END;", args...)
	if err != nil {
		return contract.RowRef{}, err
	}
	if affected != 1 {
		return contract.RowRef{}, contract.ErrDataConflict
	}
	if e.Kind == "delete" {
		return e.Original.Ref, nil
	}
	return returnedDataRef(t, key, values)
}

func savedEdit(e contract.DataEdit, snapshot *contract.RowSnapshot) contract.Edit {
	v := contract.Edit{Kind: e.Kind, Values: make(map[string]contract.Cell)}
	ref := snapshot
	if ref == nil {
		ref = e.Original
	}
	if ref != nil {
		v.Identity.Columns = append([]string(nil), ref.Ref.Columns...)
		for _, c := range ref.Ref.TypedValues {
			v.Identity.Values = append(v.Identity.Values, c.Clone())
		}
	}
	if e.Original != nil {
		for _, c := range e.Original.Original {
			v.Original = append(v.Original, c.Clone())
		}
	}
	if snapshot != nil {
		for i, n := range snapshot.Columns {
			v.Values[n] = snapshot.Original[i].Clone()
		}
	}
	return v
}

func (s *Session) saveData(ctx context.Context, id int64, schema, name string, input []contract.DataEdit) (out *contract.DataEditResult, err error) {
	o, err := s.dataObject(id, schema, name, contract.ObjectRef{})
	if err != nil {
		return nil, err
	}
	edits, err := cloneDataEdits(input)
	if err != nil {
		return nil, err
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	c, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	out = &contract.DataEditResult{}
	var tx driver.Tx
	mutationAttempted, commitAttempted := false, false
	defer func() {
		retire := connectionLost(err)
		if tx != nil && !out.CommitConfirmed {
			rb := tx.Rollback()
			if rb != nil {
				retire = true
				err = errors.Join(err, rb)
				if mutationAttempted {
					out.CommitUncertain = true
					out.Incomplete = !commitAttempted
					err = errors.Join(err, contract.ErrDataCommitUncertain)
				}
			} else if !commitAttempted {
				out = &contract.DataEditResult{}
			}
		}
		if retire {
			_ = c.Raw(func(any) error { return driver.ErrBadConn })
		}
		closeErr := c.Close()
		if closeErr != nil && out.CommitConfirmed {
			out.Incomplete = true
		}
		err = errors.Join(err, closeErr)
	}()
	err = c.Raw(func(raw any) error {
		begin, ok := raw.(driver.ConnBeginTx)
		if !ok {
			return contract.ErrUnsupported
		}
		var e error
		tx, e = begin.BeginTx(ctx, driver.TxOptions{})
		return e
	})
	if err != nil {
		_ = c.Raw(func(any) error { return driver.ErrBadConn })
		return out, err
	}
	for _, text := range []string{"SET TRANSACTION ISOLATION LEVEL SERIALIZABLE", "LOCK TABLE " + qualified(o) + " IN EXCLUSIVE MODE"} {
		if _, err = c.ExecContext(ctx, text); err != nil {
			return out, err
		}
	}
	t, err := structureAt(ctx, c, o)
	if err != nil {
		return out, fmt.Errorf("DM8 save structure: %w", err)
	}
	identity, err := tableIdentityAt(ctx, c, o)
	if err != nil {
		return out, err
	}
	key, err := editableTableAt(ctx, c, t)
	if err != nil {
		return out, fmt.Errorf("DM8 editable table: %w", err)
	}
	if err = validateEditBatch(ctx, s, c, t, key, identity, edits); err != nil {
		return out, err
	}
	budget := dataBatchBytes
	for _, e := range edits {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		// Earlier triggers in the batch may have changed another submitted row.
		if e.Original != nil {
			row, e2 := readFullRowAt(ctx, c, t, key, e.Original.Ref)
			if e2 != nil {
				return out, e2
			}
			if !sameDataRow(row, e.Original.Original) {
				return out, contract.ErrDataConflict
			}
		}
		out.Outcomes = append(out.Outcomes, contract.DataEditOutcome{Kind: e.Kind, Original: e.Original})
		mutationAttempted = true
		ref, e2 := executeDataEdit(ctx, c, t, key, e)
		if e2 != nil {
			return out, e2
		}
		out.Applied++
		index := len(out.Outcomes) - 1
		if e.Kind != "delete" {
			row, e2 := readFullRowAt(ctx, c, t, key, ref)
			if e2 != nil {
				return out, e2
			}
			if err = chargeDataRow(&budget, row); err != nil {
				return out, err
			}
			out.Outcomes[index].Saved = s.fullSnapshot(t, key, row, identity)
		}
	}
	// Read final values after all statements so later triggers cannot leave an
	// earlier result displaying values that were superseded within this batch.
	for i, e := range edits {
		outcome := &out.Outcomes[i]
		if e.Kind != "delete" {
			row, e2 := readFullRowAt(ctx, c, t, key, outcome.Saved.Ref)
			if e2 != nil {
				return out, e2
			}
			if err = chargeDataRow(&budget, row); err != nil {
				return out, err
			}
			outcome.Saved = s.fullSnapshot(t, key, row, identity)
			setOutcomePreview(outcome, t, row)
			if e.Kind == "insert" && len(key) == 1 && dataColumns(t)[key[0]].Extra == "IDENTITY" && outcome.Saved.Ref.TypedValues[0].Kind == contract.Integer {
				p, e2 := dataParameter(outcome.Saved.Ref.TypedValues[0], contract.Integer)
				if e2 != nil {
					return out, e2
				}
				outcome.GeneratedID = p.(int64)
			}
		}
		out.Saved = append(out.Saved, savedEdit(e, outcome.Saved))
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	commitAttempted = true
	if err = tx.Commit(); err != nil {
		out.CommitUncertain = true
		return out, errors.Join(err, contract.ErrDataCommitUncertain)
	}
	out.CommitConfirmed = true
	return out, nil
}

func (s *Session) DataEdits() contract.DataEdits {
	return contract.DataEdits{Saver: s.saveData, Verify: s.verifyData}
}

var _ contract.DataEditsProvider = (*Session)(nil)
