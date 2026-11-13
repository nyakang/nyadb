package consul

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// AgentServiceRegisterRequest retains the complete authored native definition.
// nil omits replacement; false and true remain explicit native query values.
// Preparation and execution belong to the original Client/document lease.
func AgentServiceRegisterRequest(definition []byte, replaceExistingChecks *bool) (Request, error) {
	r := Request{Method: http.MethodPut, Path: "/v1/agent/service/register", Body: bytes.Clone(definition)}
	if replaceExistingChecks != nil {
		r.Query = url.Values{"replace-existing-checks": {strconv.FormatBool(*replaceExistingChecks)}}
	}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}

// AgentServiceDeregisterRequest selects the original native service ID, not a
// Session UUID or KV key. A path that Consul may clean is rejected without I/O.
func AgentServiceDeregisterRequest(id string) (Request, error) {
	if !validText(id, 4096) || id == "" {
		return Request{}, ErrInvalid
	}
	if !canonicalHTTPKey(id) {
		return Request{}, ErrUnsupported
	}
	r := Request{Method: http.MethodPut, Path: "/v1/agent/service/deregister/" + url.PathEscape(id)}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}

func validateAgentServiceDefinition(raw []byte) error {
	var fields map[string]json.RawMessage
	if jsondocument.Decode(raw, &fields, requestLimit) != nil || fields == nil {
		return ErrInvalid
	}
	namePresent := false
	for key, value := range fields {
		if strings.EqualFold(key, "Name") {
			var name string
			if namePresent || json.Unmarshal(value, &name) != nil || name == "" || !validText(name, 4096) {
				return ErrInvalid
			}
			namePresent = true
		}
	}
	if !namePresent {
		return ErrInvalid
	}
	// Detect unsupported routing and embedded authentication without rebuilding
	// the definition. Meta is native user metadata, not a routing/auth container.
	var definition any
	if jsondocument.Decode(raw, &definition, requestLimit) != nil {
		return ErrInvalid
	}
	return validateAgentServiceRouting(definition)
}

func validateAgentServiceRouting(value any) error {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			switch strings.ToLower(key) {
			case "namespace", "partition", "token":
				return ErrUnsupported
			case "meta":
				continue
			}
			if err := validateAgentServiceRouting(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := validateAgentServiceRouting(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func agentServiceWriteKind(kind string) bool {
	return kind == "agent-service-register" || kind == "agent-service-deregister"
}

// Agent writes return a complete empty 200 response, not the KV boolean ACK.
// ACL checks precede native changes; an unknown original ID is checked before
// deregistration. Other failures can follow partial service/check/sidecar work.
func projectAgentServiceResponse(r *Response, kind string) error {
	if r.Status == http.StatusForbidden || kind == "agent-service-deregister" && r.Status == http.StatusNotFound {
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
