package contract

import "slices"

// A nil Value is an unrecorded native metric. Values preserve exact integers;
// Unit identifies durations without assigning them to the estimated cost.
type PlanMetric struct {
	Name, Unit string
	Value      *GraphValue
}

// Optional metrics retain unknown server values. Estimates are never inferred
// from a different node or fabricated from execution time.
type PlanNode struct {
	Label, Detail string
	Rows, Cost    *float64
	Metrics       []PlanMetric
	Children      []PlanNode
}

func (p *ExecutionPlan) Clone() *ExecutionPlan {
	if p == nil {
		return nil
	}
	var clone func(PlanNode) PlanNode
	clone = func(n PlanNode) PlanNode {
		n.Rows, n.Cost = cloneGraphMetric(n.Rows), cloneGraphMetric(n.Cost)
		n.Metrics = slices.Clone(n.Metrics)
		for i := range n.Metrics {
			if v := n.Metrics[i].Value; v != nil {
				copy := v.Clone()
				n.Metrics[i].Value = &copy
			}
		}
		n.Children = slices.Clone(n.Children)
		for i := range n.Children {
			n.Children[i] = clone(n.Children[i])
		}
		return n
	}
	copy := &ExecutionPlan{Engine: p.Engine, Nodes: slices.Clone(p.Nodes)}
	for i := range copy.Nodes {
		copy.Nodes[i] = clone(copy.Nodes[i])
	}
	return copy
}

type ExecutionPlan struct {
	Engine string
	Nodes  []PlanNode
}
