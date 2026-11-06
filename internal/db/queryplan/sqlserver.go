package queryplan

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func parseSQLServer(result contract.SQLResult) (*contract.ExecutionPlan, error) {
	sets := result.ResultSets()
	if result.Truncated || len(sets) == 0 {
		return nil, errors.New("empty or truncated SQL Server plan")
	}
	plan := &contract.ExecutionPlan{Engine: "sqlserver"}
	count := 0
	for _, set := range sets {
		if len(set.Truncation) > 0 {
			return nil, errors.New("truncated SQL Server plan")
		}
		for _, row := range set.Rows {
			if len(row) != 1 || len(row[0].Text) > 8<<20 {
				return nil, errors.New("invalid SQL Server plan")
			}
			decoder := xml.NewDecoder(strings.NewReader(row[0].Text))
			var stack []contract.PlanNode
			root := false
			for {
				token, err := decoder.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					return nil, err
				}
				switch t := token.(type) {
				case xml.Directive:
					return nil, errors.New("SQL Server plan directives are unsupported")
				case xml.StartElement:
					if t.Name.Local == "ShowPlanXML" {
						root = true
					}
					if t.Name.Local == "RelOp" {
						count++
						if count > 10000 || len(stack) >= 64 {
							return nil, errors.New("SQL Server plan exceeds node budget")
						}
						var n contract.PlanNode
						for _, a := range t.Attr {
							switch a.Name.Local {
							case "PhysicalOp":
								n.Label = a.Value
							case "LogicalOp":
								n.Detail = a.Value
							case "EstimateRows":
								n.Rows = numeric(a.Value)
							case "EstimatedTotalSubtreeCost":
								n.Cost = numeric(a.Value)
							}
						}
						stack = append(stack, n)
					}
					if t.Name.Local == "Object" && len(stack) > 0 {
						var parts []string
						for _, a := range t.Attr {
							if a.Name.Local == "Database" || a.Name.Local == "Schema" || a.Name.Local == "Table" || a.Name.Local == "Index" {
								parts = append(parts, a.Value)
							}
						}
						if len(parts) > 0 {
							stack[len(stack)-1].Detail += " " + strings.Join(parts, ".")
						}
					}
				case xml.EndElement:
					if t.Name.Local == "RelOp" {
						if len(stack) == 0 {
							return nil, errors.New("invalid SQL Server plan nesting")
						}
						n := stack[len(stack)-1]
						stack = stack[:len(stack)-1]
						if len(stack) == 0 {
							plan.Nodes = append(plan.Nodes, n)
						} else {
							i := len(stack) - 1
							stack[i].Children = append(stack[i].Children, n)
						}
					}
				}
			}
			if !root || len(stack) != 0 {
				return nil, errors.New("invalid SQL Server showplan")
			}
		}
	}
	if len(plan.Nodes) == 0 {
		return nil, errors.New("SQL Server plan has no operators")
	}
	return plan, nil
}
