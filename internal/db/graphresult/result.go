// Package graphresult projects retained native graph results for the shared grid.
package graphresult

import (
	"encoding/base64"
	"encoding/json"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func Project(source string, r contract.GraphResult) contract.SQLResult {
	copy := r.Clone()
	result := contract.SQLResult{AffectedRows: -1, Graph: &copy}
	if !r.Attempted {
		return result
	}
	state := "uncertain"
	if r.Completed {
		state = "completed"
	}
	result.Steps = []contract.StatementOutcome{{SQL: source, State: state, AffectedRows: -1}}
	if len(r.Columns) == 0 {
		return result
	}
	set := contract.ResultSet{AffectedRows: -1, Columns: make([]contract.Column, len(r.Columns))}
	for i, k := range r.Columns {
		set.Columns[i] = contract.Column{Name: k, DatabaseType: "Neo4j native value", Nullable: true}
	}
	for _, row := range r.Rows {
		cells := make([]contract.Cell, len(row))
		for i, v := range row {
			cells[i] = graphCell(v)
		}
		set.Rows = append(set.Rows, cells)
	}
	if r.Truncated {
		set.Truncation = []string{"native retention"}
	}
	set = nodeProperties(set, copy)
	result.Sets = []contract.ResultSet{set}
	return result
}
func graphCell(v contract.GraphValue) contract.Cell {
	switch v.Kind {
	case "null":
		return contract.Cell{Kind: contract.Null}
	case "string":
		return contract.Cell{Kind: contract.Text, Text: v.Text}
	case "integer":
		return contract.Cell{Kind: contract.Integer, Text: v.Text}
	case "float":
		return contract.Cell{Kind: contract.Floating, Text: v.Text}
	case "boolean":
		return contract.Cell{Kind: contract.Boolean, Text: v.Text}
	case "bytes":
		raw, _ := base64.StdEncoding.DecodeString(v.Text)
		if raw == nil {
			raw = []byte{}
		}
		return contract.Cell{Kind: contract.Binary, Bytes: raw}
	case "date", "datetime", "local_datetime", "time", "local_time":
		return contract.Cell{Kind: contract.DateTime, Text: v.Text}
	default:
		raw, _ := json.Marshal(v)
		return contract.Cell{Kind: contract.JSON, Text: string(raw)}
	}
}
