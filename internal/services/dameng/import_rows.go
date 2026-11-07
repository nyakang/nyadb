package dameng

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/importspec"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func importTableAt(ctx context.Context, q metadataQueryer, t *contract.TableStructure) error {
	if err := exportTableAt(ctx, q, t); err != nil {
		return err
	}
	if _, err := dataProjection(t, 1024); err != nil {
		return err
	}
	for _, col := range t.Columns {
		if col.DataType == "CLOB" || col.DataType == "TEXT" || col.DataType == "BLOB" || col.DataType == "IMAGE" {
			var count int64
			if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM SYS.ALL_TRIGGERS WHERE TABLE_OWNER=? AND TABLE_NAME=?`, t.Object.Schema, t.Object.Name).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return contract.ErrDataReadOnly
			}
			break
		}
	}
	return nil
}

// Resolve only the reviewed complete, non-null unique index. A collision on
// another unique index is an error, never permission to update its other row.
func importKeyAt(ctx context.Context, q metadataQueryer, t *contract.TableStructure, chosen []string) ([]string, error) {
	if len(chosen) == 0 || len(chosen) > 16 {
		return nil, contract.ErrDataReadOnly
	}
	seen := map[string]bool{}
	columns := dataColumns(t)
	for _, name := range chosen {
		c, ok := columns[name]
		if !ok || seen[name] || c.Nullable || c.Generated && c.Extra != "IDENTITY" || variableField(c) && (c.DataType == "CLOB" || c.DataType == "TEXT" || c.DataType == "BLOB" || c.DataType == "IMAGE") {
			return nil, contract.ErrDataReadOnly
		}
		seen[name] = true
	}
	rows, err := q.QueryContext(ctx, `SELECT I.INDEX_NAME,IC.COLUMN_NAME,IC.COLUMN_POSITION FROM SYS.ALL_INDEXES I JOIN SYS.ALL_IND_COLUMNS IC ON IC.INDEX_OWNER=I.OWNER AND IC.INDEX_NAME=I.INDEX_NAME AND IC.TABLE_OWNER=I.TABLE_OWNER AND IC.TABLE_NAME=I.TABLE_NAME WHERE I.TABLE_OWNER=? AND I.TABLE_NAME=? AND I.STATUS='VALID' AND I.UNIQUENESS='UNIQUE' AND I.INDEX_TYPE IN ('NORMAL','CLUSTER') AND I.PARTITIONED='NO' AND I.FUNCIDX_STATUS IS NULL AND I.ITYP_OWNER IS NULL AND NOT EXISTS(SELECT 1 FROM SYS.ALL_CONSTRAINTS C WHERE C.INDEX_OWNER=I.OWNER AND C.INDEX_NAME=I.INDEX_NAME AND (C.STATUS<>'ENABLED' OR C.VALIDATED<>'VALIDATED' OR C.DEFERRABLE<>'NOT DEFERRABLE')) ORDER BY I.INDEX_NAME,IC.COLUMN_POSITION`, t.Object.Schema, t.Object.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var index string
	var key, selected []string
	match := func() {
		if len(key) == len(chosen) {
			ok := true
			for _, n := range key {
				ok = ok && seen[n]
			}
			if ok {
				selected = slices.Clone(key)
			}
		}
	}
	for rows.Next() {
		var name, col string
		var position int64
		if err = rows.Scan(&name, &col, &position); err != nil {
			return nil, err
		}
		if name != index {
			match()
			key = nil
			index = name
		}
		if position != int64(len(key)+1) || len(key) >= 16 {
			return nil, contract.ErrUnsupported
		}
		key = append(key, col)
	}
	match()
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, contract.ErrDataReadOnly
	}
	return selected, nil
}

func validateImportRow(t *contract.TableStructure, r contract.DataImportRequest, edit contract.DataEdit) error {
	bytes := 0
	for _, value := range edit.Values {
		bytes += len(value.Text) + len(value.Bytes)
		if bytes > 16<<20 {
			return contract.ErrDataEditLimit
		}
	}
	check := r
	check.NewTable = false
	check.Columns = nil
	check.Edits = []contract.DataEdit{edit}
	if err := importspec.Validate("dameng", t.Columns, check); err != nil {
		return err
	}
	if importspec.NeedsKey(r.Mode) {
		for _, name := range r.MatchColumns {
			col := dataColumns(t)[name]
			if _, err := editParameter(col, edit.Values[name]); err != nil {
				return errors.Join(contract.ErrDataReadOnly, err)
			}
		}
	}
	if r.Mode != "delete" {
		for name, v := range edit.Values {
			if v.Default {
				continue
			}
			if _, err := editParameter(dataColumns(t)[name], v); err != nil {
				return err
			}
		}
		if r.Mode == "append" || r.Mode == "replace" {
			if _, _, _, err := editAssignments(t, edit); err != nil {
				return err
			}
		}
	}
	return nil
}

func importWhere(t *contract.TableStructure, key []string, edit contract.DataEdit) (string, []any, error) {
	columns := dataColumns(t)
	var terms []string
	var args []any
	for _, name := range key {
		v, ok := edit.Values[name]
		if !ok || v.Null || v.Default {
			return "", nil, contract.ErrDataReadOnly
		}
		p, err := editParameter(columns[name], v)
		if err != nil {
			return "", nil, errors.Join(contract.ErrDataReadOnly, err)
		}
		terms = append(terms, quote(name)+"=?")
		args = append(args, p)
	}
	return strings.Join(terms, " AND "), args, nil
}

func importPhysicalRow(ctx context.Context, q metadataQueryer, t *contract.TableStructure, where string, args []any) (string, bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT ROWID FROM "+qualified(t.Object)+" WHERE "+where+" LIMIT 2", args...)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var rowID string
	count := 0
	for rows.Next() {
		if err = rows.Scan(&rowID); err != nil {
			return "", false, err
		}
		count++
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return "", false, err
	}
	if count > 1 {
		return "", false, contract.ErrDataReadOnly
	}
	return rowID, count == 1, nil
}

func (s *Session) importSnapshot(ctx context.Context, q metadataQueryer, t *contract.TableStructure, rowID string) (*contract.RowSnapshot, error) {
	values, err := exportRowAt(ctx, q, t.Object, t.Columns, rowID)
	if err != nil {
		return nil, err
	}
	cellID := contract.Cell{Kind: contract.Integer, Text: rowID}
	encoded, _ := json.Marshal(cellID)
	out := &contract.RowSnapshot{Mode: "values", Metadata: s.dataSignature(t, contract.DataOptions{}, nil, "import"), Ref: contract.RowRef{Object: t.Object, Database: t.Object.Schema, Table: t.Object.Name, Columns: []string{"ROWID"}, TypedValues: []contract.Cell{cellID}, Values: [][]byte{encoded}}}
	for i, v := range values {
		out.Columns = append(out.Columns, t.Columns[i].Name)
		out.Original = append(out.Original, contract.Cell{Kind: v.Kind, Text: v.Text, Bytes: bytes.Clone(v.Bytes)})
	}
	return out, nil
}

type deferredImportLOB struct {
	column contract.StructureColumn
	value  contract.DataValue
}

// Keep each complete LOB in its own native request. A 16 MiB source row must
// not exceed the worker frame after JSON escaping or binary/base64 encoding.
func importAssignments(t *contract.TableStructure, e contract.DataEdit) ([]string, []string, []any, []deferredImportLOB, error) {
	names, expressions, args, err := editAssignments(t, e)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var bound []any
	var deferred []deferredImportLOB
	columns := dataColumns(t)
	at := 0
	for i, quoted := range names {
		if expressions[i] != "?" {
			continue
		}
		name := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(quoted, `"`), `"`), `""`, `"`)
		col := columns[name]
		v := e.Values[name]
		if !v.Null && v.Kind != contract.Null && (col.DataType == "CLOB" || col.DataType == "TEXT" || col.DataType == "BLOB" || col.DataType == "IMAGE") {
			expressions[i] = "EMPTY_CLOB()"
			if col.ValueKind == contract.Binary {
				expressions[i] = "EMPTY_BLOB()"
			}
			deferred = append(deferred, deferredImportLOB{col, v})
		} else {
			bound = append(bound, args[at])
		}
		at++
	}
	return names, expressions, bound, deferred, nil
}

func importExec(ctx context.Context, c *sql.Conn, query string, args []any, maxSQL int, mutated *bool) error {
	if maxSQL > 0 && len(query) > maxSQL {
		return contract.ErrDataEditLimit
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	*mutated = true
	_, err := c.ExecContext(ctx, query, args...)
	return err
}

func (s *Session) importRow(ctx context.Context, c *sql.Conn, t *contract.TableStructure, r contract.DataImportRequest, key []string, input contract.DataEdit, maxSQL int, mutated *bool) (contract.DataImportRowResult, error) {
	row := contract.DataImportRowResult{Kind: "insert", Status: "prepared"}
	edit := input
	var rowID string
	var original *contract.RowSnapshot
	if importspec.NeedsKey(r.Mode) {
		where, args, err := importWhere(t, key, input)
		if err != nil {
			return row, err
		}
		id, present, err := importPhysicalRow(ctx, c, t, where, args)
		if err != nil {
			return row, err
		}
		if !present && (r.Mode == "update" || r.Mode == "delete") || present && r.Mode == "insert_missing" {
			row.Kind, row.Status = "skip", "skipped"
			return row, nil
		}
		if present {
			rowID = id
			original, err = s.importSnapshot(ctx, c, t, id)
			if err != nil {
				return row, err
			}
			edit.Kind = "update"
			if r.Mode == "delete" {
				edit.Kind = "delete"
				edit.Values = nil
			} else {
				edit.Values = map[string]contract.DataValue{}
				for name, v := range input.Values {
					if !slices.Contains(key, name) {
						edit.Values[name] = v
					}
				}
				if len(edit.Values) == 0 {
					row.Kind, row.Status = "skip", "skipped"
					return row, nil
				}
			}
		}
	}
	row.Kind = edit.Kind
	row.Outcome = &contract.DataEditOutcome{Kind: edit.Kind, Original: original}
	var query string
	var args []any
	var deferred []deferredImportLOB
	if edit.Kind == "delete" {
		query = "DELETE FROM " + qualified(t.Object) + " WHERE ROWID=?"
		args = []any{rowID}
	} else {
		names, expressions, bound, lobs, err := importAssignments(t, edit)
		if err != nil {
			return row, err
		}
		args, deferred = bound, lobs
		if edit.Kind == "insert" {
			query = "INSERT INTO " + qualified(t.Object) + "(" + strings.Join(names, ",") + ") VALUES(" + strings.Join(expressions, ",") + ")"
		} else {
			var assignments []string
			for i, name := range names {
				assignments = append(assignments, name+"="+expressions[i])
			}
			query = "UPDATE " + qualified(t.Object) + " SET " + strings.Join(assignments, ",") + " WHERE ROWID=?"
			args = append(args, rowID)
		}
	}
	var actual sql.NullString
	var affected int64
	args = append(args, sql.Out{Dest: &actual}, sql.Out{Dest: &affected})
	query = "BEGIN " + query + " RETURNING ROWID INTO ?; ? := SQL%ROWCOUNT; END;"
	if err := importExec(ctx, c, query, args, maxSQL, mutated); err != nil {
		return row, err
	}
	if affected != 1 || !actual.Valid || actual.String == "" {
		return row, contract.ErrDataConflict
	}
	for _, lob := range deferred {
		parameter, err := editParameter(lob.column, lob.value)
		if err != nil {
			return row, err
		}
		args = []any{parameter, actual.String, sql.Out{Dest: &affected}}
		query = "BEGIN UPDATE " + qualified(t.Object) + " SET " + quote(lob.column.Name) + "=? WHERE ROWID=?; ? := SQL%ROWCOUNT; END;"
		if err = importExec(ctx, c, query, args, maxSQL, mutated); err != nil {
			return row, err
		}
		if affected != 1 {
			return row, contract.ErrDataConflict
		}
	}
	if edit.Kind != "delete" {
		snapshot, err := s.importSnapshot(ctx, c, t, actual.String)
		if err != nil {
			return row, err
		}
		row.Outcome.Saved = snapshot
		setOutcomePreview(row.Outcome, t, snapshot.Original)
		for i, col := range t.Columns {
			if col.Extra == "IDENTITY" && col.ValueKind == contract.Integer {
				row.Outcome.GeneratedID, _ = strconv.ParseInt(snapshot.Original[i].Text, 10, 64)
				break
			}
		}
	}
	return row, nil
}

func importRowDiagnostic(err error) string {
	if errors.Is(err, connections.ErrInvalid) || errors.Is(err, contract.ErrDataPage) {
		return "invalid mapped value or column"
	}
	return "row validation or native database operation failed"
}
