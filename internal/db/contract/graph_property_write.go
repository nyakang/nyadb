package contract

import "context"

// GraphPropertyTarget qualifies one observed node or directed relationship.
// Element IDs and labels are observations, not incarnation guarantees.
type GraphPropertyTarget struct {
	Kind, ElementID, SourceID, TargetID, RelationshipType string
	Labels                                                []string
}
type GraphPropertyWriteRequest struct {
	Namespace     NamespaceRef
	Target        GraphPropertyTarget
	Property      string
	Before, After GraphValue
}

func (r GraphPropertyWriteRequest) Clone() GraphPropertyWriteRequest {
	r.Target.Labels = append([]string(nil), r.Target.Labels...)
	r.Before, r.After = r.Before.Clone(), r.After.Clone()
	return r
}

// A receipt retains the complete original native response, even on error.
// Conflict means completed without admitting the requested replacement. The
// dependent self-assignment used to acquire a lock remains in native counters.
type GraphPropertyWriteReceipt struct {
	Query             string
	Result            GraphResult
	Applied, Conflict bool
}

func (r GraphPropertyWriteReceipt) Clone() GraphPropertyWriteReceipt {
	r.Result = r.Result.Clone()
	return r
}

type GraphPropertyWriteLease interface {
	WriteGraphProperty(context.Context, GraphPropertyWriteRequest) (GraphPropertyWriteReceipt, error)
}
