package contract

import "slices"

// Graph plans belong to the original native completion. Arguments retain their
// exact kinds; child and identifier order are the server's order.
type GraphExecutionPlan struct {
	Mode, Query string
	Parameters  []GraphProperty
	Root        GraphPlanOperator
}

type GraphPlanOperator struct {
	Operator    string
	Arguments   []GraphProperty
	Identifiers []string
	Children    []GraphPlanOperator
	Profile     *GraphPlanProfile
}

// Nil means the server did not record a metric, independently of a recorded zero.
// TimeNanoseconds uses the driver's exact duration unit.
type GraphPlanProfile struct {
	Rows, DbHits, PageCacheHits, PageCacheMisses *int64
	PageCacheHitRatio                            *float64
	TimeNanoseconds                              *int64
}

func cloneGraphMetric[T int64 | float64](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func (p *GraphExecutionPlan) Clone() *GraphExecutionPlan {
	if p == nil {
		return nil
	}
	copy := *p
	copy.Parameters = cloneGraphProperties(p.Parameters)
	copy.Root = p.Root.Clone()
	return &copy
}

func (p GraphPlanOperator) Clone() GraphPlanOperator {
	p.Arguments = cloneGraphProperties(p.Arguments)
	p.Identifiers = slices.Clone(p.Identifiers)
	p.Children = slices.Clone(p.Children)
	for i := range p.Children {
		p.Children[i] = p.Children[i].Clone()
	}
	if p.Profile != nil {
		v := *p.Profile
		v.Rows, v.DbHits = cloneGraphMetric(v.Rows), cloneGraphMetric(v.DbHits)
		v.PageCacheHits, v.PageCacheMisses = cloneGraphMetric(v.PageCacheHits), cloneGraphMetric(v.PageCacheMisses)
		v.PageCacheHitRatio, v.TimeNanoseconds = cloneGraphMetric(v.PageCacheHitRatio), cloneGraphMetric(v.TimeNanoseconds)
		p.Profile = &v
	}
	return p
}
