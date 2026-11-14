package consul

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// AgentNodeMaintenanceRequest targets the connected local Agent. An omitted or
// empty reason selects the native default; repeated enable retains its Notes.
func AgentNodeMaintenanceRequest(enable bool, reason *string) (Request, error) {
	return agentMaintenanceRequest("/v1/agent/maintenance", enable, reason)
}

// AgentServiceMaintenanceRequest retains the original registered service ID.
// Disable removes its maintenance check, leaving the service and ordinary checks.
func AgentServiceMaintenanceRequest(id string, enable bool, reason *string) (Request, error) {
	if !validText(id, 4096) || id == "" {
		return Request{}, ErrInvalid
	}
	if !canonicalHTTPKey(id) {
		return Request{}, ErrUnsupported
	}
	return agentMaintenanceRequest("/v1/agent/service/maintenance/"+url.PathEscape(id), enable, reason)
}

func agentMaintenanceRequest(path string, enable bool, reason *string) (Request, error) {
	r := Request{Method: http.MethodPut, Path: path, Query: url.Values{"enable": {strconv.FormatBool(enable)}}}
	if reason != nil {
		if len(*reason) > requestLimit {
			return Request{}, ErrBounds
		}
		r.Query["reason"] = []string{*reason}
	}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}

func agentMaintenanceKind(path string) string {
	if path == "/v1/agent/maintenance" {
		return "agent-node-maintenance"
	}
	if strings.HasPrefix(path, "/v1/agent/service/maintenance/") {
		return "agent-service-maintenance"
	}
	return ""
}

func agentMaintenanceWriteKind(kind string) bool {
	return kind == "agent-node-maintenance" || kind == "agent-service-maintenance"
}

func projectAgentMaintenanceResponse(r *Response, kind string) error {
	// Native ACL preflight, and service absence, precede the mutation. Other
	// faults may follow local changes and do not establish a known write outcome.
	if r.Status == http.StatusForbidden || r.Status == http.StatusNotFound && kind == "agent-service-maintenance" {
		r.Acknowledged, r.Succeeded = true, false
		return &HTTPError{r.Status}
	}
	if r.Status != http.StatusOK {
		return &HTTPError{r.Status}
	}
	if len(r.Body) != 0 {
		return ErrProtocol
	}
	// Completion of this endpoint is separate from local/catalog health state.
	// In particular, ACLs can prevent catalog deletion after a local disable.
	r.Acknowledged, r.Succeeded = true, true
	return nil
}
