package consul

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// These native requests remain outside generic request documents. Their bodies
// and responses can carry token secrets or authentication configuration.
// Token, policy and role mutations use the contacted agent's datacenter. A
// configured DC must match immutable original opening evidence; preparation
// performs no discovery or fallback. Auth methods and binding rules route DC.
func ACLCreateRequest(resource ACLReadResource, definition []byte) (Request, error) {
	return aclMutationRequest(resource, "create", "", definition)
}

func ACLUpdateRequest(resource ACLReadResource, identity string, definition []byte) (Request, error) {
	return aclMutationRequest(resource, "update", identity, definition)
}

func ACLDeleteRequest(resource ACLReadResource, identity string) (Request, error) {
	return aclMutationRequest(resource, "delete", identity, nil)
}

// Consul clones the original token's links, generates both IDs and uses only
// Description from the body. nil omits Description; an empty string retains
// the native inheritance behavior. No other authored fields are silently lost.
func ACLTokenCloneRequest(accessor string, description *string) (Request, error) {
	fields := map[string]string{}
	if description != nil {
		if !utf8.ValidString(*description) || len(*description) > requestLimit {
			return Request{}, ErrInvalid
		}
		fields["Description"] = *description
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return Request{}, ErrInvalid
	}
	return aclMutationRequest(ACLReadToken, "clone", accessor, raw)
}

func aclMutationRequest(resource ACLReadResource, action, identity string, body []byte) (Request, error) {
	r := Request{Method: http.MethodPut, Path: "/v1/acl/" + string(resource), Body: bytes.Clone(body)}
	if action != "create" {
		if identity == "" || !validText(identity, 4096) {
			return Request{}, ErrInvalid
		}
		if !canonicalHTTPKey(identity) {
			return Request{}, ErrUnsupported
		}
		r.Path += "/" + url.PathEscape(identity)
	}
	if action == "delete" {
		r.Method = http.MethodDelete
	} else if action == "clone" {
		r.Path += "/clone"
	}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}

type aclMutationTarget struct {
	resource         ACLReadResource
	action, identity string
}

func aclMutationTargetOf(r Request) (aclMutationTarget, error) {
	for _, resource := range []ACLReadResource{ACLReadToken, ACLReadPolicy, ACLReadRole, ACLReadAuthMethod, ACLReadBindingRule} {
		base := "/v1/acl/" + string(resource)
		if r.Path == base {
			if r.Method != http.MethodPut {
				return aclMutationTarget{}, ErrInvalid
			}
			return aclMutationTarget{resource: resource, action: "create"}, nil
		}
		if !strings.HasPrefix(r.Path, base+"/") {
			continue
		}
		path, action := r.Path, "update"
		if resource == ACLReadToken && strings.HasSuffix(path, "/clone") {
			path, action = strings.TrimSuffix(path, "/clone"), "clone"
		} else if r.Method == http.MethodDelete {
			action = "delete"
		}
		if action != "delete" && r.Method != http.MethodPut {
			return aclMutationTarget{}, ErrInvalid
		}
		identity, err := keyAt(path, base+"/", false)
		if err != nil || !validText(identity, 4096) {
			return aclMutationTarget{}, ErrInvalid
		}
		if resource == ACLReadAuthMethod {
			if !canonicalHTTPKey(identity) {
				return aclMutationTarget{}, ErrUnsupported
			}
		} else if !validID(identity) {
			return aclMutationTarget{}, ErrInvalid
		}
		return aclMutationTarget{resource: resource, action: action, identity: identity}, nil
	}
	return aclMutationTarget{}, ErrUnsupported
}

func validateACLMutation(r Request) (string, error) {
	target, err := aclMutationTargetOf(r)
	if err != nil {
		return "", err
	}
	if target.action == "delete" {
		if len(r.Body) != 0 {
			return "", ErrInvalid
		}
	} else if err := validateACLMutationBody(target, r.Body); err != nil {
		return "", err
	}
	return "acl-" + string(target.resource) + "-" + target.action, nil
}

func validateACLMutationBody(target aclMutationTarget, raw []byte) error {
	var fields map[string]json.RawMessage
	if jsondocument.Decode(raw, &fields, requestLimit) != nil || fields == nil {
		return ErrInvalid
	}
	allowed := "Description"
	switch target.resource {
	case ACLReadToken:
		if target.action != "clone" {
			allowed += " AccessorID SecretID Policies Roles ServiceIdentities NodeIdentities TemplatedPolicies Local ExpirationTTL ExpirationTime"
		}
	case ACLReadPolicy:
		allowed += " ID Name Rules Datacenters"
	case ACLReadRole:
		allowed += " ID Name Policies ServiceIdentities NodeIdentities TemplatedPolicies"
	case ACLReadAuthMethod:
		allowed += " Name Type DisplayName MaxTokenTTL TokenLocality Config"
	case ACLReadBindingRule:
		allowed += " ID AuthMethod Selector BindType BindName BindVars"
	}
	known := map[string]bool{}
	for _, key := range strings.Fields(allowed) {
		known[key] = true
	}
	for key := range fields {
		if !known[key] {
			// This rejects routing/credential overrides, server-only metadata
			// and clone fields that the native handler would otherwise ignore.
			return ErrUnsupported
		}
	}
	identityField := "ID"
	if target.resource == ACLReadToken {
		identityField = "AccessorID"
	} else if target.resource == ACLReadAuthMethod {
		identityField = "Name"
	}
	var identity string
	if raw, present := fields[identityField]; present {
		if json.Unmarshal(raw, &identity) != nil || !validText(identity, 4096) {
			return ErrInvalid
		}
	}
	if target.action == "update" && identity != "" && identity != target.identity {
		return ErrInvalid
	}
	if target.action == "create" {
		if target.resource == ACLReadAuthMethod {
			if identity == "" || !canonicalHTTPKey(identity) {
				return ErrInvalid
			}
		} else if identity != "" && (target.resource != ACLReadToken || !validID(identity)) {
			return ErrInvalid
		}
	}
	if target.resource == ACLReadToken {
		if value, ok := fields["SecretID"]; ok {
			var secret string
			if json.Unmarshal(value, &secret) != nil || secret == "<hidden>" {
				return ErrInvalid
			}
		}
	}
	if target.resource == ACLReadAuthMethod {
		if value, ok := fields["Config"]; ok {
			redacted, err := aclConfigRedacted(value)
			if err != nil || redacted {
				return ErrInvalid
			}
		}
	}
	return nil
}

func aclMutationWriteKind(kind string) bool {
	return strings.HasPrefix(kind, "acl-") && kind != "acl-read"
}

func aclMutationUsesAgent(kind string) bool {
	return strings.HasPrefix(kind, "acl-token-") || strings.HasPrefix(kind, "acl-policy-") || strings.HasPrefix(kind, "acl-role-")
}

func projectACLMutationResponse(r *Response, p Plan) error {
	target, err := aclMutationTargetOf(p.request)
	if err != nil {
		return err
	}
	// These native ACL authorization/not-found checks precede mutation. Other
	// status codes may follow processing; preserve their uncertain outcome.
	if r.Status == http.StatusUnauthorized || r.Status == http.StatusForbidden || target.action == "delete" && r.Status == http.StatusNotFound {
		r.Acknowledged, r.Succeeded = true, false
		return &HTTPError{r.Status}
	}
	if r.Status != http.StatusOK {
		return &HTTPError{r.Status}
	}
	if target.action == "delete" {
		if !bytes.Equal(bytes.TrimSpace(r.Body), []byte("true")) {
			return ErrProtocol
		}
	} else {
		var checked sessionProjectionJSON
		if jsondocument.Decode(r.Body, &checked, responseLimit) != nil {
			return ErrProtocol
		}
		row, err := decodeACLRow(r.Body, string(target.resource))
		if err != nil || target.action == "update" && row.Identity != target.identity || target.action == "clone" && row.Identity == target.identity {
			return ErrProtocol
		}
		if row.CreateIndex == nil || row.ModifyIndex == nil || *row.CreateIndex == 0 || *row.ModifyIndex < *row.CreateIndex {
			return ErrProtocol
		}
		if target.resource == ACLReadToken && row.SecretState != "value" {
			return ErrProtocol
		}
		if target.action == "create" {
			var authored map[string]json.RawMessage
			if json.Unmarshal(p.request.Body, &authored) != nil {
				return ErrInvalid
			}
			key := "AccessorID"
			if target.resource == ACLReadAuthMethod {
				key = "Name"
			}
			var identity string
			if value, ok := authored[key]; ok && json.Unmarshal(value, &identity) != nil {
				return ErrInvalid
			}
			if identity != "" && row.Identity != identity {
				return ErrProtocol
			}
		}
	}
	r.Acknowledged, r.Succeeded = true, true
	return nil
}
