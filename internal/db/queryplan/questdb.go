package queryplan

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

func buildQuestDB(source string) (string, error) {
	const prefix = "EXPLAIN "
	if len(source) > (1<<20)-len(prefix) || !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return "", contract.ErrUnsupported
	}
	if err := sqltext.SinglePortableStatement(source); err != nil {
		return "", err
	}
	return prefix + source, nil
}

// QuestDB's text plan uses four spaces for child operators and two extra
// spaces for their attributes. Preserve native labels and properties; the
// format provides no cost estimates.
func parseQuestDB(result contract.SQLResult) (*contract.ExecutionPlan, error) {
	fail := func() (*contract.ExecutionPlan, error) {
		return nil, errors.New("invalid, truncated or unsupported QuestDB plan; see original result")
	}
	sets := result.ResultSets()
	if result.Truncated || len(sets) != 1 || len(sets[0].Columns) != 1 || !strings.EqualFold(sets[0].Columns[0].Name, "QUERY PLAN") || len(sets[0].Truncation) != 0 || len(sets[0].Rows) == 0 || len(sets[0].Rows) > 10000 {
		return fail()
	}
	type item struct {
		node     contract.PlanNode
		children []int
	}
	var items []item
	var path []int
	used := 0
	for _, row := range sets[0].Rows {
		if len(row) != 1 || row[0].Kind != contract.Text || row[0].Truncated || len(row[0].Bytes) != 0 {
			return fail()
		}
		raw := row[0].Text
		used += len(raw)
		if used > 8<<20 || !utf8.ValidString(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
			return fail()
		}
		label := strings.TrimLeft(raw, " ")
		indent := len(raw) - len(label)
		depth := indent / 4
		if indent%4 == 2 {
			if depth >= len(path) || strings.Index(label, ":") <= 0 {
				return fail()
			}
			n := &items[path[depth]].node
			if n.Detail != "" {
				n.Detail += "\n"
			}
			n.Detail += label
			path = path[:depth+1]
			continue
		}
		if indent%4 != 0 || depth > 64 || strings.TrimSpace(label) == "" || depth > len(path) || depth == 0 && len(items) != 0 {
			return fail()
		}
		index := len(items)
		items = append(items, item{node: contract.PlanNode{Label: label}})
		if depth > 0 {
			parent := &items[path[depth-1]]
			parent.children = append(parent.children, index)
		}
		path = append(path[:depth], index)
	}
	var build func(int) contract.PlanNode
	build = func(index int) contract.PlanNode {
		n := items[index].node
		for _, child := range items[index].children {
			n.Children = append(n.Children, build(child))
		}
		return n
	}
	return &contract.ExecutionPlan{Engine: "questdb", Nodes: []contract.PlanNode{build(0)}}, nil
}
