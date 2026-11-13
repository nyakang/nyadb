package consul

import "github.com/nyakang/nyadb/internal/db/contract"

func buildHTTPAgentTool(kind contract.KVRequestToolKind, input contract.KVRequestToolInput) (contract.KVRequest, error) {
	a := input.HTTPAgent
	input.HTTPAgent = contract.KVHTTPAgentToolInput{}
	if input != (contract.KVRequestToolInput{}) || !a.IncludeFilter && a.Filter != "" {
		return contract.KVRequest{}, ErrInvalid
	}
	var filter *string
	if a.IncludeFilter {
		filter = &a.Filter
	}
	var r Request
	var err error
	switch kind {
	case contract.KVToolHTTPAgentSelf:
		if a != (contract.KVHTTPAgentToolInput{}) {
			return contract.KVRequest{}, ErrInvalid
		}
		r = AgentSelfRequest()
	case contract.KVToolHTTPAgentServices:
		r, err = AgentServicesRequest(filter)
	case contract.KVToolHTTPAgentChecks:
		r, err = AgentChecksRequest(filter)
	default:
		return contract.KVRequest{}, ErrUnsupported
	}
	if err != nil {
		return contract.KVRequest{}, err
	}
	return DocumentRequest(r)
}
