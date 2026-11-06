package oracle

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	goora "github.com/sijms/go-ora/v2"
	"strings"
	"time"
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
		if len(v.Bytes) > contract.DataEditMaxBytes {
			return nil, contract.ErrDataEditLimit
		}
		if c.DataType == "BLOB" {
			return goora.Blob{Data: bytes.Clone(v.Bytes), Valid: true}, nil
		}
		return bytes.Clone(v.Bytes), nil
	}
	if len(v.Text) > contract.DataEditMaxBytes || !utf8.ValidString(v.Text) {
		return nil, contract.ErrDataEditLimit
	}
	if c.ValueKind == contract.Binary {
		return nil, connections.ErrInvalid
	}
	if c.ValueKind == contract.Text && v.Text == "" && !c.Nullable {
		return nil, connections.ErrInvalid
	}
	switch c.ValueKind {
	case contract.Decimal:
		return numberArgument(c, v.Text)
	case contract.DateTime:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999", "2006-01-02"} {
			if t, err := time.Parse(layout, v.Text); err == nil {
				if c.DataType == "DATE" && t.Nanosecond() != 0 {
					return nil, connections.ErrInvalid
				}
				if c.DatetimePrecision != nil {
					precision := *c.DatetimePrecision
					factor := int64(1)
					for i := precision; i < 9; i++ {
						factor *= 10
					}
					if int64(t.Nanosecond())%factor != 0 {
						return nil, connections.ErrInvalid
					}
				}
				if strings.Contains(c.DataType, "TIME ZONE") {
					return goora.TimeStampTZ(t), nil
				}
				if strings.HasPrefix(c.DataType, "TIMESTAMP") {
					return goora.TimeStamp(t), nil
				}
				return t, nil
			}
		}
		return nil, connections.ErrInvalid
	}
	if c.DataType == "CLOB" {
		return goora.Clob{String: v.Text, Valid: true}, nil
	}
	if c.DataType == "NCLOB" {
		return goora.NClob{String: v.Text, Valid: true}, nil
	}
	if c.DataType == "NVARCHAR2" || c.DataType == "NCHAR" {
		return goora.NVarChar(v.Text), nil
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
		conditions = append(conditions, fmt.Sprintf("%s=:%d", quote(name), len(*args)))
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
				if _, err := hex.DecodeString(fp); err != nil {
					return "", connections.ErrInvalid
				}
				*args = append(*args, fp)
				where += fmt.Sprintf(" AND %s=:%d", fingerprint(col), len(*args))
			}
		case "values":
			v := snap.Original[i]
			if v.Kind == contract.Null {
				where += " AND " + quote(col.Name) + " IS NULL"
			} else {
				if !supported(col) {
					return "", contract.ErrUnsupported
				}
				arg, err := cellArgument(col, v)
				if err != nil {
					return "", err
				}
				*args = append(*args, arg)
				if lob(col) {
					where += fmt.Sprintf(" AND DBMS_LOB.COMPARE(%s,:%d)=0", quote(col.Name), len(*args))
				} else {
					where += fmt.Sprintf(" AND %s=:%d", quote(col.Name), len(*args))
				}
			}
		default:
			return "", connections.ErrInvalid
		}
	}
	return where, nil
}
func (s *Session) save(ctx context.Context, id int64, schema, name string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	tx, err := s.db.BeginTx(child, &sql.TxOptions{Isolation: sql.LevelDefault})
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
			if col.Generated || !supported(col) {
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
			param := fmt.Sprintf(":%d", len(args))
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
		if edit.Kind != "insert" {
			var lockArgs []any
			lockWhere, err := s.originalWhere(t, edit.Original, &lockArgs)
			if err != nil {
				return result, err
			}
			rows, err := tx.QueryContext(ctx, "SELECT ROWID FROM "+qualified(t.Object)+" WHERE "+lockWhere+" FOR UPDATE", lockArgs...)
			if err != nil {
				return result, safeError(err)
			}
			if !rows.Next() {
				readErr := rows.Err()
				rows.Close()
				if readErr != nil {
					return result, safeError(readErr)
				}
				return result, contract.ErrDataConflict
			}
			var originalRowID string
			if err = rows.Scan(&originalRowID); err != nil {
				rows.Close()
				return result, safeError(err)
			}
			if rows.Next() {
				rows.Close()
				return result, contract.ErrDataConflict
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return result, safeError(err)
			}
		}
		var query string
		switch edit.Kind {
		case "insert":
			query = "INSERT INTO " + qualified(t.Object)
			if len(names) == 0 {
				for _, col := range t.Columns {
					if !col.Generated {
						names = append(names, quote(col.Name))
						values = append(values, "DEFAULT")
					}
				}
			}
			if len(names) == 0 {
				return result, contract.ErrUnsupported
			}
			query += " (" + strings.Join(names, ",") + ") VALUES (" + strings.Join(values, ",") + ")"
		case "update":
			if len(assignments) == 0 {
				return result, connections.ErrInvalid
			}
			query = "UPDATE " + qualified(t.Object) + " SET " + strings.Join(assignments, ",") + " WHERE " + where
		case "delete":
			if len(edit.Values) != 0 {
				return result, connections.ErrInvalid
			}
			query = "DELETE FROM " + qualified(t.Object) + " WHERE " + where
		}
		// Capture the physical row after DML, then read its actual key and values.
		// Triggers and defaults can change both; reconstructing them from edits is unsafe.
		var rowid string
		if edit.Kind != "delete" {
			args = append(args, goora.Out{Dest: &rowid, Size: 128})
			query += fmt.Sprintf(" RETURNING ROWID INTO :%d", len(args))
		}
		executed, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return result, safeError(err)
		}
		count, err := executed.RowsAffected()
		if err != nil || count != 1 {
			return result, contract.ErrDataConflict
		}
		outcome := contract.DataEditOutcome{Kind: edit.Kind, Original: edit.Original}
		if edit.Kind != "delete" {
			keys := keyColumns(t)
			var fields []string
			for _, key := range keys {
				fields = append(fields, quote(key))
			}
			rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(fields, ",")+" FROM "+qualified(t.Object)+" WHERE ROWID=:1", rowid)
			if err != nil {
				return result, safeError(err)
			}
			if !rows.Next() {
				rows.Close()
				return result, contract.ErrDataConflict
			}
			raw := make([]any, len(keys))
			dest := make([]any, len(keys))
			for i := range raw {
				dest[i] = &raw[i]
			}
			err = rows.Scan(dest...)
			rows.Close()
			if err != nil {
				return result, safeError(err)
			}
			ref := contract.RowRef{Object: t.Object, Database: schema, Table: name, Columns: keys}
			for i, key := range keys {
				for _, col := range t.Columns {
					if col.Name == key {
						ref.TypedValues = append(ref.TypedValues, cell(raw[i], col.DataType))
					}
				}
			}
			row, previews, err := s.rowWithPreviews(ctx, tx, t, ref, false)
			if err != nil {
				return result, err
			}
			outcome.Saved = row.Snapshot
			outcome.Previews = previews
			for _, col := range t.Columns {
				outcome.PreviewColumns = append(outcome.PreviewColumns, col.Name)
			}
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

func filterArgument(c contract.StructureColumn, text string) (any, error) {
	if c.ValueKind == contract.Binary {
		return nil, contract.ErrUnsupported
	}
	if c.ValueKind == contract.Decimal {
		c.NumericScale = nil
		c.NumericPrecision = nil
		return numberArgument(c, text)
	}
	if c.ValueKind == contract.DateTime {
		return valueArgument(c, contract.DataValue{Text: text})
	}
	if c.DataType == "NVARCHAR2" || c.DataType == "NCHAR" {
		return goora.NVarChar(text), nil
	}
	return text, nil
}
