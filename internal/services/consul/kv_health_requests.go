package consul

import (
	"net/url"
	"slices"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type HealthReadOptions struct {
	Filter, Near *string
	Index        *uint64
	Wait         *string
}

// The returned request owns its query map and authored option values. Scope,
// authentication and datacenter continue to belong to the original client.
func WithHealthReadOptions(request Request, options HealthReadOptions) (Request, error) {
	if _, ok := healthProjectionTarget(contract.KVHTTPRequest{Method: request.Method, Path: request.Path, Body: request.Body}); !ok {
		return Request{}, ErrInvalid
	}
	r := request.Clone()
	if r.Query == nil {
		r.Query = url.Values{}
	}
	for key, p := range map[string]*string{"filter": options.Filter, "near": options.Near, "wait": options.Wait} {
		if p != nil {
			r.Query.Set(key, *p)
		}
	}
	if options.Index != nil {
		r.Query.Set("index", strconv.FormatUint(*options.Index, 10))
	}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}

func HealthStateRequest(state string) (Request, error) {
	if !slices.Contains([]string{"any", "passing", "warning", "critical"}, state) {
		return Request{}, ErrInvalid
	}
	return Request{Method: "GET", Path: "/v1/health/state/" + state}, nil
}
func healthNamedRequest(kind, name string) (Request, error) {
	if !validKey(name, false) {
		return Request{}, ErrInvalid
	}
	r := Request{Method: "GET", Path: "/v1/health/" + kind + "/" + url.PathEscape(name)}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}
func HealthNodeRequest(node string) (Request, error) { return healthNamedRequest("node", node) }
func HealthChecksRequest(service string) (Request, error) {
	return healthNamedRequest("checks", service)
}

// Passing nil omits the filter; false and true remain explicit native values.
func HealthServiceRequest(service string, passing *bool) (Request, error) {
	r, err := healthNamedRequest("service", service)
	if err != nil {
		return r, err
	}
	if passing != nil {
		value := "false"
		if *passing {
			value = "true"
		}
		r.Query = url.Values{"passing": {value}}
	}
	return r, nil
}
