package neo4j

import (
	"encoding/json"
	"math"
	"slices"
	"unicode/utf8"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

const nativePlanMaxNodes = 4096
const nativePlanMaxDepth = 64

type nativePlanCapture struct {
	budget *valueBudget
	nodes  int
}

func (c *nativePlanCapture) operator(name string, arguments map[string]any, identifiers []string, depth, children int) (contract.GraphPlanOperator, error) {
	c.nodes++
	if depth > nativePlanMaxDepth || c.nodes > nativePlanMaxNodes || children > nativePlanMaxNodes || len(identifiers) > 100000 {
		return contract.GraphPlanOperator{}, ErrBounds
	}
	if name == "" || !utf8.ValidString(name) {
		return contract.GraphPlanOperator{}, ErrProtocol
	}
	if err := c.budget.add(len(name) + 128); err != nil {
		return contract.GraphPlanOperator{}, err
	}
	for _, id := range identifiers {
		if !utf8.ValidString(id) {
			return contract.GraphPlanOperator{}, ErrProtocol
		}
		if err := c.budget.add(len(id) + 16); err != nil {
			return contract.GraphPlanOperator{}, err
		}
	}
	args, _, err := c.budget.properties(arguments, true, 0)
	return contract.GraphPlanOperator{Operator: name, Arguments: args, Identifiers: slices.Clone(identifiers)}, err
}

func (c *nativePlanCapture) plan(p nd.Plan, depth int) (contract.GraphPlanOperator, error) {
	if p == nil {
		return contract.GraphPlanOperator{}, ErrProtocol
	}
	children := p.Children()
	n, err := c.operator(p.Operator(), p.Arguments(), p.Identifiers(), depth, len(children))
	if err != nil {
		return n, err
	}
	for _, child := range children {
		v, err := c.plan(child, depth+1)
		if err != nil {
			return n, err
		}
		n.Children = append(n.Children, v)
	}
	return n, nil
}

func recordedInteger(value int64, recorded bool) (*int64, error) {
	if !recorded {
		return nil, nil
	}
	if value < 0 {
		return nil, ErrProtocol
	}
	return &value, nil
}

func (c *nativePlanCapture) profile(p nd.QueryProfile, depth int) (contract.GraphPlanOperator, error) {
	if p == nil {
		return contract.GraphPlanOperator{}, ErrProtocol
	}
	children := p.Children()
	n, err := c.operator(p.Operator(), p.Arguments(), p.Identifiers(), depth, len(children))
	if err != nil {
		return n, err
	}
	m := &contract.GraphPlanProfile{}
	for _, field := range []struct {
		target **int64
		read   func() (int64, bool)
	}{{&m.Rows, p.Rows}, {&m.DbHits, p.DbHits}, {&m.PageCacheHits, p.PageCacheHits}, {&m.PageCacheMisses, p.PageCacheMisses}} {
		*field.target, err = recordedInteger(field.read())
		if err != nil {
			return n, err
		}
	}
	if value, ok := p.PageCacheHitRatio(); ok {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return n, ErrProtocol
		}
		m.PageCacheHitRatio = &value
	}
	if value, ok := p.Time(); ok {
		m.TimeNanoseconds, err = recordedInteger(int64(value), true)
		if err != nil {
			return n, err
		}
	}
	n.Profile = m
	for _, child := range children {
		v, err := c.profile(child, depth+1)
		if err != nil {
			return n, err
		}
		n.Children = append(n.Children, v)
	}
	return n, nil
}

func captureNativePlan(summary nd.ResultSummary, budget *valueBudget, retained int, prepared Plan) (*contract.GraphExecutionPlan, error) {
	c := nativePlanCapture{budget: budget}
	p := &contract.GraphExecutionPlan{}
	var err error
	if profile := summary.QueryProfile(); profile != nil {
		p.Mode = "profile"
		p.Root, err = c.profile(profile, 0)
	} else if plan := summary.Plan(); plan != nil {
		p.Mode = "explain"
		p.Root, err = c.plan(plan, 0)
	} else {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Query, p.Parameters = prepared.Query(), prepared.Parameters()
	if err = budget.add(len(p.Query)); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p)
	if err != nil || retained+len(raw) > budget.limit {
		return nil, ErrBounds
	}
	return p, nil
}
