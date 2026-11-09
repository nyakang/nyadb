package queryplan

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type planArgumentBudget struct{ bytes, items int }

func (b *planArgumentBudget) add(n int) error {
	b.bytes += n
	b.items++
	if b.bytes > 16<<20 || b.items > 100000 {
		return errors.New("native plan exceeds retained bounds")
	}
	return nil
}

func validatePlanArgument(v contract.GraphValue, depth int, budget *planArgumentBudget) error {
	if err := budget.add(len(v.Kind) + len(v.Text) + 128); err != nil {
		return err
	}
	if depth > 64 || !utf8.ValidString(v.Text) {
		return errors.New("invalid native plan argument")
	}
	if v.Node != nil && v.Kind != "node" || v.Relationship != nil && v.Kind != "relationship" || v.Path != nil && v.Kind != "path" || len(v.Values) > 0 && v.Kind != "list" {
		return errors.New("inconsistent native plan argument")
	}
	switch v.Kind {
	case "integer":
		if _, err := strconv.ParseInt(v.Text, 10, 64); err != nil {
			return err
		}
	case "float":
		if _, err := strconv.ParseFloat(v.Text, 64); err != nil {
			return err
		}
	case "boolean":
		if v.Text != "true" && v.Text != "false" {
			return errors.New("invalid native plan boolean")
		}
	case "bytes":
		if _, err := base64.StdEncoding.DecodeString(v.Text); err != nil {
			return err
		}
	case "null", "string", "list", "map", "date", "local_time", "local_datetime", "time", "datetime", "duration", "point2d", "point3d":
	case "node":
		if v.Node == nil {
			return errors.New("missing native plan node")
		}
	case "relationship":
		if v.Relationship == nil {
			return errors.New("missing native plan relationship")
		}
	case "path":
		if v.Path == nil {
			return errors.New("missing native plan path")
		}
	default:
		return errors.New("unsupported native plan argument kind")
	}
	for _, child := range v.Values {
		if err := validatePlanArgument(child, depth+1, budget); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, p := range v.Properties {
		if err := budget.add(len(p.Name) + 16); err != nil {
			return err
		}
		if !utf8.ValidString(p.Name) || seen[p.Name] {
			return errors.New("invalid native plan argument property")
		}
		seen[p.Name] = true
		if err := validatePlanArgument(p.Value, depth+1, budget); err != nil {
			return err
		}
	}
	if v.Node != nil {
		if err := budget.add(len(v.Node.ElementID) + 32); err != nil {
			return err
		}
		for _, label := range v.Node.Labels {
			if err := budget.add(len(label) + 16); err != nil {
				return err
			}
			if !utf8.ValidString(label) {
				return errors.New("invalid native plan node label")
			}
		}
		if !utf8.ValidString(v.Node.ElementID) {
			return errors.New("invalid native plan node identity")
		}
		if err := validatePlanArgument(contract.GraphValue{Kind: "map", Properties: v.Node.Properties}, depth+1, budget); err != nil {
			return err
		}
	}
	if v.Relationship != nil {
		r := v.Relationship
		for _, text := range []string{r.ElementID, r.StartElementID, r.EndElementID, r.Type} {
			if err := budget.add(len(text) + 16); err != nil {
				return err
			}
			if !utf8.ValidString(text) {
				return errors.New("invalid native plan relationship identity")
			}
		}
		if err := validatePlanArgument(contract.GraphValue{Kind: "map", Properties: r.Properties}, depth+1, budget); err != nil {
			return err
		}
	}
	if v.Path != nil {
		for i := range v.Path.Nodes {
			if err := validatePlanArgument(contract.GraphValue{Kind: "node", Node: &v.Path.Nodes[i]}, depth+1, budget); err != nil {
				return err
			}
		}
		for i := range v.Path.Relationships {
			if err := validatePlanArgument(contract.GraphValue{Kind: "relationship", Relationship: &v.Path.Relationships[i]}, depth+1, budget); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseNeo4j(result contract.SQLResult) (*contract.ExecutionPlan, error) {
	r := result.Graph
	if r == nil || !r.Attempted || !r.Completed || r.Uncertain || r.Summary.PlanCaptureError != "" || r.Summary.Plan == nil {
		return nil, errors.New("missing complete native Neo4j plan")
	}
	p := r.Summary.Plan
	if p.Mode != "explain" && p.Mode != "profile" || strings.TrimSpace(p.Query) == "" || !utf8.ValidString(p.Query) {
		return nil, errors.New("invalid native Neo4j plan mode")
	}
	// Row truncation is independent of a completely captured native plan.
	// Validate the tree before encoding so excessive nesting never reaches JSON.
	nodes := 0
	budget := &planArgumentBudget{}
	if err := budget.add(len(p.Query) + 128); err != nil {
		return nil, err
	}
	parameters := map[string]bool{}
	for _, parameter := range p.Parameters {
		if !utf8.ValidString(parameter.Name) || parameters[parameter.Name] {
			return nil, errors.New("invalid native plan parameter")
		}
		parameters[parameter.Name] = true
		if err := budget.add(len(parameter.Name) + 16); err != nil {
			return nil, err
		}
		if err := validatePlanArgument(parameter.Value, 0, budget); err != nil {
			return nil, err
		}
	}
	var project func(contract.GraphPlanOperator, int) (contract.PlanNode, error)
	project = func(o contract.GraphPlanOperator, depth int) (contract.PlanNode, error) {
		if err := budget.add(len(o.Operator) + 128); err != nil {
			return contract.PlanNode{}, err
		}
		nodes++
		if depth > 64 || nodes > 4096 || len(o.Children) > 4096 || strings.TrimSpace(o.Operator) == "" || !utf8.ValidString(o.Operator) || (p.Mode == "profile") != (o.Profile != nil) {
			return contract.PlanNode{}, errors.New("incomplete or oversized native Neo4j operator tree")
		}
		n := contract.PlanNode{Label: o.Operator}
		for _, id := range o.Identifiers {
			if err := budget.add(len(id) + 16); err != nil {
				return n, err
			}
			if !utf8.ValidString(id) {
				return n, errors.New("invalid native plan identifier")
			}
		}
		seen := map[string]bool{}
		for _, arg := range o.Arguments {
			if err := budget.add(len(arg.Name) + 16); err != nil {
				return n, err
			}
			if !utf8.ValidString(arg.Name) || seen[arg.Name] {
				return n, errors.New("invalid native plan argument name")
			}
			seen[arg.Name] = true
			if err := validatePlanArgument(arg.Value, 0, budget); err != nil {
				return n, err
			}
			if arg.Name == "EstimatedRows" {
				if arg.Value.Kind != "float" && arg.Value.Kind != "integer" {
					return n, errors.New("invalid native estimated rows kind")
				}
				rows, err := strconv.ParseFloat(arg.Value.Text, 64)
				if err != nil || math.IsInf(rows, 0) || math.IsNaN(rows) || rows < 0 {
					return n, errors.New("invalid native estimated rows")
				}
				n.Rows = &rows
			}
		}
		if m := o.Profile; m != nil {
			for _, metric := range []struct {
				name, unit string
				value      *int64
			}{{"actual_rows", "", m.Rows}, {"db_hits", "", m.DbHits}, {"page_cache_hits", "", m.PageCacheHits}, {"page_cache_misses", "", m.PageCacheMisses}, {"time", "ns", m.TimeNanoseconds}} {
				x := contract.PlanMetric{Name: metric.name, Unit: metric.unit}
				if metric.value != nil {
					if *metric.value < 0 {
						return n, errors.New("invalid native runtime metric")
					}
					x.Value = &contract.GraphValue{Kind: "integer", Text: strconv.FormatInt(*metric.value, 10)}
				}
				n.Metrics = append(n.Metrics, x)
			}
			x := contract.PlanMetric{Name: "page_cache_ratio"}
			if m.PageCacheHitRatio != nil {
				v := *m.PageCacheHitRatio
				if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
					return n, errors.New("invalid native cache ratio")
				}
				x.Value = &contract.GraphValue{Kind: "float", Text: strconv.FormatFloat(v, 'g', -1, 64)}
			}
			n.Metrics = append(n.Metrics, x)
		}
		detail, err := json.Marshal(struct {
			Identifiers []string
			Arguments   []contract.GraphProperty
		}{o.Identifiers, o.Arguments})
		if err != nil || len(detail) > 16<<20 {
			return n, errors.New("oversized native plan details")
		}
		n.Detail = string(detail)
		for _, child := range o.Children {
			v, err := project(child, depth+1)
			if err != nil {
				return n, err
			}
			n.Children = append(n.Children, v)
		}
		return n, nil
	}
	n, err := project(p.Root, 0)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 16<<20 {
		return nil, errors.New("native Neo4j plan exceeds retained bounds")
	}
	return &contract.ExecutionPlan{Engine: "neo4j", Nodes: []contract.PlanNode{n}}, nil
}
