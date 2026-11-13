package consul

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func httpWatchNativeRequest(r contract.KVHTTPWatchRequest) (Request, error) {
	h := r.Health
	if h.Kind == "" {
		if h != (contract.KVHTTPHealthWatchTarget{}) || !validKey(r.Target, r.Prefix) || !validKey(r.ObservationPrefix, true) || !strings.HasPrefix(r.Target, r.ObservationPrefix) {
			return Request{}, ErrInvalid
		}
		path, err := KeyPath(r.ObservationPrefix)
		if err != nil {
			return Request{}, err
		}
		return Request{Method: "GET", Path: path, Query: url.Values{"recurse": {""}, "index": {strconv.FormatUint(r.StartIndex, 10)}, "wait": {r.Wait.String()}}}, nil
	}
	if r.Target != "" || r.ObservationPrefix != "" || r.Prefix {
		return Request{}, ErrInvalid
	}
	input := contract.KVRequestToolInput{HTTPHealth: contract.KVHTTPHealthToolInput{
		Passing: h.Passing, IncludeFilter: h.IncludeFilter, Filter: h.Filter,
		IncludeNear: h.IncludeNear, Near: h.Near, Index: strconv.FormatUint(r.StartIndex, 10), Wait: r.Wait.String(),
	}}
	var kind contract.KVRequestToolKind
	switch h.Kind {
	case "state":
		kind, input.HTTPHealth.State = contract.KVToolHTTPHealthState, h.Name
	case "node":
		kind, input.HTTPHealth.Node = contract.KVToolHTTPHealthNode, h.Name
	case "checks":
		kind, input.HTTPHealth.Service = contract.KVToolHTTPHealthChecks, h.Name
	case "service":
		kind, input.HTTPHealth.Service = contract.KVToolHTTPHealthService, h.Name
	default:
		return Request{}, ErrInvalid
	}
	request, err := buildHTTPHealthTool(kind, input)
	if err != nil {
		return Request{}, err
	}
	native, _, write, err := decodeDocumentRequest(request)
	if err != nil || write {
		return Request{}, ErrInvalid
	}
	return native, nil
}
