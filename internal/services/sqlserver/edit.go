package sqlserver

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func valueArgument(c contract.StructureColumn, v contract.DataValue) (any, error) {
	if v.Default {
		return nil, contract.ErrUnsupported
	}
	if v.Null {
		if !c.Nullable {
			return nil, connections.ErrInvalid
		}
		return nil, nil
	}
	if v.Binary || v.Kind == contract.Binary {
		if c.ValueKind != contract.Binary {
			return nil, connections.ErrInvalid
		}
		return bytes.Clone(v.Bytes), nil
	}
	if len(v.Text) > contract.DataEditMaxBytes || !utf8.ValidString(v.Text) {
		return nil, contract.ErrDataEditLimit
	}
	if c.ValueKind == contract.Binary {
		return nil, connections.ErrInvalid
	}
	switch c.ValueKind {
	case contract.Integer:
		n, err := strconv.ParseInt(v.Text, 10, 64)
		if err != nil {
			return nil, connections.ErrInvalid
		}
		return n, nil
	case contract.Boolean:
		v, err := strconv.ParseBool(v.Text)
		if err != nil {
			return nil, connections.ErrInvalid
		}
		return v, nil
	case contract.Decimal:
		n, ok := new(big.Rat).SetString(v.Text)
		if !ok || strings.ContainsAny(v.Text, "/eE") {
			return nil, connections.ErrInvalid
		}
		scale := int64(4)
		if c.NumericScale != nil {
			scale = *c.NumericScale
		}
		factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(scale), nil)
		if new(big.Rat).Mul(n, new(big.Rat).SetInt(factor)).Denom().Cmp(big.NewInt(1)) != 0 {
			return nil, connections.ErrInvalid
		}
	}
	return v.Text, nil
}
func cellArgument(c contract.StructureColumn, v contract.Cell) (any, error) {
	if v.Truncated {
		return nil, connections.ErrInvalid
	}
	return valueArgument(c, contract.DataValue{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes, Null: v.Kind == contract.Null, Binary: v.Kind == contract.Binary})
}
func validateRef(t *contract.TableStructure, r contract.RowRef) error {
	keys := keyColumns(t)
	if r.Object != t.Object || r.Database != t.Object.Schema || r.Table != t.Name || len(keys) == 0 || len(keys) != len(r.Columns) || len(keys) != len(r.TypedValues) {
		return connections.ErrInvalid
	}
	if len(r.Values) != 0 && len(r.Values) != len(keys) {
		return connections.ErrInvalid
	}
	for i, key := range keys {
		v := r.TypedValues[i]
		if r.Columns[i] != key || v.Truncated || v.Kind == contract.Null {
			return connections.ErrInvalid
		}
		if len(r.Values) > 0 {
			raw := []byte(v.Text)
			if v.Kind == contract.Binary {
				raw = v.Bytes
			}
			if !bytes.Equal(raw, r.Values[i]) {
				return connections.ErrInvalid
			}
		}
	}
	return nil
}
func keyWhere(t *contract.TableStructure, r contract.RowRef, args *[]any) (string, error) {
	if err := validateRef(t, r); err != nil {
		return "", err
	}
	var conditions []string
	for i, name := range r.Columns {
		var col contract.StructureColumn
		for _, c := range t.Columns {
			if c.Name == name {
				col = c
			}
		}
		arg, err := cellArgument(col, r.TypedValues[i])
		if err != nil {
			return "", err
		}
		*args = append(*args, arg)
		conditions = append(conditions, fmt.Sprintf("CONVERT(varbinary(max),%s)=CONVERT(varbinary(max),CONVERT(%s,@p%d))", quote(name), col.Type, len(*args)))
	}
	return strings.Join(conditions, " AND "), nil
}
func (s *Session) originalWhere(t *contract.TableStructure, snap *contract.RowSnapshot, args *[]any) (string, error) {
	if snap == nil || snap.Metadata != s.metadataSeal(t) || len(snap.Columns) != len(t.Columns) {
		return "", connections.ErrInvalid
	}
	where, err := keyWhere(t, snap.Ref, args)
	if err != nil {
		return "", err
	}
	if snap.Mode == "fingerprints" && len(snap.Fingerprints) != len(t.Columns) || snap.Mode == "values" && len(snap.Original) != len(t.Columns) {
		return "", connections.ErrInvalid
	}
	for i, col := range t.Columns {
		if snap.Columns[i] != col.Name {
			return "", connections.ErrInvalid
		}
		switch snap.Mode {
		case "fingerprints":
			if snap.Fingerprints[i] == nil {
				where += " AND " + quote(col.Name) + " IS NULL"
			} else {
				fp := *snap.Fingerprints[i]
				if len(fp) != 64 {
					return "", connections.ErrInvalid
				}
				if _, err := hexDecode(fp); err != nil {
					return "", connections.ErrInvalid
				}
				*args = append(*args, fp)
				where += fmt.Sprintf(" AND %s=@p%d", fingerprint(col.Name), len(*args))
			}
		case "values":
			v := snap.Original[i]
			if v.Kind == contract.Null {
				where += " AND " + quote(col.Name) + " IS NULL"
			} else {
				if !comparable(col) {
					return "", contract.ErrUnsupported
				}
				arg, err := cellArgument(col, v)
				if err != nil {
					return "", err
				}
				*args = append(*args, arg)
				where += fmt.Sprintf(" AND CONVERT(varbinary(max),%s)=CONVERT(varbinary(max),CONVERT(%s,@p%d))", quote(col.Name), col.Type, len(*args))
			}
		default:
			return "", connections.ErrInvalid
		}
	}
	return where, nil
}
func hexDecode(v string) ([]byte, error) {
	var result []byte
	for i := 0; i < len(v); i += 2 {
		n, err := strconv.ParseUint(v[i:i+2], 16, 8)
		if err != nil {
			return nil, err
		}
		result = append(result, byte(n))
	}
	return result, nil
}
func (s *Session) save(ctx context.Context, id int64, schema, name string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	tx, err := s.db.BeginTx(child, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, safeError(err)
	}
	defer tx.Rollback()
	result, err := s.apply(child, tx, id, schema, name, edits)
	if err != nil {
		rollbackErr := tx.Rollback()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			result.CommitUncertain = true
			return result, errors.Join(contract.ErrDataCommitUncertain, err, safeError(rollbackErr))
		}
		return result, err
	}
	if err = tx.Commit(); err != nil {
		result.CommitUncertain = true
		return result, errors.Join(contract.ErrDataCommitUncertain, safeError(err))
	}
	result.CommitConfirmed = true
	return result, nil
}
func (s *Session) apply(ctx context.Context, tx *sql.Tx, id int64, schema, name string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
	result := &contract.DataEditResult{}
	if len(edits) == 0 || len(edits) > 500 {
		return result, contract.ErrDataEditLimit
	}
	t, err := s.resolve(ctx, tx, id, schema, name, contract.ObjectRef{})
	if err != nil {
		return result, err
	}
	if !t.Editable {
		return result, contract.ErrDataReadOnly
	}
	keys := keyColumns(t)
	for _, edit := range edits {
		if edit.Kind != "insert" && edit.Kind != "update" && edit.Kind != "delete" {
			return result, connections.ErrInvalid
		}
		var args []any
		var names, values, assignments []string
		budget := 0
		for name := range edit.Values {
			found := false
			for _, col := range t.Columns {
				if col.Name == name {
					found = true
				}
			}
			if !found {
				return result, connections.ErrInvalid
			}
		}
		for _, col := range t.Columns {
			v, ok := edit.Values[col.Name]
			if !ok {
				continue
			}
			if col.Generated || !comparable(col) && col.DataType != "xml" && col.DataType != "text" && col.DataType != "ntext" && col.DataType != "image" {
				return result, contract.ErrUnsupported
			}
			budget += len(v.Text) + len(v.Bytes)
			if budget > contract.DataEditMaxBytes {
				return result, contract.ErrDataEditLimit
			}
			if v.Default {
				if edit.Kind == "insert" {
					continue
				}
				assignments = append(assignments, quote(col.Name)+"=DEFAULT")
				continue
			}
			arg, err := valueArgument(col, v)
			if err != nil {
				return result, err
			}
			args = append(args, arg)
			param := fmt.Sprintf("@p%d", len(args))
			names = append(names, quote(col.Name))
			values = append(values, param)
			assignments = append(assignments, quote(col.Name)+"="+param)
		}
		var where string
		if edit.Kind != "insert" {
			where, err = s.originalWhere(t, edit.Original, &args)
			if err != nil {
				return result, err
			}
		}
		var declarations, output, selected []string
		for _, key := range keys {
			for _, col := range t.Columns {
				if col.Name == key {
					declarations = append(declarations, quote(key)+" "+col.Type)
					prefix := "INSERTED."
					if edit.Kind == "delete" {
						prefix = "DELETED."
					}
					output = append(output, prefix+quote(key))
					selected = append(selected, quote(key))
				}
			}
		}
		query := "DECLARE @nyadb_keys TABLE (" + strings.Join(declarations, ",") + "); "
		out := " OUTPUT " + strings.Join(output, ",") + " INTO @nyadb_keys "
		switch edit.Kind {
		case "insert":
			query += "INSERT INTO " + qualified(t.Object)
			if len(names) > 0 {
				query += " (" + strings.Join(names, ",") + ")" + out + "VALUES (" + strings.Join(values, ",") + ")"
			} else {
				query += out + "DEFAULT VALUES"
			}
		case "update":
			if len(assignments) == 0 {
				return result, connections.ErrInvalid
			}
			query += "UPDATE " + qualified(t.Object) + " WITH (UPDLOCK,HOLDLOCK) SET " + strings.Join(assignments, ",") + out + "WHERE " + where
		case "delete":
			if len(edit.Values) != 0 {
				return result, connections.ErrInvalid
			}
			query += "DELETE FROM " + qualified(t.Object) + " WITH (UPDLOCK,HOLDLOCK)" + out + "WHERE " + where
		}
		query += "; SELECT " + strings.Join(selected, ",") + " FROM @nyadb_keys;"
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return result, safeError(err)
		}
		var keyCells []contract.Cell
		count := 0
		for {
			cols, err := rows.Columns()
			if err != nil {
				rows.Close()
				return result, safeError(err)
			}
			for rows.Next() {
				if len(cols) == 0 {
					continue
				}
				if len(cols) != len(keys) {
					rows.Close()
					return result, contract.ErrUnsupported
				}
				raw := make([]any, len(keys))
				dest := make([]any, len(keys))
				for i := range raw {
					dest[i] = &raw[i]
				}
				if err = rows.Scan(dest...); err != nil {
					rows.Close()
					return result, safeError(err)
				}
				count++
				if count > 1 {
					rows.Close()
					return result, contract.ErrDataConflict
				}
				for i, key := range keys {
					for _, col := range t.Columns {
						if col.Name == key {
							keyCells = append(keyCells, cell(raw[i], col.DataType))
						}
					}
				}
			}
			if !rows.NextResultSet() {
				break
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, safeError(err)
		}
		if count != 1 {
			return result, contract.ErrDataConflict
		}
		outcome := contract.DataEditOutcome{Kind: edit.Kind, Original: edit.Original}
		if edit.Kind != "delete" {
			ref := contract.RowRef{Object: t.Object, Database: schema, Table: name, Columns: keys, TypedValues: keyCells}
			row, previews, readErr := s.rowWithPreviews(ctx, tx, t, ref, false)
			if readErr != nil {
				return result, readErr
			}
			outcome.Saved = row.Snapshot
			for _, col := range t.Columns {
				outcome.PreviewColumns = append(outcome.PreviewColumns, col.Name)
			}
			outcome.Previews = previews
		}
		result.Outcomes = append(result.Outcomes, outcome)
		result.Applied++
	}
	return result, nil
}
func (s *Session) verify(ctx context.Context, id int64, schema, name string, result *contract.DataEditResult) (*contract.DataVerification, error) {
	if id != s.ProfileID() {
		return nil, connections.ErrInvalid
	}
	// A fresh read cannot prove that a partially acknowledged batch or its
	// triggers did not commit. Never turn this into permission to replay.
	if _, err := s.tableMetadata(ctx, id, schema, name); err != nil {
		return nil, err
	}
	return &contract.DataVerification{Status: "indeterminate"}, nil
}
