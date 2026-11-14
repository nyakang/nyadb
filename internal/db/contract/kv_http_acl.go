package contract

import (
	"context"
	"slices"
)

// Lookup is list, id, name or self. Self addresses the originally configured
// credential, never an accessor ID. Optional native query values retain presence.
type KVHTTPACLReadRequest struct {
	Resource, Lookup, Identity            string
	Policy, Role, AuthMethod, ServiceName *string
	Index                                 *uint64
	Wait                                  *string
	Expanded                              bool
}

func (r KVHTTPACLReadRequest) Clone() KVHTTPACLReadRequest {
	r.Policy = clonePointer(r.Policy)
	r.Role = clonePointer(r.Role)
	r.AuthMethod = clonePointer(r.AuthMethod)
	r.ServiceName = clonePointer(r.ServiceName)
	r.Index = clonePointer(r.Index)
	r.Wait = clonePointer(r.Wait)
	return r
}

// Raw can contain native credentials and AuthMethod configuration. It belongs
// to the original ACL receipt and requires an explicit secret presentation or
// export flow; it must not enter generic operation logs or default previews.
// SecretState/ConfigState distinguish absent, null, empty, redacted and value.
type KVHTTPACLRow struct {
	Key, Identity, Name, Description string
	PresentFields, NullFields        []string
	CreateIndex, ModifyIndex         *uint64
	SecretState, ConfigState         string
	Raw                              []byte
}

func (r KVHTTPACLRow) Clone() KVHTTPACLRow {
	r.PresentFields = slices.Clone(r.PresentFields)
	r.NullFields = slices.Clone(r.NullFields)
	r.CreateIndex = clonePointer(r.CreateIndex)
	r.ModifyIndex = clonePointer(r.ModifyIndex)
	r.Raw = slices.Clone(r.Raw)
	return r
}

// Complete describes only the scoped native collection, independently of
// server redaction and any authority to mutate an ACL resource.
type KVHTTPACLProjection struct {
	Resource, Lookup                           string
	Rows                                       []KVHTTPACLRow
	Null, ProjectionComplete, Complete         bool
	ContainsRedaction, ContainsSensitiveValues bool
}

func (p KVHTTPACLProjection) Clone() KVHTTPACLProjection {
	p.Rows = slices.Clone(p.Rows)
	for i := range p.Rows {
		p.Rows[i] = p.Rows[i].Clone()
	}
	return p
}

// An ACL receipt is deliberately separate from KVRequestLease.LastResult and
// KVRecovery.Last. Generic file/clipboard flows must not receive native secrets.
type KVHTTPACLReadResult struct {
	LocalRecordID   string // local receipt identity, never native identity or execution authority
	Scope           KVScope
	Request         KVHTTPACLReadRequest
	Receipt         KVResult
	Projection      *KVHTTPACLProjection
	ProjectionError string
}

func (r KVHTTPACLReadResult) Clone() KVHTTPACLReadResult {
	r.Scope = r.Scope.Clone()
	r.Request = r.Request.Clone()
	r.Receipt = r.Receipt.Clone()
	if r.Projection != nil {
		p := r.Projection.Clone()
		r.Projection = &p
	}
	return r
}

// This preparation cannot be executed through the generic KVPreparation path.
// Preparation is pure and execution consumes one original physical lease plan.
type KVHTTPACLReadPreparation interface {
	LocalRecordID() string
	Request() KVHTTPACLReadRequest
}

type KVHTTPACLReading interface {
	PrepareHTTPACLRead(KVHTTPACLReadRequest) (KVHTTPACLReadPreparation, error)
	ExecuteHTTPACLRead(context.Context, KVHTTPACLReadPreparation) (KVHTTPACLReadResult, error)
	LastHTTPACLRead() KVHTTPACLReadResult
}
