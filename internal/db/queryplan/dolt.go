package queryplan

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

func buildDolt(sql string) (string, error) {
	const prefix = "EXPLAIN FORMAT=tree "
	if len(sql) > (1<<20)-len(prefix) || !utf8.ValidString(sql) || strings.ContainsRune(sql, 0) {
		return "", errors.New("Dolt execution plan SQL exceeds display bounds")
	}
	if err := sqltext.SingleMySQLStatement(sql); err != nil {
		return "", err
	}
	return prefix + sql, nil
}

// Dolt's native go-mysql-server tree has operator and property lines. Keep
// both verbatim and reconstruct only the serializer's explicit branches;
// the tree does not provide optimizer costs or row estimates.
func parseDolt(result contract.SQLResult) (*contract.ExecutionPlan, error) {
	fail := func() (*contract.ExecutionPlan, error) {
		return nil, errors.New("invalid, truncated or unsupported Dolt plan; see original result")
	}
	sets := result.ResultSets()
	if result.Truncated || len(sets) != 1 || len(sets[0].Columns) != 1 || !strings.EqualFold(sets[0].Columns[0].Name, "plan") || len(sets[0].Truncation) != 0 || len(sets[0].Rows) == 0 || len(sets[0].Rows) > 10000 {
		return fail()
	}
	type item struct {
		node             contract.PlanNode
		children         []int
		more, finalChild bool
	}
	var items []item
	var path []int
	used := 0
	for i, row := range sets[0].Rows {
		if len(row) != 1 || row[0].Kind != contract.Text || row[0].Truncated || len(row[0].Bytes) != 0 {
			return fail()
		}
		raw := row[0].Text
		used += len(raw)
		if used > 8<<20 || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\r\n\x00\t") {
			return fail()
		}
		text, depth, more := raw, 0, false
		if i == 0 {
			if strings.TrimSpace(text) == "" || strings.HasPrefix(text, " ") || strings.HasPrefix(text, "│") || strings.HasPrefix(text, "├") || strings.HasPrefix(text, "└") {
				return fail()
			}
		} else {
			for strings.HasPrefix(text, "    ") || strings.HasPrefix(text, " │  ") {
				if depth+1 >= len(path) || depth >= 63 {
					return fail()
				}
				prefix := "    "
				if items[path[depth+1]].more {
					prefix = " │  "
				}
				if !strings.HasPrefix(text, prefix) {
					return fail()
				}
				text = strings.TrimPrefix(text, prefix)
				depth++
			}
			if strings.HasPrefix(text, " ├─ ") {
				more = true
				text = strings.TrimPrefix(text, " ├─ ")
			} else if strings.HasPrefix(text, " └─ ") {
				text = strings.TrimPrefix(text, " └─ ")
			} else {
				return fail()
			}
			depth++
			if depth > len(path) || items[path[depth-1]].finalChild || strings.TrimSpace(text) == "" {
				return fail()
			}
		}
		index := len(items)
		items = append(items, item{node: contract.PlanNode{Label: text}, more: more})
		if depth > 0 {
			parent := &items[path[depth-1]]
			parent.children = append(parent.children, index)
			parent.finalChild = !more
		}
		path = append(path[:depth], index)
	}
	for _, item := range items {
		if len(item.children) > 0 && !item.finalChild {
			return fail()
		}
	}
	var build func(int) contract.PlanNode
	build = func(index int) contract.PlanNode {
		n := items[index].node
		for _, child := range items[index].children {
			n.Children = append(n.Children, build(child))
		}
		return n
	}
	return &contract.ExecutionPlan{Engine: "dolt", Nodes: []contract.PlanNode{build(0)}}, nil
}
