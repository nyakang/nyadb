package queryplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"strconv"
)

func Build(engine, sql string) (string, error) {
	if engine == "oceanbase" {
		if err := sqltext.SingleMySQLStatement(sql); err != nil {
			return "", err
		}
		return "EXPLAIN FORMAT=JSON " + sql, nil
	}
	if engine == "tidb" {
		if err := sqltext.SingleMySQLStatement(sql); err != nil {
			return "", err
		}
		return "EXPLAIN FORMAT=tidb_json " + sql, nil
	}
	if engine == "oracle" {
		return buildOracle(sql)
	}
	if engine == "duckdb" {
		if err := sqltext.SingleDuckDBStatement(sql); err != nil {
			return "", err
		}
		return "EXPLAIN (FORMAT JSON) " + sql, nil
	}
	if engine == "sqlserver" {
		if err := sqltext.SingleSQLServerStatement(sql); err != nil {
			return "", err
		}
		return "SET SHOWPLAN_XML ON\nGO\n" + sql + "\nGO\nSET SHOWPLAN_XML OFF", nil
	}
	validate := sqltext.SinglePortableStatement
	if engine == "mysql" || engine == "mariadb" {
		validate = sqltext.SingleMySQLStatement
	}
	if err := validate(sql); err != nil {
		return "", err
	}
	switch engine {
	case "mysql", "mariadb":
		return "EXPLAIN FORMAT=JSON " + sql, nil
	case "postgresql", "postgres":
		return "EXPLAIN (FORMAT JSON) " + sql, nil
	case "sqlite":
		return "EXPLAIN QUERY PLAN " + sql, nil
	default:
		return "", contract.ErrUnsupported
	}
}
func numeric(v any) *float64 {
	var n float64
	var err error
	switch x := v.(type) {
	case float64:
		n = x
	case string:
		n, err = strconv.ParseFloat(x, 64)
	default:
		return nil
	}
	if err != nil {
		return nil
	}
	return &n
}
func Parse(engine string, result contract.SQLResult) (*contract.ExecutionPlan, error) {
	if engine == "oceanbase" {
		return parseOceanBase(result)
	}
	if engine == "tidb" {
		return parseTiDB(result)
	}
	if engine == "oracle" {
		return parseOracle(result)
	}
	if engine == "duckdb" {
		return parseDuckDB(result)
	}
	if engine == "sqlserver" {
		return parseSQLServer(result)
	}
	sets := result.ResultSets()
	if len(sets) == 0 {
		return nil, errors.New("empty execution plan")
	}
	if result.Truncated || len(sets[0].Truncation) > 0 {
		return nil, errors.New("execution plan was truncated")
	}
	plan := &contract.ExecutionPlan{Engine: engine}
	if engine == "sqlite" {
		nodes := map[int]contract.PlanNode{}
		parents := map[int]int{}
		order := []int{}
		if len(sets[0].Rows) > 10000 {
			return nil, errors.New("execution plan exceeds node budget")
		}
		for _, row := range sets[0].Rows {
			if len(row) < 4 {
				return nil, errors.New("invalid SQLite plan")
			}
			id, e := strconv.Atoi(row[0].Text)
			if e != nil {
				return nil, e
			}
			parent, e := strconv.Atoi(row[1].Text)
			if e != nil {
				return nil, e
			}
			if _, exists := nodes[id]; exists {
				return nil, errors.New("duplicate SQLite plan node")
			}
			nodes[id] = contract.PlanNode{Label: row[3].Text}
			parents[id] = parent
			order = append(order, id)
		}
		children := map[int][]int{}
		for _, id := range order {
			if parents[id] != id {
				children[parents[id]] = append(children[parents[id]], id)
			}
		}
		visited := map[int]bool{}
		var build func(int, int) (contract.PlanNode, error)
		build = func(id, depth int) (contract.PlanNode, error) {
			node := nodes[id]
			if depth > 64 || visited[id] {
				return node, errors.New("cyclic or excessively deep SQLite plan")
			}
			visited[id] = true
			for _, child := range children[id] {
				n, err := build(child, depth+1)
				if err != nil {
					return node, err
				}
				node.Children = append(node.Children, n)
			}
			return node, nil
		}
		for _, id := range order {
			if _, exists := nodes[parents[id]]; !exists || parents[id] == id {
				node, err := build(id, 0)
				if err != nil {
					return nil, err
				}
				plan.Nodes = append(plan.Nodes, node)
			}
		}
		if len(plan.Nodes) == 0 || len(visited) != len(nodes) {
			return nil, errors.New("cyclic SQLite plan")
		}
		return plan, nil
	}
	if len(sets[0].Rows) == 0 || len(sets[0].Rows[0]) == 0 {
		return nil, errors.New("empty JSON plan")
	}
	cell := sets[0].Rows[0][0]
	if cell.Truncated {
		return nil, errors.New("execution plan was truncated")
	}
	raw := cell.Text
	if raw == "" && len(cell.Bytes) > 0 {
		raw = string(cell.Bytes)
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, err
	}
	count := 0
	var postgres func(map[string]any, int) (contract.PlanNode, error)
	postgres = func(m map[string]any, depth int) (contract.PlanNode, error) {
		count++
		if count > 10000 || depth > 64 {
			return contract.PlanNode{}, errors.New("plan exceeds display bounds")
		}
		label, _ := m["Node Type"].(string)
		if label == "" {
			return contract.PlanNode{}, errors.New("missing PostgreSQL node type")
		}
		n := contract.PlanNode{Label: label, Rows: numeric(m["Plan Rows"]), Cost: numeric(m["Total Cost"])}
		for _, key := range []string{"Relation Name", "Alias", "Index Name", "Filter", "Index Cond", "Join Filter"} {
			if v, ok := m[key]; ok {
				n.Detail += fmt.Sprint(key, ": ", v, "\n")
			}
		}
		if children, ok := m["Plans"].([]any); ok {
			for _, child := range children {
				c, ok := child.(map[string]any)
				if !ok {
					return n, errors.New("invalid PostgreSQL child")
				}
				node, err := postgres(c, depth+1)
				if err != nil {
					return n, err
				}
				n.Children = append(n.Children, node)
			}
		}
		return n, nil
	}
	if engine == "postgresql" || engine == "postgres" {
		array, ok := value.([]any)
		if !ok || len(array) == 0 {
			return nil, errors.New("invalid PostgreSQL plan envelope")
		}
		root, ok := array[0].(map[string]any)
		if !ok {
			return nil, errors.New("invalid PostgreSQL plan")
		}
		m, ok := root["Plan"].(map[string]any)
		if !ok {
			return nil, errors.New("missing PostgreSQL Plan")
		}
		node, err := postgres(m, 0)
		if err != nil {
			return nil, err
		}
		plan.Nodes = []contract.PlanNode{node}
		return plan, nil
	}
	if engine != "mysql" && engine != "mariadb" {
		return nil, contract.ErrUnsupported
	}
	var mysql func(string, any, int) ([]contract.PlanNode, error)
	mysql = func(key string, v any, depth int) ([]contract.PlanNode, error) {
		if depth > 64 || count > 10000 {
			return nil, errors.New("plan exceeds display bounds")
		}
		switch m := v.(type) {
		case []any:
			out := []contract.PlanNode{}
			for _, child := range m {
				nodes, err := mysql(key, child, depth+1)
				if err != nil {
					return nil, err
				}
				out = append(out, nodes...)
			}
			return out, nil
		case map[string]any:
			count++
			n := contract.PlanNode{Label: key, Rows: numeric(m["rows_produced_per_join"])}
			if n.Rows == nil {
				n.Rows = numeric(m["rows_examined_per_scan"])
			}
			if n.Rows == nil && engine == "mariadb" {
				n.Rows = numeric(m["rows"])
			}
			if name, ok := m["table_name"].(string); ok {
				n.Label = name
				if access, ok := m["access_type"].(string); ok {
					n.Label += " · " + access
				}
			}
			if cost, ok := m["cost_info"].(map[string]any); ok {
				n.Cost = numeric(cost["query_cost"])
				if n.Cost == nil {
					n.Cost = numeric(cost["read_cost"])
				}
			}
			keys := []string{"query_block", "nested_loop", "table", "ordering_operation", "grouping_operation", "duplicates_removal", "union_result", "query_specifications", "materialized_from_subquery", "attached_subqueries", "dependent_subquery", "buffer_result"}
			if engine == "mariadb" {
				keys = append(keys, "read_sorted_file", "filesort", "temporary_table", "materialized", "subqueries", "expression_cache", "block-nl-join", "query_specification")
			}
			for _, childKey := range keys {
				if child, ok := m[childKey]; ok {
					nodes, err := mysql(childKey, child, depth+1)
					if err != nil {
						return nil, err
					}
					n.Children = append(n.Children, nodes...)
				}
			}
			for _, detail := range []string{"key", "attached_condition", "using_filesort", "using_temporary_table", "message"} {
				if v, ok := m[detail]; ok {
					n.Detail += fmt.Sprint(detail, ": ", v, "\n")
				}
			}
			return []contract.PlanNode{n}, nil
		default:
			return nil, nil
		}
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("invalid MySQL plan")
	}
	if _, ok := root["query_block"]; !ok {
		return nil, errors.New("missing MySQL query block")
	}
	nodes, err := mysql("plan", root, 0)
	if err != nil {
		return nil, err
	}
	plan.Nodes = nodes
	return plan, nil
}
