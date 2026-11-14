package contract

import (
	"context"
	"slices"
)

// Definition is the exact authored native create/update JSON. Clone uses only
// Description; delete carries neither. Identity is an original ID, or the
// original AuthMethod name. These values may contain secrets and stay private.
type KVHTTPACLMutationRequest struct {
	Resource, Operation, Identity string
	Definition                    []byte
	Description                   *string
}

func (r KVHTTPACLMutationRequest) Clone() KVHTTPACLMutationRequest {
	r.Definition = slices.Clone(r.Definition)
	r.Description = clonePointer(r.Description)
	return r
}

// This receipt never enters generic LastResult/Unknown. The last attempted ACL
// mutation remains independent of subsequent ACL/generic reads. An unknown
// mutation prevents every later write in its original document until disposal.
type KVHTTPACLMutationResult struct {
	LocalRecordID   string
	Scope           KVScope
	Request         KVHTTPACLMutationRequest
	Receipt         KVResult
	Projection      *KVHTTPACLProjection
	ProjectionError string
	Impact          *KVHTTPACLMutationImpactEvidence
}

func (r KVHTTPACLMutationResult) Clone() KVHTTPACLMutationResult {
	r.Scope = r.Scope.Clone()
	r.Request = r.Request.Clone()
	r.Receipt = r.Receipt.Clone()
	if r.Impact != nil {
		v := r.Impact.Clone()
		r.Impact = &v
	}
	if r.Projection != nil {
		v := r.Projection.Clone()
		r.Projection = &v
	}
	return r
}

// Pure preparation snapshots one original lease request; inspection cannot
// authorize another lease. The generic KVPreparation executor rejects it.
type KVHTTPACLMutationPreparation interface {
	LocalRecordID() string
	Request() KVHTTPACLMutationRequest
}

type KVHTTPACLMutating interface {
	PrepareHTTPACLMutation(KVHTTPACLMutationRequest) (KVHTTPACLMutationPreparation, error)
	ExecuteHTTPACLMutation(context.Context, KVHTTPACLMutationPreparation) (KVHTTPACLMutationResult, error)
	LastHTTPACLMutation() KVHTTPACLMutationResult
}
