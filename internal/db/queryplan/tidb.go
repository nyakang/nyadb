package queryplan

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type tidbOperator struct {
	ID       string          `json:"id"`
	EstRows  json.RawMessage `json:"estRows"`
	EstCost  json.RawMessage `json:"estCost"`
	Task     string          `json:"taskType"`
	Access   string          `json:"accessObject"`
	Info     string          `json:"operatorInfo"`
	Children []tidbOperator  `json:"subOperators"`
}

func parseTiDB(result contract.SQLResult) (*contract.ExecutionPlan, error) {
	sets := result.ResultSets()
	if result.Truncated || len(sets) == 0 || len(sets[0].Truncation) > 0 || len(sets[0].Rows) != 1 || len(sets[0].Rows[0]) != 1 {
		return nil, errors.New("empty, truncated or invalid TiDB execution plan")
	}
	cell := sets[0].Rows[0][0]
	if cell.Truncated {
		return nil, errors.New("TiDB execution plan was truncated")
	}
	raw := cell.Text
	if raw == "" {
		raw = string(cell.Bytes)
	}
	if len(raw) > 8<<20 {
		return nil, errors.New("TiDB execution plan exceeds size limit")
	}
	var operators []tidbOperator
	if err := json.Unmarshal([]byte(raw), &operators); err != nil {
		return nil, err
	}
	if len(operators) == 0 {
		return nil, errors.New("empty TiDB execution plan")
	}
	seen := map[string]bool{}
	var convert func(tidbOperator, int) (contract.PlanNode, error)
	convert = func(op tidbOperator, depth int) (contract.PlanNode, error) {
		if op.ID == "" || seen[op.ID] || len(seen) >= 10000 || depth > 64 {
			return contract.PlanNode{}, errors.New("invalid, duplicate or excessively deep TiDB operator")
		}
		seen[op.ID] = true
		parts := []string{}
		for _, part := range []string{op.Task, op.Access, op.Info} {
			if part != "" {
				parts = append(parts, part)
			}
		}
		n := contract.PlanNode{Label: op.ID, Detail: strings.Join(parts, "; ")}
		var rows, cost any
		if len(op.EstRows) > 0 {
			if err := json.Unmarshal(op.EstRows, &rows); err != nil {
				return n, err
			}
			n.Rows = numeric(rows)
		}
		if len(op.EstCost) > 0 {
			if err := json.Unmarshal(op.EstCost, &cost); err != nil {
				return n, err
			}
			n.Cost = numeric(cost)
		}
		for _, child := range op.Children {
			value, err := convert(child, depth+1)
			if err != nil {
				return n, err
			}
			n.Children = append(n.Children, value)
		}
		return n, nil
	}
	p := &contract.ExecutionPlan{Engine: "tidb"}
	for _, op := range operators {
		n, err := convert(op, 0)
		if err != nil {
			return nil, err
		}
		p.Nodes = append(p.Nodes, n)
	}
	return p, nil
}
