package contract

import (
	"context"
	"slices"
)

// Management operations share the original HTTP lease, but their definitions
// and receipts can contain credentials and must never enter generic KV files.
type KVHTTPManagementOperation struct {
	Name, Title, Method, Path, BodyKind, Guard string
	Options                                    []string
	Write                                      bool
}

type KVHTTPManagementRequest struct {
	Operation, Kind, Identity string
	Options                   map[string][]string
	Body                      []byte
}

func (r KVHTTPManagementRequest) Clone() KVHTTPManagementRequest {
	r.Options = cloneHTTPFields(r.Options)
	r.Body = slices.Clone(r.Body)
	return r
}

type KVHTTPManagementPreparation interface {
	Request() KVHTTPManagementRequest
	Scope() KVScope
	LocalRecordID() string
	HTTP() KVHTTPRequest
	Writes() bool
}

type KVHTTPManagementResult struct {
	EnterpriseImpact, EnterpriseVerification *KVHTTPEnterpriseImpact
	Scope                                    KVScope
	LocalRecordID                            string
	Request                                  KVHTTPManagementRequest
	Receipt                                  KVResult
}

func (r KVHTTPManagementResult) Clone() KVHTTPManagementResult {
	if r.EnterpriseImpact != nil {
		v := r.EnterpriseImpact.Clone()
		r.EnterpriseImpact = &v
	}
	if r.EnterpriseVerification != nil {
		v := r.EnterpriseVerification.Clone()
		r.EnterpriseVerification = &v
	}
	r.Scope = r.Scope.Clone()
	r.Request = r.Request.Clone()
	r.Receipt = r.Receipt.Clone()
	return r
}

type KVHTTPManaging interface {
	HTTPManagementOperations() []KVHTTPManagementOperation
	PrepareHTTPManagement(KVHTTPManagementRequest) (KVHTTPManagementPreparation, error)
	ExecuteHTTPManagement(context.Context, KVHTTPManagementPreparation) (KVHTTPManagementResult, error)
	LastHTTPManagement() KVHTTPManagementResult
}
