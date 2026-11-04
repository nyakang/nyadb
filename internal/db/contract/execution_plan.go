package contract

// Optional metrics retain unknown server values. Estimates are never inferred
// from a different node or fabricated from execution time.
type PlanNode struct {
	Label, Detail string
	Rows, Cost    *float64
	Children      []PlanNode
}
type ExecutionPlan struct {
	Engine string
	Nodes  []PlanNode
}
