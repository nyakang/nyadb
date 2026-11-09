package contract

import "context"

// GraphPropertyTarget qualifies one observed node or directed relationship.
// Element IDs and labels are observations, not incarnation guarantees.
type GraphPropertyTarget struct {
	Kind, ElementID, SourceID, TargetID, RelationshipType string
	Labels                                                []string
}
type GraphPropertyWriteRequest struct {
	// Before=null means absent. After=null removes the property. Stored Neo4j
	// properties cannot contain null; empty strings are ordinary present values.
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

// Optional preview validation is synchronous and performs no I/O. A bound
// provider can reject its own unsupported components/ranges before dispatch.
type GraphPropertyWriteValidator interface {
	ValidateGraphPropertyWrite(GraphPropertyWriteRequest) error
}
