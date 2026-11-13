package consul

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// TTL status requests retain the native ID and omitted/empty note presence.
// The Agent sets Output and resets TTL; it keeps the definition's Notes.
func AgentCheckPassRequest(id string, note *string) (Request, error) {
	return agentTTLStatusRequest("pass", id, note)
}

func AgentCheckWarnRequest(id string, note *string) (Request, error) {
	return agentTTLStatusRequest("warn", id, note)
}

func AgentCheckFailRequest(id string, note *string) (Request, error) {
	return agentTTLStatusRequest("fail", id, note)
}

// AgentCheckUpdateRequest retains the entire original Status/Output JSON.
// Native Output omission, empty and null remain authored, without rebuilding.
func AgentCheckUpdateRequest(id string, definition []byte) (Request, error) {
	path, err := agentTTLPath("update", id)
	if err != nil {
		return Request{}, err
	}
	r := Request{Method: http.MethodPut, Path: path, Body: bytes.Clone(definition)}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}

func agentTTLPath(action, id string) (string, error) {
	if !validText(id, 4096) || id == "" {
		return "", ErrInvalid
	}
	if !canonicalHTTPKey(id) {
		return "", ErrUnsupported
	}
	return "/v1/agent/check/" + action + "/" + url.PathEscape(id), nil
}

func agentTTLStatusRequest(action, id string, note *string) (Request, error) {
	path, err := agentTTLPath(action, id)
	if err != nil {
		return Request{}, err
	}
	r := Request{Method: http.MethodPut, Path: path}
	if note != nil {
		if len(*note) > requestLimit {
			return Request{}, ErrBounds
		}
		r.Query = url.Values{"note": {*note}}
	}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}

func agentTTLStatusKind(path string) string {
	for _, action := range []string{"pass", "warn", "fail"} {
		if strings.HasPrefix(path, "/v1/agent/check/"+action+"/") {
			return "agent-check-" + action
		}
	}
	return ""
}

func validateAgentTTLUpdate(raw []byte) error {
	var fields map[string]json.RawMessage
	if jsondocument.Decode(raw, &fields, requestLimit) != nil || fields == nil {
		return ErrInvalid
	}
	statusPresent, outputPresent := false, false
	for key, value := range fields {
		switch {
		case strings.EqualFold(key, "Status"):
			var status string
			if statusPresent || json.Unmarshal(value, &status) != nil || status != "passing" && status != "warning" && status != "critical" {
				return ErrInvalid
			}
			statusPresent = true
		case strings.EqualFold(key, "Output"):
			var output *string
			if outputPresent || json.Unmarshal(value, &output) != nil {
				return ErrInvalid
			}
			outputPresent = true
		}
	}
	if !statusPresent {
		return ErrInvalid
	}
	var definition any
	if jsondocument.Decode(raw, &definition, requestLimit) != nil {
		return ErrInvalid
	}
	return validateAgentCheckRouting(definition)
}

func agentTTLWriteKind(kind string) bool {
	return kind == "agent-check-pass" || kind == "agent-check-warn" || kind == "agent-check-fail" || kind == "agent-check-update"
}

func projectAgentTTLResponse(r *Response) error {
	// Both official versions vet ACL and the original check before updateTTLCheck.
	// Other errors can follow SetStatus/reset/persistence and remain unknown.
	if r.Status == http.StatusForbidden || r.Status == http.StatusNotFound {
		r.Acknowledged, r.Succeeded = true, false
		return &HTTPError{r.Status}
	}
	if r.Status != http.StatusOK {
		return &HTTPError{r.Status}
	}
	if len(r.Body) != 0 {
		return ErrProtocol
	}
	r.Acknowledged, r.Succeeded = true, true
	return nil
}
