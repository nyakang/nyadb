package contract

import "context"

type KVHTTPACLImpactLimits struct {
	MaxRows, MaxReads int
	MaxBytes          int64
}

// The original preview and the separate write-time reads remain private even
// if no write was attempted. Revalidated is never an atomic native CAS claim.
type KVHTTPACLMutationImpactEvidence struct {
	Preview, Recheck KVHTTPACLImpact
	Revalidated      bool
}

func (e KVHTTPACLMutationImpactEvidence) Clone() KVHTTPACLMutationImpactEvidence {
	e.Preview = e.Preview.Clone()
	e.Recheck = e.Recheck.Clone()
	return e
}

type KVHTTPACLMutationPlan interface {
	KVHTTPACLMutationPreparation
	Impact() KVHTTPACLImpact
	Close() error // cancels and waits for its original executor, then releases evidence
}

type KVHTTPACLMutationPlanning interface {
	// Native create/update/delete or token clone. The observation
	// is returned even on error so partial original reads are never discarded.
	// No authority is returned unless every required read is complete.
	PrepareHTTPACLMutationPlan(context.Context, KVHTTPACLMutationRequest, KVHTTPACLImpactLimits) (KVHTTPACLMutationPlan, KVHTTPACLImpact, error)
}
