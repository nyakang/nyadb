package rqlite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type dataBatch struct {
	statements []any
	mutations  []int
	guards     map[int]bool
}

func dataValueLiteral(c contract.StructureColumn, v contract.DataValue) (string, error) {
	if len(v.Text)+len(v.Bytes) > contract.DataEditMaxBytes {
		return "", contract.ErrDataEditLimit
	}
	if v.Default {
		if v.Null || v.Binary || len(v.Bytes) != 0 || v.Text != "" {
			return "", connections.ErrInvalid
		}
		// SQLite has no UPDATE SET field=DEFAULT. The native parsed default
		// expression is evaluated inside this same original transaction.
		if c.Default != nil {
			return "(" + *c.Default + ")", nil
		}
		if !c.Nullable {
			return "", connections.ErrInvalid
		}
		return "NULL", nil
	}
	if v.Null {
		if !c.Nullable || v.Binary || len(v.Bytes) != 0 || v.Text != "" {
			return "", connections.ErrInvalid
		}
		return "NULL", nil
	}
	kind := v.Kind
	if kind == "" {
		kind = contract.Text
		if v.Binary {
			kind = contract.Binary
		}
	}
	if v.Binary && kind != contract.Binary {
		return "", connections.ErrInvalid
	}
	literal, _, err := dataCellLiteral(contract.Cell{Kind: kind, Text: v.Text, Bytes: v.Bytes})
	return literal, err
}

func (s *Session) originalDataGuard(t tableSnapshot, original *contract.RowSnapshot) (key, guard string, err error) {
	if original == nil || !slices.Equal(original.Columns, columnNames(t)) {
		return "", "", connections.ErrInvalid
	}
	key, err = dataKeyWhere(t, original.Ref)
	if err != nil {
		return "", "", err
	}
	if original.Metadata != s.dataMetadataSeal(t) {
		return "", "", contract.ErrDataConflict
	}
	n := len(t.structure.Columns)
	if original.Mode != "values" && original.Mode != "fingerprints" || original.Mode == "values" && len(original.Original) != n || original.Mode == "fingerprints" && len(original.Fingerprints) != n || len(original.Original) != 0 && len(original.Original) != n || len(original.Fingerprints) != 0 && len(original.Fingerprints) != n {
		return "", "", connections.ErrInvalid
	}
	where := key
	total := 0
	for i, v := range original.Original {
		total += len(v.Text) + len(v.Bytes)
		if total > contract.DataEditMaxBytes || v.Truncated {
			return "", "", contract.ErrDataEditLimit
		}
		column := quote(t.structure.Columns[i].Name)
		if v.Kind == contract.Null {
			if v.Text != "" || len(v.Bytes) != 0 {
				return "", "", connections.ErrInvalid
			}
			where += " AND " + column + " IS NULL"
			continue
		}
		literal, typ, e := dataCellLiteral(v)
		if e != nil {
			return "", "", e
		}
		where += " AND typeof(" + column + ")='" + typ + "' AND " + dataBytes(column) + "=" + dataBytes(literal)
	}
	if len(original.Fingerprints) == 0 {
		return key, "SELECT count(*)=1 FROM main." + quote(t.structure.Name) + " WHERE " + where, nil
	}
	fields, matches := make([]string, n), make([]string, n)
	for i, c := range t.structure.Columns {
		fields[i] = fmt.Sprintf("%s AS c%d", quote(c.Name), i)
		match := "p.digest IS NULL"
		if fp := original.Fingerprints[i]; fp != nil {
			if len(*fp) != 64 {
				return "", "", connections.ErrInvalid
			}
			if _, e := hex.DecodeString(*fp); e != nil {
				return "", "", connections.ErrInvalid
			}
			match = "p.digest='" + strings.ToLower(*fp) + "'"
		}
		matches[i] = fmt.Sprintf("(p.cid=%d AND %s)", i, match)
	}
	source := "SELECT " + strings.Join(fields, ",") + " FROM main." + quote(t.structure.Name) + " WHERE " + where + " LIMIT 2"
	// The separate exactly-one key guard excludes an ambiguous original row.
	// Hash every complete server value; bounded previews never validate writes.
	projection := dataProjection(source, n, 1, false)
	guard = fmt.Sprintf("SELECT count(*)=%d FROM (%s) p WHERE p.rid=0 AND (%s)", n, projection, strings.Join(matches, " OR "))
	return key, guard, nil
}

func columnNames(t tableSnapshot) []string {
	out := make([]string, len(t.structure.Columns))
	for i, c := range t.structure.Columns {
		out[i] = c.Name
	}
	return out
}

func (s *Session) dataEditBatch(t tableSnapshot, edits []contract.DataEdit) (out dataBatch, err error) {
	if !t.structure.Editable {
		return out, contract.ErrDataReadOnly
	}
	if len(edits) == 0 || len(edits) > contract.DataMaxPageSize {
		return out, contract.ErrDataEditLimit
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return out, err
	}
	name := quote("_nyadb_guard_" + hex.EncodeToString(nonce[:]))
	guardTable := "temp." + name
	out.guards = map[int]bool{}
	out.statements = append(out.statements, "CREATE TEMP TABLE "+name+"(ok INTEGER NOT NULL CHECK(ok=1))")
	addGuard := func(query string, conflict bool) {
		out.guards[len(out.statements)] = conflict
		out.statements = append(out.statements, "INSERT INTO "+guardTable+" "+query)
	}
	addGuard("SELECT "+schemaGuard(t), true)
	for _, edit := range edits {
		if edit.GraphEndpoints != nil || edit.Kind != "insert" && edit.Kind != "update" && edit.Kind != "delete" || edit.Kind == "insert" && edit.Original != nil || edit.Kind == "delete" && len(edit.Values) != 0 {
			return dataBatch{}, connections.ErrInvalid
		}
		for name := range edit.Values {
			if !slices.Contains(columnNames(t), name) {
				return dataBatch{}, connections.ErrInvalid
			}
		}
		var names, values, assignments []string
		total := 0
		for _, column := range t.structure.Columns {
			v, ok := edit.Values[column.Name]
			if !ok {
				continue
			}
			if column.Generated {
				return dataBatch{}, contract.ErrUnsupported
			}
			total += len(v.Text) + len(v.Bytes)
			if total > contract.DataEditMaxBytes {
				return dataBatch{}, contract.ErrDataEditLimit
			}
			if edit.Kind == "insert" && v.Default {
				if v.Null || v.Binary || len(v.Bytes) != 0 || v.Text != "" {
					return dataBatch{}, connections.ErrInvalid
				}
				continue // Native INSERT supplies the omitted/default value.
			}
			literal, e := dataValueLiteral(column, v)
			if e != nil {
				return dataBatch{}, e
			}
			names = append(names, quote(column.Name))
			values = append(values, literal)
			assignments = append(assignments, quote(column.Name)+"="+literal)
		}
		statement := ""
		if edit.Kind == "insert" {
			statement = "INSERT INTO main." + quote(t.structure.Name) + " DEFAULT VALUES"
			if len(names) > 0 {
				statement = "INSERT INTO main." + quote(t.structure.Name) + "(" + strings.Join(names, ",") + ") VALUES(" + strings.Join(values, ",") + ")"
			}
		} else {
			key, originalGuard, e := s.originalDataGuard(t, edit.Original)
			if e != nil {
				return dataBatch{}, e
			}
			addGuard("SELECT count(*)=1 FROM main."+quote(t.structure.Name)+" WHERE "+key, true)
			addGuard(originalGuard, true)
			statement = "DELETE FROM main." + quote(t.structure.Name) + " WHERE " + key
			if edit.Kind == "update" {
				if len(assignments) == 0 {
					return dataBatch{}, connections.ErrInvalid
				}
				statement = "UPDATE main." + quote(t.structure.Name) + " SET " + strings.Join(assignments, ",") + " WHERE " + key
			}
		}
		out.mutations = append(out.mutations, len(out.statements))
		out.statements = append(out.statements, statement)
		addGuard("SELECT changes()=1", true)
	}
	out.statements = append(out.statements, "DROP TABLE "+guardTable)
	body, e := json.Marshal(out.statements)
	if e != nil || len(body) > maxRequestBytes || len(out.statements) > 1024 {
		return dataBatch{}, contract.ErrDataEditLimit
	}
	return out, nil
}

func (s *Session) executeDataBatch(ctx context.Context, t tableSnapshot, edits []contract.DataEdit, batch dataBatch) (*contract.DataEditResult, error) {
	out := &contract.DataEditResult{RefreshRequired: true}
	r, err := s.request(ctx, "/db/request", batch.statements, true, true)
	out.NativeJSON = r.Raw
	uncertain := func(cause error) (*contract.DataEditResult, error) {
		out.CommitUncertain, out.Incomplete = true, true
		out.Applied, out.Outcomes = 0, nil
		for _, edit := range edits {
			out.Outcomes = append(out.Outcomes, contract.DataEditOutcome{Kind: edit.Kind, Original: edit.Original})
		}
		return out, errors.Join(contract.ErrDataCommitUncertain, cause)
	}
	if r.Uncertain || errors.Is(err, errIncomplete) || errors.Is(err, errResponseLost) {
		return uncertain(err)
	}
	if err != nil {
		// A complete native failed transaction proves rollback. Keep earlier
		// affected counts only in its exact Raw receipt, never as applied rows.
		for i, result := range r.Results {
			if result.failed() {
				if batch.guards[i] && guardRejected(result) {
					return out, errors.Join(contract.ErrDataConflict, err)
				}
				return out, err
			}
		}
		return out, err // Admission/auth/parser rejection before execution.
	}
	if len(r.Results) != len(batch.statements) {
		return uncertain(errIncomplete)
	}
	for index := range batch.guards {
		if count, e := r.Results[index].Affected.Int64(); e != nil || count != 1 {
			return uncertain(errIncomplete)
		}
	}
	for i, index := range batch.mutations {
		n := r.Results[index]
		affected, e := n.Affected.Int64()
		if e != nil || affected != 1 {
			return uncertain(errIncomplete)
		}
		outcome := contract.DataEditOutcome{Kind: edits[i].Kind, Original: edits[i].Original}
		// TEMP guard inserts have their own last_insert_id. Use only the
		// original INSERT receipt and only a proven INTEGER rowid key.
		if edits[i].Kind == "insert" && t.rowIDKey != "" {
			outcome.GeneratedID, e = n.InsertID.Int64()
			if e != nil {
				return uncertain(errIncomplete)
			}
		}
		out.Outcomes = append(out.Outcomes, outcome)
	}
	out.Applied, out.CommitConfirmed = len(edits), true
	return out, nil
}

func guardRejected(result nativeResult) bool {
	var detail struct {
		SQLite struct{ Code, ExtendedCode int } `json:"sqlite"`
	}
	if json.Unmarshal(result.ErrorV2, &detail) == nil && detail.SQLite.Code == 19 && detail.SQLite.ExtendedCode == 275 {
		return true
	}
	return result.Error == "CHECK constraint failed: ok=1"
}
