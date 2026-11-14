package consul

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func httpWatchNativeRequest(r contract.KVHTTPWatchRequest) (Request, error) {
	if r.Catalog.Kind != "" {
		return httpCatalogWatchNativeRequest(r)
	}
	if r.Catalog != (contract.KVHTTPCatalogWatchTarget{}) {
		return Request{}, ErrInvalid
	}
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

func httpCatalogWatchNativeRequest(r contract.KVHTTPWatchRequest) (Request, error) {
	c := r.Catalog
	if r.Health != (contract.KVHTTPHealthWatchTarget{}) || r.Target != "" || r.ObservationPrefix != "" || r.Prefix {
		return Request{}, ErrInvalid
	}
	in := contract.KVRequestToolInput{HTTPCatalog: contract.KVHTTPCatalogToolInput{IncludeFilter: c.IncludeFilter, IncludeNear: c.IncludeNear, Filter: c.Filter, Near: c.Near, Index: strconv.FormatUint(r.StartIndex, 10), Wait: r.Wait.String()}}
	var kind contract.KVRequestToolKind
	switch c.Kind {
	case "nodes":
		kind = contract.KVToolHTTPCatalogNodes
		if c.Name != "" {
			return Request{}, ErrInvalid
		}
	case "services":
		kind = contract.KVToolHTTPCatalogServices
		if c.Name != "" {
			return Request{}, ErrInvalid
		}
	case "node":
		kind = contract.KVToolHTTPCatalogNode
		in.HTTPCatalog.Node = c.Name
	case "service":
		kind = contract.KVToolHTTPCatalogService
		in.HTTPCatalog.Service = c.Name
	default:
		return Request{}, ErrInvalid
	}
	request, err := buildHTTPCatalogTool(kind, in)
	if err != nil {
		return Request{}, err
	}
	native, _, write, err := decodeDocumentRequest(request)
	if err != nil || write {
		return Request{}, ErrInvalid
	}
	return native, nil
}
