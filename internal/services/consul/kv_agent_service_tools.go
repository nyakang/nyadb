package consul

import (
	"net/url"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// These tools only author text; the existing original document owns immutable
// preparation, confirmation, execution and complete native receipts.
func buildHTTPAgentServiceTool(kind contract.KVRequestToolKind, input contract.KVRequestToolInput) (contract.KVRequest, error) {
	a := input.HTTPAgentService
	input.HTTPAgentService = contract.KVHTTPAgentServiceToolInput{}
	if input != (contract.KVRequestToolInput{}) {
		return contract.KVRequest{}, ErrInvalid
	}
	if len(a.ID) > 4096 || len(a.DefinitionJSON) > requestLimit {
		return contract.KVRequest{}, ErrBounds
	}
	var request Request
	var err error
	switch kind {
	case contract.KVToolHTTPAgentServiceRegister:
		if a.ID != "" || !a.IncludeReplacement && a.ReplaceExistingChecks != "" {
			return contract.KVRequest{}, ErrInvalid
		}
		request, err = AgentServiceRegisterRequest([]byte(a.DefinitionJSON), nil)
		if a.IncludeReplacement {
			request.Query = url.Values{"replace-existing-checks": {a.ReplaceExistingChecks}}
		}
	case contract.KVToolHTTPAgentServiceDeregister:
		if a.DefinitionJSON != "" || a.IncludeReplacement || a.ReplaceExistingChecks != "" {
			return contract.KVRequest{}, ErrInvalid
		}
		request, err = AgentServiceDeregisterRequest(a.ID)
	default:
		return contract.KVRequest{}, ErrUnsupported
	}
	if err != nil {
		return contract.KVRequest{}, err
	}
	return DocumentRequest(request)
}
