package consul

import "github.com/nyakang/nyadb/internal/db/contract"

func buildHTTPHealthTool(kind contract.KVRequestToolKind, input contract.KVRequestToolInput) (contract.KVRequest, error) {
	h := input.HTTPHealth
	input.HTTPHealth = contract.KVHTTPHealthToolInput{}
	if input != (contract.KVRequestToolInput{}) || !h.IncludeFilter && h.Filter != "" || !h.IncludeNear && h.Near != "" {
		return contract.KVRequest{}, ErrInvalid
	}
	for _, text := range []string{h.State, h.Node, h.Service, h.Passing, h.Filter, h.Near, h.Index, h.Wait} {
		if len(text) > 4096 {
			return contract.KVRequest{}, ErrBounds
		}
		if !validText(text, 4096) {
			return contract.KVRequest{}, ErrInvalid
		}
	}
	index, err := optionalToolUint(h.Index)
	if err != nil {
		return contract.KVRequest{}, err
	}
	var r Request
	switch kind {
	case contract.KVToolHTTPHealthState:
		if h.Node != "" || h.Service != "" || h.Passing != "" {
			return contract.KVRequest{}, ErrInvalid
		}
		r, err = HealthStateRequest(h.State)
	case contract.KVToolHTTPHealthNode:
		if h.State != "" || h.Service != "" || h.Passing != "" {
			return contract.KVRequest{}, ErrInvalid
		}
		if h.Node != "" && !canonicalHTTPKey(h.Node) {
			return contract.KVRequest{}, ErrUnsupported
		}
		r, err = HealthNodeRequest(h.Node)
	case contract.KVToolHTTPHealthChecks:
		if h.State != "" || h.Node != "" || h.Passing != "" {
			return contract.KVRequest{}, ErrInvalid
		}
		if h.Service != "" && !canonicalHTTPKey(h.Service) {
			return contract.KVRequest{}, ErrUnsupported
		}
		r, err = HealthChecksRequest(h.Service)
	case contract.KVToolHTTPHealthService:
		if h.State != "" || h.Node != "" {
			return contract.KVRequest{}, ErrInvalid
		}
		if h.Service != "" && !canonicalHTTPKey(h.Service) {
			return contract.KVRequest{}, ErrUnsupported
		}
		var passing *bool
		switch h.Passing {
		case "":
		case "false":
			passing = new(false)
		case "true":
			passing = new(true)
		default:
			return contract.KVRequest{}, ErrInvalid
		}
		r, err = HealthServiceRequest(h.Service, passing)
	default:
		return contract.KVRequest{}, ErrUnsupported
	}
	if err != nil {
		return contract.KVRequest{}, err
	}
	options := HealthReadOptions{Index: index}
	if h.IncludeFilter {
		options.Filter = &h.Filter
	}
	if h.IncludeNear {
		options.Near = &h.Near
	}
	if h.Wait != "" {
		options.Wait = &h.Wait
	}
	r, err = WithHealthReadOptions(r, options)
	if err != nil {
		return contract.KVRequest{}, err
	}
	return DocumentRequest(r)
}
