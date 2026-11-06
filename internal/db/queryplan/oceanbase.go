package queryplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// OceanBase returns JSON as one text row per line, rather than one JSON cell.
func parseOceanBase(result contract.SQLResult) (*contract.ExecutionPlan, error) {
	sets := result.ResultSets()
	if result.Truncated || len(sets) != 1 || len(sets[0].Truncation) != 0 || len(sets[0].Rows) == 0 {
		return nil, errors.New("empty or truncated OceanBase execution plan")
	}
	var raw strings.Builder
	for _, row := range sets[0].Rows {
		if len(row) != 1 || row[0].Truncated || row[0].Kind == contract.Null {
			return nil, errors.New("invalid OceanBase plan row")
		}
		raw.WriteString(row[0].Text)
		raw.WriteByte('\n')
		if raw.Len() > 8<<20 {
			return nil, errors.New("OceanBase plan exceeds size limit")
		}
	}
	seen := map[int]bool{}
	var convert func(json.RawMessage, int) (contract.PlanNode, error)
	convert = func(data json.RawMessage, depth int) (contract.PlanNode, error) {
		var fields map[string]json.RawMessage
		if depth > 64 || len(seen) >= 10000 {
			return contract.PlanNode{}, errors.New("OceanBase plan exceeds node or depth limit")
		}
		if err := json.Unmarshal(data, &fields); err != nil {
			return contract.PlanNode{}, err
		}
		var id int
		var operator, name, output string
		if string(fields["ID"]) == "null" || json.Unmarshal(fields["ID"], &id) != nil || id < 0 || seen[id] || json.Unmarshal(fields["OPERATOR"], &operator) != nil || operator == "" {
			return contract.PlanNode{}, errors.New("invalid or duplicate OceanBase operator")
		}
		seen[id] = true
		_ = json.Unmarshal(fields["NAME"], &name)
		_ = json.Unmarshal(fields["output"], &output)
		n := contract.PlanNode{Label: fmt.Sprintf("%d · %s", id, operator)}
		var rows, micros any
		if value, ok := fields["EST.ROWS"]; ok {
			if json.Unmarshal(value, &rows) != nil || numeric(rows) == nil {
				return n, errors.New("invalid OceanBase row estimate")
			}
			n.Rows = numeric(rows)
		}
		parts := []string{}
		if name != "" {
			parts = append(parts, name)
		}
		if value, ok := fields["EST.TIME(us)"]; ok {
			if json.Unmarshal(value, &micros) != nil || numeric(micros) == nil {
				return n, errors.New("invalid OceanBase time estimate")
			}
			parts = append(parts, fmt.Sprintf("EST.TIME(us)=%g", *numeric(micros)))
		}
		if output != "" {
			parts = append(parts, output)
		}
		n.Detail = strings.Join(parts, "; ")
		children := map[int]json.RawMessage{}
		order := []int{}
		for key, child := range fields {
			if strings.HasPrefix(key, "CHILD_") {
				index, err := strconv.Atoi(strings.TrimPrefix(key, "CHILD_"))
				if err != nil || index < 1 || children[index] != nil {
					return n, errors.New("invalid OceanBase child identity")
				}
				children[index] = child
				order = append(order, index)
			}
		}
		sort.Ints(order)
		for _, index := range order {
			child, err := convert(children[index], depth+1)
			if err != nil {
				return n, err
			}
			n.Children = append(n.Children, child)
		}
		return n, nil
	}
	root, err := convert(json.RawMessage(raw.String()), 0)
	if err != nil {
		return nil, err
	}
	return &contract.ExecutionPlan{Engine: "oceanbase", Nodes: []contract.PlanNode{root}}, nil
}
