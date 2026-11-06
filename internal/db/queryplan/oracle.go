package queryplan

import (
	"crypto/rand"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"strconv"
	"strings"
)

func buildOracle(source string) (string, error) {
	if err := sqltext.SingleOracleStatement(source); err != nil {
		return "", err
	}
	parts, err := sqltext.SplitOracle(source)
	if err != nil {
		return "", err
	}
	id := rand.Text()
	return "EXPLAIN PLAN SET STATEMENT_ID='" + id + "' FOR " + parts[0] + ";\nSELECT ID,PARENT_ID,OPERATION,OPTIONS,OBJECT_OWNER,OBJECT_NAME,CARDINALITY,COST,ACCESS_PREDICATES,FILTER_PREDICATES FROM PLAN_TABLE WHERE STATEMENT_ID='" + id + "' ORDER BY ID;\nDELETE FROM PLAN_TABLE WHERE STATEMENT_ID='" + id + "'", nil
}
func parseOracle(result contract.SQLResult) (*contract.ExecutionPlan, error) {
	sets := result.ResultSets()
	if result.Truncated || len(sets) != 1 || len(sets[0].Rows) == 0 || len(sets[0].Rows) > 10000 || len(sets[0].Truncation) > 0 {
		return nil, errors.New("empty or truncated Oracle execution plan")
	}
	nodes := map[int]contract.PlanNode{}
	parents := map[int]int{}
	var order []int
	for _, row := range sets[0].Rows {
		if len(row) != 10 {
			return nil, errors.New("invalid Oracle execution plan")
		}
		for _, c := range row {
			if c.Truncated {
				return nil, errors.New("truncated Oracle plan cell")
			}
		}
		id, err := strconv.Atoi(row[0].Text)
		if err != nil {
			return nil, err
		}
		if _, ok := nodes[id]; ok {
			return nil, errors.New("duplicate Oracle plan node")
		}
		parent := -1
		if row[1].Kind != contract.Null {
			parent, err = strconv.Atoi(row[1].Text)
			if err != nil {
				return nil, err
			}
		}
		n := contract.PlanNode{Label: strings.TrimSpace(row[2].Text + " " + row[3].Text), Rows: numeric(row[6].Text), Cost: numeric(row[7].Text)}
		if row[5].Kind != contract.Null {
			n.Detail = row[4].Text + "." + row[5].Text
		}
		for i := 8; i < 10; i++ {
			if row[i].Kind != contract.Null {
				n.Detail += "\n" + row[i].Text
			}
		}
		nodes[id] = n
		parents[id] = parent
		order = append(order, id)
	}
	children := map[int][]int{}
	for _, id := range order {
		p := parents[id]
		if p >= 0 {
			if _, ok := nodes[p]; !ok {
				return nil, errors.New("missing Oracle plan parent")
			}
			children[p] = append(children[p], id)
		}
	}
	seen := map[int]bool{}
	var build func(int, int) (contract.PlanNode, error)
	build = func(id, depth int) (contract.PlanNode, error) {
		n := nodes[id]
		if seen[id] || depth > 64 {
			return n, errors.New("cyclic or deep Oracle execution plan")
		}
		seen[id] = true
		for _, child := range children[id] {
			v, err := build(child, depth+1)
			if err != nil {
				return n, err
			}
			n.Children = append(n.Children, v)
		}
		return n, nil
	}
	out := &contract.ExecutionPlan{Engine: "oracle"}
	for _, id := range order {
		if parents[id] < 0 {
			n, err := build(id, 0)
			if err != nil {
				return nil, err
			}
			out.Nodes = append(out.Nodes, n)
		}
	}
	if len(seen) != len(nodes) {
		return nil, errors.New("cyclic Oracle execution plan")
	}
	return out, nil
}
