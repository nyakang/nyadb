package consul

import "github.com/nyakang/nyadb/internal/db/contract"

// Tools author text only; original documents and leases still own execution.
func buildHTTPAgentCheckTool(kind contract.KVRequestToolKind, input contract.KVRequestToolInput) (contract.KVRequest, error) {
	a := input.HTTPAgentCheck
	input.HTTPAgentCheck = contract.KVHTTPAgentCheckToolInput{}
	if input != (contract.KVRequestToolInput{}) {
		return contract.KVRequest{}, ErrInvalid
	}
	if len(a.ID) > 4096 || len(a.DefinitionJSON) > requestLimit {
		return contract.KVRequest{}, ErrBounds
	}
	var request Request
	var err error
	switch kind {
	case contract.KVToolHTTPAgentCheckRegister:
		if a.ID != "" {
			return contract.KVRequest{}, ErrInvalid
		}
		request, err = AgentCheckRegisterRequest([]byte(a.DefinitionJSON))
	case contract.KVToolHTTPAgentCheckDeregister:
		if a.DefinitionJSON != "" {
			return contract.KVRequest{}, ErrInvalid
		}
		request, err = AgentCheckDeregisterRequest(a.ID)
	default:
		return contract.KVRequest{}, ErrUnsupported
	}
	if err != nil {
		return contract.KVRequest{}, err
	}
	return DocumentRequest(request)
}
