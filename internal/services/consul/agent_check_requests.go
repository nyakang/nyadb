package consul

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
)

// AgentCheckRegisterRequest retains the entire original native definition.
// ID omission, empty and null remain authored; the Agent chooses defaults.
func AgentCheckRegisterRequest(definition []byte) (Request, error) {
	r := Request{Method: http.MethodPut, Path: "/v1/agent/check/register", Body: bytes.Clone(definition)}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}

// AgentCheckDeregisterRequest selects an original check ID without path cleanup.
func AgentCheckDeregisterRequest(id string) (Request, error) {
	if !validText(id, 4096) || id == "" {
		return Request{}, ErrInvalid
	}
	if !canonicalHTTPKey(id) {
		return Request{}, ErrUnsupported
	}
	r := Request{Method: http.MethodPut, Path: "/v1/agent/check/deregister/" + url.PathEscape(id)}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}

func validateAgentCheckRouting(value any) error {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			switch strings.ToLower(key) {
			case "namespace", "partition", "token":
				return ErrUnsupported
			}
			if err := validateAgentCheckRouting(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := validateAgentCheckRouting(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func agentCheckWriteKind(kind string) bool {
	return kind == "agent-check-register" || kind == "agent-check-deregister"
}

// Keep complete empty 200 ACKs separate from native KV boolean receipts.
// Only the proved ACL and missing-original-ID preflight denials are known here.
func projectAgentCheckResponse(r *Response, kind string) error {
	if r.Status == http.StatusForbidden || kind == "agent-check-deregister" && r.Status == http.StatusNotFound {
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
