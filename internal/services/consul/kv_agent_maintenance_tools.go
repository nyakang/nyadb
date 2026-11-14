package consul

import (
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

func buildHTTPAgentMaintenanceTool(kind contract.KVRequestToolKind, input contract.KVRequestToolInput) (contract.KVRequest, error) {
	a := input.HTTPAgentMaintenance
	input.HTTPAgentMaintenance = contract.KVHTTPAgentMaintenanceToolInput{}
	if input != (contract.KVRequestToolInput{}) || !a.IncludeReason && a.ReasonJSON != "" {
		return contract.KVRequest{}, ErrInvalid
	}
	if len(a.ID) > 4096 || len(a.ReasonJSON) > requestLimit {
		return contract.KVRequest{}, ErrBounds
	}
	enable, err := strconv.ParseBool(a.Enable)
	if err != nil {
		return contract.KVRequest{}, ErrInvalid
	}
	var reason *string
	if a.IncludeReason {
		if a.ReasonJSON == "" {
			reason = new(string)
		} else if jsondocument.Decode([]byte(a.ReasonJSON), &reason, requestLimit) != nil || reason == nil {
			return contract.KVRequest{}, ErrInvalid
		}
	}
	var request Request
	switch kind {
	case contract.KVToolHTTPAgentNodeMaintenance:
		if a.ID != "" {
			return contract.KVRequest{}, ErrInvalid
		}
		request, err = AgentNodeMaintenanceRequest(enable, reason)
	case contract.KVToolHTTPAgentServiceMaintenance:
		request, err = AgentServiceMaintenanceRequest(a.ID, enable, reason)
	default:
		return contract.KVRequest{}, ErrUnsupported
	}
	if err != nil {
		return contract.KVRequest{}, err
	}
	request.Query.Set("enable", a.Enable)
	return DocumentRequest(request)
}
