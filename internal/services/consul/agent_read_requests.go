package consul

import (
	"net/http"
	"net/url"
)

// AgentSelfRequest reads the originally contacted agent. It does not select a
// datacenter or grant cluster-wide catalog authority.
func AgentSelfRequest() Request {
	return Request{Method: http.MethodGet, Path: "/v1/agent/self"}
}

// AgentServicesRequest builds a local-agent read without opening a connection.
// A nil filter omits the parameter; an empty filter preserves explicit presence.
func AgentServicesRequest(filter *string) (Request, error) {
	return agentListRequest("/v1/agent/services", filter)
}

// AgentChecksRequest reads local checks, which can differ from catalog health.
func AgentChecksRequest(filter *string) (Request, error) {
	return agentListRequest("/v1/agent/checks", filter)
}

func agentListRequest(path string, filter *string) (Request, error) {
	r := Request{Method: http.MethodGet, Path: path}
	if filter != nil {
		r.Query = url.Values{"filter": {*filter}}
	}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}
