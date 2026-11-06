package queryplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func parseDuckDB(result contract.SQLResult) (*contract.ExecutionPlan, error) {
	sets := result.ResultSets()
	if result.Truncated || len(sets) != 1 || len(sets[0].Truncation) > 0 || len(sets[0].Rows) != 1 {
		return nil, errors.New("invalid or truncated DuckDB plan")
	}
	row := sets[0].Rows[0]
	if len(row) != 2 || row[0].Text != "physical_plan" || len(row[1].Text) > 8<<20 {
		return nil, errors.New("invalid DuckDB plan result")
	}
	var roots []json.RawMessage
	if err := json.Unmarshal([]byte(row[1].Text), &roots); err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, errors.New("empty DuckDB plan")
	}
	plan := &contract.ExecutionPlan{Engine: "duckdb"}
	count := 0
	var node func(json.RawMessage, int) (contract.PlanNode, error)
	node = func(raw json.RawMessage, depth int) (contract.PlanNode, error) {
		count++
		if count > 10000 || depth > 64 {
			return contract.PlanNode{}, errors.New("DuckDB plan exceeds node budget")
		}
		var v struct {
			Name     string            `json:"name"`
			Children []json.RawMessage `json:"children"`
			Extra    map[string]any    `json:"extra_info"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return contract.PlanNode{}, err
		}
		if v.Name == "" {
			return contract.PlanNode{}, errors.New("DuckDB plan node has no name")
		}
		n := contract.PlanNode{Label: v.Name}
		var keys []string
		for key := range v.Extra {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var detail []string
		for _, key := range keys {
			value := v.Extra[key]
			if key == "Estimated Cardinality" {
				n.Rows = numeric(value)
			}
			detail = append(detail, key+": "+fmt.Sprint(value))
		}
		n.Detail = strings.Join(detail, "; ")
		for _, child := range v.Children {
			c, err := node(child, depth+1)
			if err != nil {
				return n, err
			}
			n.Children = append(n.Children, c)
		}
		return n, nil
	}
	for _, raw := range roots {
		n, err := node(raw, 0)
		if err != nil {
			return nil, err
		}
		plan.Nodes = append(plan.Nodes, n)
	}
	return plan, nil
}
