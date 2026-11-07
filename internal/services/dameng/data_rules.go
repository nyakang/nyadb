package dameng

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

func qualified(o contract.ObjectRef) string { return quote(o.Schema) + "." + quote(o.Name) }

func dataColumns(t *contract.TableStructure) map[string]contract.StructureColumn {
	cols := make(map[string]contract.StructureColumn, len(t.Columns))
	for _, c := range t.Columns {
		cols[c.Name] = c
	}
	return cols
}

// Native numeric comparisons have been verified for seek and row identity.
// Other key types remain available for offset browsing, without a seek claim.
func numericKey(c contract.StructureColumn) bool {
	return !c.Nullable && (!c.Generated || c.Extra == "IDENTITY") && (c.ValueKind == contract.Integer || c.ValueKind == contract.Decimal)
}

func primaryKeyAt(ctx context.Context, q metadataQueryer, t *contract.TableStructure) ([]string, error) {
	// Native ALL_CONS_COLUMNS hides granted foreign keys from a SELECT-only
	// account. Qualified physical index columns remain visible; combine them
	// with the validated, nondeferrable primary constraint and valid index.
	rows, err := q.QueryContext(ctx, `SELECT PC.CONSTRAINT_NAME,IC.COLUMN_NAME,IC.COLUMN_POSITION FROM SYS.ALL_CONSTRAINTS PC JOIN SYS.ALL_INDEXES I ON I.OWNER=PC.INDEX_OWNER AND I.INDEX_NAME=PC.INDEX_NAME JOIN SYS.ALL_IND_COLUMNS IC ON IC.INDEX_OWNER=I.OWNER AND IC.INDEX_NAME=I.INDEX_NAME AND IC.TABLE_OWNER=PC.OWNER AND IC.TABLE_NAME=PC.TABLE_NAME WHERE PC.OWNER=? AND PC.TABLE_NAME=? AND PC.CONSTRAINT_TYPE='P' AND PC.STATUS='ENABLED' AND PC.VALIDATED='VALIDATED' AND PC.DEFERRABLE='NOT DEFERRABLE' AND I.STATUS='VALID' AND I.UNIQUENESS='UNIQUE' AND I.INDEX_TYPE IN ('NORMAL','CLUSTER') AND I.PARTITIONED='NO' AND I.FUNCIDX_STATUS IS NULL AND I.ITYP_OWNER IS NULL ORDER BY IC.COLUMN_POSITION`, t.Object.Schema, t.Object.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := dataColumns(t)
	var key []string
	var constraint string
	approved := true
	for rows.Next() {
		var name, col string
		var pos int64
		if err = rows.Scan(&name, &col, &pos); err != nil {
			return nil, err
		}
		if constraint != "" && name != constraint || pos != int64(len(key)+1) || len(key) >= 16 {
			return nil, contract.ErrUnsupported
		}
		constraint = name
		c, ok := cols[col]
		approved = approved && ok && numericKey(c)
		key = append(key, col)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if !approved {
		return nil, nil
	}
	return key, nil
}

func dataParameter(v contract.Cell, kind contract.CellKind) (any, error) {
	if v.Kind != kind || v.Truncated || !utf8.ValidString(v.Text) || len(v.Text) > 4096 || len(v.Bytes) > 4096 || kind != contract.Binary && len(v.Bytes) != 0 || kind == contract.Binary && v.Text != "" {
		return nil, connections.ErrInvalid
	}
	switch kind {
	case contract.Integer:
		n, err := strconv.ParseInt(v.Text, 10, 64)
		if err != nil {
			return nil, connections.ErrInvalid
		}
		return n, nil
	case contract.Decimal:
		text := v.Text
		if strings.HasPrefix(text, "-") || strings.HasPrefix(text, "+") {
			text = text[1:]
		}
		parts := strings.Split(text, ".")
		if len(parts) > 2 || parts[0] == "" || len(parts) == 2 && parts[1] == "" || len(v.Text) > 128 {
			return nil, connections.ErrInvalid
		}
		digits := strings.Join(parts, "")
		for _, d := range digits {
			if d < '0' || d > '9' {
				return nil, connections.ErrInvalid
			}
		}
		scale := 0
		if len(parts) == 2 {
			scale = len(parts[1])
		}
		if max(len(strings.TrimLeft(digits, "0")), scale) > 38 {
			return nil, connections.ErrInvalid
		}
		return dpi.Decimal(v.Text), nil
	case contract.Floating:
		n, err := strconv.ParseFloat(v.Text, 64)
		if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			return nil, connections.ErrInvalid
		}
		return n, nil
	case contract.Boolean:
		if v.Text != "true" && v.Text != "false" {
			return nil, connections.ErrInvalid
		}
		return v.Text == "true", nil
	case contract.Text, contract.DateTime:
		return v.Text, nil
	case contract.Binary:
		return bytes.Clone(v.Bytes), nil
	}
	return nil, contract.ErrUnsupported
}

func dataWhere(t *contract.TableStructure, o contract.DataOptions) (string, []any, error) {
	e, err := contract.EffectiveDataExpression(o)
	if err != nil {
		return "", nil, err
	}
	if o.Condition == nil && len(o.Filters) == 0 && o.FilterMode == "" && (o.FilterColumn != "" || o.FilterValue != "") {
		return "", nil, connections.ErrInvalid
	}
	cols := dataColumns(t)
	var args []any
	where, err := contract.CompileFilter(e, func(f contract.DataFilterRule) (string, error) {
		c, ok := cols[f.Column]
		if !ok || len(f.Value) > 4096 || !utf8.ValidString(f.Value) {
			return "", connections.ErrInvalid
		}
		expr := quote(c.Name)
		if f.Mode == "is_null" {
			return expr + " IS NULL", nil
		}
		if f.Mode == "is_not_null" {
			return expr + " IS NOT NULL", nil
		}
		if c.ValueKind == contract.Binary || c.DataType == "CLOB" || c.DataType == "TEXT" {
			return "", contract.ErrUnsupported
		}
		ops := map[string]string{"equal": "=", "not_equal": "<>", "less": "<", "greater": ">", "less_equal": "<=", "greater_equal": ">=", "like": "LIKE", "not_like": "NOT LIKE"}
		op, ok := ops[f.Mode]
		if !ok {
			return "", contract.ErrUnsupported
		}
		if (f.Mode == "like" || f.Mode == "not_like") && c.ValueKind != contract.Text {
			return "", contract.ErrUnsupported
		}
		p, err := dataParameter(contract.Cell{Kind: c.ValueKind, Text: f.Value}, c.ValueKind)
		if err != nil {
			return "", err
		}
		args = append(args, p)
		return expr + " " + op + " ?", nil
	})
	if where == "" {
		where = "1=1"
	}
	return where, args, err
}

func dataOrder(t *contract.TableStructure, o contract.DataOptions, key []string) ([]contract.DataSortRule, bool, error) {
	if len(o.Sorts) > 16 || len(o.Sorts) > 0 && o.SortColumn != "" {
		return nil, false, connections.ErrInvalid
	}
	order := append([]contract.DataSortRule(nil), o.Sorts...)
	if o.SortColumn != "" {
		order = append(order, contract.DataSortRule{Column: o.SortColumn, Descending: o.SortDescending})
	}
	cols := dataColumns(t)
	seen := map[string]bool{}
	stable := len(key) != 0
	for _, term := range order {
		c, ok := cols[term.Column]
		if !ok || seen[term.Column] {
			return nil, false, connections.ErrInvalid
		}
		if c.ValueKind == contract.Binary || c.DataType == "CLOB" || c.DataType == "TEXT" {
			return nil, false, contract.ErrUnsupported
		}
		stable = stable && numericKey(c)
		seen[term.Column] = true
	}
	for _, k := range key {
		if !seen[k] {
			order = append(order, contract.DataSortRule{Column: k})
		}
	}
	return order, stable, nil
}

func (s *Session) dataSignature(t *contract.TableStructure, o contract.DataOptions, order []contract.DataSortRule, identity string) string {
	e, _ := contract.EffectiveDataExpression(o)
	v := struct {
		Scope    string
		Table    *contract.TableStructure
		Filter   *contract.FilterExpression[contract.DataFilterRule]
		Order    []contract.DataSortRule
		Identity string
	}{s.dataScope, t, e, order, identity}
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func seekWhere(t *contract.TableStructure, order []contract.DataSortRule, signature string, cursor *contract.DataCursor, args *[]any) (string, error) {
	if cursor.RuleSignature != signature || cursor.Database != t.Object.Schema || cursor.Table != t.Object.Name || len(cursor.Columns) != len(order) || len(cursor.Descending) != len(order) || len(cursor.Values) != len(order) || len(order) == 0 {
		return "", connections.ErrInvalid
	}
	cols := dataColumns(t)
	var terms []string
	for i, term := range order {
		if cursor.Columns[i] != term.Column || cursor.Descending[i] != term.Descending || len(cursor.Values[i]) > 8192 {
			return "", connections.ErrInvalid
		}
		var predicates []string
		for j := 0; j <= i; j++ {
			if len(cursor.Values[j]) > 8192 {
				return "", connections.ErrInvalid
			}
			var c contract.Cell
			decoder := json.NewDecoder(bytes.NewReader(cursor.Values[j]))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&c); err != nil {
				return "", connections.ErrInvalid
			}
			// Require exactly one canonical typed value; trailing input is invalid.
			canonical, _ := json.Marshal(c)
			if !bytes.Equal(bytes.TrimSpace(cursor.Values[j]), canonical) {
				return "", connections.ErrInvalid
			}
			p, err := dataParameter(c, cols[order[j].Column].ValueKind)
			if err != nil {
				return "", err
			}
			*args = append(*args, p)
			op := "="
			if j == i {
				op = ">"
				if term.Descending {
					op = "<"
				}
			}
			predicates = append(predicates, quote(order[j].Column)+" "+op+" ?")
		}
		terms = append(terms, "("+strings.Join(predicates, " AND ")+")")
	}
	return "(" + strings.Join(terms, " OR ") + ")", nil
}

func rowWhere(t *contract.TableStructure, key []string, ref contract.RowRef) (string, []any, error) {
	if len(key) == 0 || ref.Object != t.Object || ref.Database != t.Object.Schema || ref.Table != t.Object.Name || !reflect.DeepEqual(key, ref.Columns) || len(ref.TypedValues) != len(key) || len(ref.Values) != len(key) {
		return "", nil, connections.ErrInvalid
	}
	cols := dataColumns(t)
	var terms []string
	var args []any
	for i, k := range key {
		p, err := dataParameter(ref.TypedValues[i], cols[k].ValueKind)
		if err != nil {
			return "", nil, err
		}
		b, _ := json.Marshal(ref.TypedValues[i])
		if !bytes.Equal(b, ref.Values[i]) {
			return "", nil, connections.ErrInvalid
		}
		terms = append(terms, quote(k)+" = ?")
		args = append(args, p)
	}
	return strings.Join(terms, " AND "), args, nil
}
