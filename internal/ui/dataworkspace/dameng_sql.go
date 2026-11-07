package dataworkspace

import (
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqlexport"
)

func damengDataSQLTable(t *Document, fields []string) (*sqlexport.DamengTable, error) {
	if t == nil || t.page == nil || t.page.Object != t.object || t.page.Database != t.object.Schema || t.page.Table != t.object.Name {
		return nil, contract.ErrDataPage
	}
	columns := make([]contract.StructureColumn, len(fields))
	for i, name := range fields {
		found := false
		for _, c := range t.page.ColumnMetadata {
			if c.Name == name {
				if found {
					return nil, contract.ErrDataPage
				}
				columns[i], found = c, true
			}
		}
		if !found {
			return nil, contract.ErrDataPage
		}
	}
	return sqlexport.NewDamengTable(t.object, columns)
}

func damengDataSQLSelection(t *Document, update bool) (string, error) {
	if t == nil || t.page == nil || t.closed || t.capabilities.Documents || t.capabilities.KeyValues {
		return "", contract.ErrDataPage
	}
	page := t.page
	if len(page.Columns) != len(page.ColumnMetadata) || len(page.Columns) == 0 {
		return "", contract.ErrDataPage
	}
	for i, c := range page.ColumnMetadata {
		if c.Name != page.Columns[i] {
			return "", contract.ErrDataPage
		}
	}
	table, err := damengDataSQLTable(t, page.Columns)
	if err != nil {
		return "", err
	}
	var statements []string
	for row, cells := range page.Rows {
		if !t.selection.Has(row) && (t.selection.Len() != 0 || row != t.selectedRow) {
			continue
		}
		if len(cells) != len(page.Columns) {
			return "", contract.ErrDataPage
		}
		draft := mysqlDataDraftForRow(t, row)
		values := make([]contract.DataValue, len(cells))
		for i, c := range page.ColumnMetadata {
			if contract.DataColumnGenerated(c) && c.Extra != "IDENTITY" || update && (c.Key == "PRI" || c.Extra == "IDENTITY") {
				continue
			}
			if draft != nil {
				if v, ok := draft.Values[c.Name]; ok {
					values[i] = v
					continue
				}
				if draft.Kind == "insert" {
					values[i] = contract.DataValue{Default: true}
					continue
				}
			}
			cell := cells[i]
			if cell.Truncated || cell.Value != nil && cell.Value.Truncated {
				return "", contract.ErrDataEditLimit
			}
			if cell.Value == nil {
				return "", contract.ErrDataPage
			}
			v := cell.Value
			values[i] = contract.DataValue{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes, Null: v.Kind == contract.Null, Binary: v.Kind == contract.Binary}
		}
		var statement string
		if update {
			if row >= len(page.RowRefs) || page.RowRefs[row] == nil {
				return "", contract.ErrDataReadOnly
			}
			statement, err = table.Update(values, *page.RowRefs[row])
		} else {
			statement, err = table.Insert(values)
		}
		if err != nil {
			return "", err
		}
		statements = append(statements, statement)
	}
	if len(statements) == 0 {
		return "", contract.ErrDataPage
	}
	return strings.Join(statements, ""), nil
}
