package rqlite

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type dataOrderTerm struct {
	Column             string
	Descending, Binary bool
}

func dataOrder(t tableSnapshot, o contract.DataOptions) ([]dataOrderTerm, error) {
	sorts := slices.Clone(o.Sorts)
	if len(sorts) == 0 && o.SortColumn != "" {
		sorts = append(sorts, contract.DataSortRule{Column: o.SortColumn, Descending: o.SortDescending})
	}
	if len(sorts) > contract.DataMaxSortRules {
		return nil, connections.ErrInvalid
	}
	used := map[string]bool{}
	var terms []dataOrderTerm
	for _, sort := range sorts {
		if !slices.Contains(columnNames(t), sort.Column) || used[sort.Column] {
			return nil, connections.ErrInvalid
		}
		used[sort.Column] = true
		terms = append(terms, dataOrderTerm{Column: sort.Column, Descending: sort.Descending})
	}
	// The same column can need a separate BINARY tie breaker when its declared
	// collation differs from its unique index. Keep both native order terms.
	for _, key := range t.key {
		terms = append(terms, dataOrderTerm{Column: key, Binary: true})
	}
	return terms, nil
}

func (term dataOrderTerm) expression() string {
	expr := "+" + quote(term.Column)
	if term.Binary {
		expr += " COLLATE BINARY"
	}
	return expr
}

func (s *Session) cursorMAC(scope string, t tableSnapshot, o contract.DataOptions, order []dataOrderTerm, cursor contract.DataCursor) string {
	o.Offset, o.Limit, o.UseKeyset, o.After = 0, 0, false, nil
	o.Object = t.structure.Object
	cursor.RuleSignature = ""
	message, _ := json.Marshal(struct {
		Metadata string
		Options  contract.DataOptions
		Order    []dataOrderTerm
		Cursor   contract.DataCursor
	}{s.dataMetadataSeal(t), o, order, cursor})
	h := hmac.New(sha256.New, []byte(scope))
	_, _ = h.Write(message)
	return "rqlite-v1:" + hex.EncodeToString(h.Sum(nil))
}

func (s *Session) cursorWhere(scope string, t tableSnapshot, o contract.DataOptions, order []dataOrderTerm, masked bool) (string, error) {
	c := o.After
	if c == nil {
		return "", nil
	}
	if !o.UseKeyset || len(t.key) == 0 || c.Offset < 0 || c.Database != "main" || c.Table != t.structure.Name || len(c.Columns) != len(order) || len(c.Descending) != len(order) || len(c.Values) != len(order) {
		return "", connections.ErrInvalid
	}
	total := 0
	for _, raw := range c.Values {
		total += len(raw)
		if total > maxRequestBytes {
			return "", contract.ErrDataEditLimit
		}
	}
	if !hmac.Equal([]byte(c.RuleSignature), []byte(s.cursorMAC(scope, t, o, order, *c))) {
		return "", contract.ErrDataConflict
	}
	var equal, branches []string
	statementBytes := 0
	for i, term := range order {
		if c.Columns[i] != term.Column || c.Descending[i] != term.Descending {
			return "", connections.ErrInvalid
		}
		var value contract.Cell
		if err := json.Unmarshal(c.Values[i], &value); err != nil || value.Truncated {
			return "", connections.ErrInvalid
		}
		expr, literal := term.expression(), "NULL"
		if value.Kind != contract.Null {
			var err error
			literal, _, err = dataCellLiteral(value)
			if err != nil {
				return "", err
			}
			// A CAST on either comparison operand introduces affinity and can
			// change numeric/text ordering. Native ORDER BY never applies that
			// conversion: remove affinity from both operands, retain collation.
			literal = "+(" + literal + ")"
		} else if value.Text != "" || len(value.Bytes) != 0 {
			return "", connections.ErrInvalid
		}
		after := "0"
		if value.Kind == contract.Null {
			if !term.Descending {
				after = expr + " IS NOT NULL"
			}
		} else {
			if masked {
				literal = "?"
			}
			op := ">"
			if term.Descending {
				op = "<"
			}
			after = expr + " " + op + " " + literal
			if term.Descending {
				after = "(" + after + " OR " + expr + " IS NULL)"
			}
		}
		branch := append(slices.Clone(equal), "("+after+")")
		branchSQL := "(" + strings.Join(branch, " AND ") + ")"
		statementBytes += len(branchSQL)
		if statementBytes > maxRequestBytes/2 {
			return "", contract.ErrDataEditLimit
		}
		branches = append(branches, branchSQL)
		equal = append(equal, expr+" IS "+literal)
	}
	return "(" + strings.Join(branches, " OR ") + ")", nil
}

func copyDataOptions(o contract.DataOptions) (contract.DataOptions, error) {
	expr, err := contract.EffectiveDataExpression(o)
	if err != nil {
		return contract.DataOptions{}, err
	}
	total := len(o.FilterColumn) + len(o.FilterMode) + len(o.FilterValue) + len(o.SortColumn)
	if expr != nil {
		rules, err := expr.Rules(contract.DataMaxFilterRules, 3)
		if err != nil {
			return contract.DataOptions{}, err
		}
		for _, r := range rules {
			total += len(r.Column) + len(r.Mode) + len(r.Value)
		}
	}
	if len(o.Sorts) > contract.DataMaxSortRules {
		return contract.DataOptions{}, connections.ErrInvalid
	}
	for _, sort := range o.Sorts {
		total += len(sort.Column)
	}
	if c := o.After; c != nil {
		if len(c.Columns) > 516 || len(c.Descending) > 516 || len(c.Values) > 516 {
			return contract.DataOptions{}, contract.ErrDataEditLimit
		}
		for _, name := range c.Columns {
			total += len(name)
		}
		for _, value := range c.Values {
			total += len(value)
		}
		total += len(c.RuleSignature) + len(c.Database) + len(c.Table) + len(c.FilterColumn) + len(c.FilterMode) + len(c.FilterValue) + len(c.SortColumn)
	}
	if total > maxRequestBytes {
		return contract.DataOptions{}, contract.ErrDataEditLimit
	}
	raw, err := json.Marshal(o)
	var captured contract.DataOptions
	if err != nil || json.Unmarshal(raw, &captured) != nil {
		return captured, connections.ErrInvalid
	}
	return captured, nil
}

// Extract the complete last visible boundary in the same read transaction as
// the page. The original SQL bounds its total before returning any full bytes.
func cursorProjection(t tableSnapshot, source string, order []dataOrderTerm, limit int) string {
	var ordinals, cases []string
	for i, term := range order {
		column := slices.Index(columnNames(t), term.Column)
		ordinals = append(ordinals, fmt.Sprintf("(%d)", i))
		cases = append(cases, fmt.Sprintf("WHEN %d THEN r.c%d", i, column))
	}
	value := "CASE c.i " + strings.Join(cases, " ") + " END"
	return fmt.Sprintf("WITH _nyadb_cursor_row AS MATERIALIZED (SELECT * FROM (%s) LIMIT 1 OFFSET %d),_nyadb_cursor_columns(i) AS (VALUES %s),_nyadb_cursor_values AS MATERIALIZED (SELECT c.i AS cid,typeof(%s) AS typ,%s AS v FROM _nyadb_cursor_row r CROSS JOIN _nyadb_cursor_columns c) SELECT cid,typ,CASE WHEN sum(coalesce(length(v),0)) OVER ()<=%d THEN hex(v) ELSE NULL END,length(v) FROM _nyadb_cursor_values ORDER BY cid", source, limit-1, strings.Join(ordinals, ","), value, dataBytes(value), contract.DataEditMaxBytes)
}

func decodeCursorValues(r receipt, order []dataOrderTerm) ([]contract.Cell, error) {
	rows, err := decodedRows(r, 0, 4)
	if err != nil || len(rows) != 0 && len(rows) != len(order) {
		return nil, errIncomplete
	}
	var values []contract.Cell
	total := 0
	for i, row := range rows {
		cid, err := cellInt(row[0])
		if err != nil || cid != int64(i) {
			return nil, errIncomplete
		}
		typ, err := cellText(row[1])
		if err != nil {
			return nil, errIncomplete
		}
		if typ == "null" {
			if row[3].Kind != contract.Null || row[2].Kind != contract.Null && row[2].Text != "" {
				return nil, errIncomplete
			}
			values = append(values, contract.Cell{Kind: contract.Null})
			continue
		}
		n, err := cellInt(row[3])
		if err != nil || n < 0 {
			return nil, errIncomplete
		}
		if n > contract.DataEditMaxBytes || row[2].Kind == contract.Null {
			return nil, contract.ErrDataEditLimit
		}
		raw, err := cellText(row[2])
		if err != nil || int64(len(raw)) != 2*n {
			return nil, errIncomplete
		}
		data, err := hex.DecodeString(raw)
		if err != nil {
			return nil, errIncomplete
		}
		value, err := storageDataCell(typ, data, false)
		if err != nil {
			return nil, err
		}
		total += len(value.Text) + len(value.Bytes)
		if total > contract.DataEditMaxBytes {
			return nil, contract.ErrDataEditLimit
		}
		values = append(values, value)
	}
	return values, nil
}

func (s *Session) readCursorPage(ctx context.Context, scope string, t tableSnapshot, o contract.DataOptions, source string, order []dataOrderTerm) ([]dataRow, *contract.DataCursor, error) {
	r, err := s.request(ctx, "/db/query", []any{"SELECT " + schemaGuard(t), dataProjection(source, len(t.structure.Columns), 1024, false), cursorProjection(t, source, order, o.Limit)}, true, false)
	if err != nil {
		return nil, nil, err
	}
	guard, err := decodedRows(r, 0, 1)
	if err != nil || len(guard) != 1 || guard[0][0].Text != "1" {
		return nil, nil, contract.ErrDataConflict
	}
	pageReceipt := r
	pageReceipt.Results = r.Results[1:2]
	rows, err := decodeDataRows(pageReceipt, len(t.structure.Columns), 1024, o.Limit+1, false)
	if err != nil {
		return nil, nil, err
	}
	boundaryReceipt := r
	boundaryReceipt.Results = r.Results[2:]
	values, err := decodeCursorValues(boundaryReceipt, order)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) < o.Limit {
		return rows, nil, nil
	}
	if len(values) != len(order) {
		return nil, nil, errIncomplete
	}
	cursor := &contract.DataCursor{Database: "main", Table: t.structure.Name, Offset: o.Offset + int64(o.Limit), FilterColumn: o.FilterColumn, FilterMode: o.FilterMode, FilterValue: o.FilterValue, SortColumn: o.SortColumn, SortDescending: o.SortDescending}
	for i, term := range order {
		cursor.Columns = append(cursor.Columns, term.Column)
		cursor.Descending = append(cursor.Descending, term.Descending)
		raw, err := json.Marshal(values[i])
		if err != nil {
			return nil, nil, err
		}
		cursor.Values = append(cursor.Values, raw)
	}
	cursor.RuleSignature = s.cursorMAC(scope, t, o, order, *cursor)
	next := o
	next.After, next.UseKeyset = cursor, true
	// Do not publish a boundary whose expanded lexicographic predicate cannot
	// fit a later native request, even when its original bytes fit the bound.
	if _, err := s.cursorWhere(scope, t, next, order, false); err != nil {
		return nil, nil, err
	}
	return rows, cursor, nil
}
