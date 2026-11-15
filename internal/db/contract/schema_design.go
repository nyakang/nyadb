package contract

import (
	"context"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type SchemaDesignTarget struct {
	Object     ObjectRef
	Generation uint64
}
type SchemaDesignSnapshot struct {
	Target SchemaDesignTarget
	Schema schemaddl.Snapshot
}

func (s SchemaDesignSnapshot) Clone() SchemaDesignSnapshot { s.Schema = s.Schema.Clone(); return s }

type SchemaDesignRequest struct {
	Original        SchemaDesignSnapshot
	Desired         schemaddl.Table
	AllowColumnDrop bool
}

func (r SchemaDesignRequest) Clone() SchemaDesignRequest {
	r.Original = r.Original.Clone()
	r.Desired = r.Desired.Clone()
	return r
}

type SchemaCopyRequest struct {
	Original                                      SchemaDesignSnapshot
	Destination                                   string
	WithData, RebindSelfReferences, PreserveRowID bool
}

func (r SchemaCopyRequest) Clone() SchemaCopyRequest { r.Original = r.Original.Clone(); return r }

type SchemaRenameRequest struct {
	Original    SchemaDesignSnapshot
	Destination string
}

func (r SchemaRenameRequest) Clone() SchemaRenameRequest { r.Original = r.Original.Clone(); return r }

type SchemaDesignOutcome struct {
	// State is not_executed, rolled_back, committed or uncertain. Only an
	// acknowledged, verified commit permits the host to replace its draft.
	State    string
	Verified bool
	Rows     int64
	Actual   *SchemaDesignSnapshot
	// Receipt retains the original request and acknowledgement independently of
	// later reads or runtime validation. Its state must never be relabelled as a
	// later operation's result, even when the host rejects a verified outcome.
	Receipt *SchemaDesignWriteReceipt
	Error   error
}

func (o SchemaDesignOutcome) Clone() SchemaDesignOutcome {
	if o.Actual != nil {
		actual := o.Actual.Clone()
		o.Actual = &actual
	}
	if o.Receipt != nil {
		receipt := o.Receipt.Clone()
		o.Receipt = &receipt
	}
	return o
}

type SchemaDesignOperations struct {
	CanApply func(SchemaDesignTarget) bool
	Snapshot func(context.Context, SchemaDesignTarget) (SchemaDesignSnapshot, error)
	Apply    func(context.Context, SchemaDesignRequest) SchemaDesignOutcome
	// Drop consumes a complete original snapshot. A verified committed receipt
	// retains that original target with Exists=false; it never means CASCADE.
	CanDrop   func(SchemaDesignTarget) bool
	Drop      func(context.Context, SchemaDesignSnapshot) SchemaDesignOutcome
	CanCopy   func(SchemaDesignTarget) bool
	Copy      func(context.Context, SchemaCopyRequest) SchemaDesignOutcome
	CanRename func(SchemaDesignTarget) bool
	Rename    func(context.Context, SchemaRenameRequest) SchemaDesignOutcome
}
type SchemaDesignProvider interface{ SchemaDesign() SchemaDesignOperations }
