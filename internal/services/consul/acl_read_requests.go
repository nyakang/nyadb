package consul

import (
	"net/url"
	"strconv"
	"strings"
)

type ACLReadResource string

const (
	ACLReadToken           ACLReadResource = "token"
	ACLReadPolicy          ACLReadResource = "policy"
	ACLReadRole            ACLReadResource = "role"
	ACLReadAuthMethod      ACLReadResource = "auth-method"
	ACLReadBindingRule     ACLReadResource = "binding-rule"
	ACLReadTemplatedPolicy ACLReadResource = "templated-policy"
)

// Native membership parameters are endpoint-specific. Authored empty values
// remain distinct from omission. Expanded selects the native presence option;
// it does not interpret a false-looking query value as an unexpanded read.
type ACLReadOptions struct {
	Index                                 *uint64
	Wait                                  *string
	Policy, Role, AuthMethod, ServiceName *string
	Expanded                              bool
}

type aclReadTarget struct {
	resource             ACLReadResource
	list, self, template bool
}

func aclListPath(resource ACLReadResource) string {
	switch resource {
	case ACLReadPolicy:
		return "/v1/acl/policies"
	case ACLReadTemplatedPolicy:
		return "/v1/acl/templated-policies"
	case ACLReadToken, ACLReadRole, ACLReadAuthMethod, ACLReadBindingRule:
		return "/v1/acl/" + string(resource) + "s"
	}
	return ""
}

func aclTemplateReadPath(path string) bool {
	return path == "/v1/acl/templated-policies" || strings.HasPrefix(path, "/v1/acl/templated-policy/name/")
}

func aclTarget(path string) (aclReadTarget, error) {
	for _, resource := range []ACLReadResource{ACLReadToken, ACLReadPolicy, ACLReadRole, ACLReadAuthMethod, ACLReadBindingRule, ACLReadTemplatedPolicy} {
		if path == aclListPath(resource) {
			return aclReadTarget{resource: resource, list: true, template: resource == ACLReadTemplatedPolicy}, nil
		}
	}
	if path == "/v1/acl/token/self" {
		return aclReadTarget{resource: ACLReadToken, self: true}, nil
	}
	for _, resource := range []ACLReadResource{ACLReadPolicy, ACLReadRole, ACLReadTemplatedPolicy} {
		prefix := "/v1/acl/" + string(resource) + "/name/"
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		name, err := keyAt(path, prefix, false)
		if resource == ACLReadPolicy && err == nil {
			// The native policy-name handler QueryUnescapes the already decoded
			// URL path. Protect literal '+', '%' and '/' through both decoders.
			name, err = url.QueryUnescape(name)
			if err == nil && path != prefix+url.PathEscape(url.QueryEscape(name)) {
				err = ErrInvalid
			}
		}
		if err != nil || name == "" || !validText(name, 4096) {
			return aclReadTarget{}, ErrInvalid
		}
		if !canonicalHTTPKey(name) {
			return aclReadTarget{}, ErrUnsupported
		}
		return aclReadTarget{resource: resource, template: resource == ACLReadTemplatedPolicy}, nil
	}
	for _, resource := range []ACLReadResource{ACLReadToken, ACLReadPolicy, ACLReadRole, ACLReadAuthMethod, ACLReadBindingRule} {
		prefix := "/v1/acl/" + string(resource) + "/"
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		identity, err := keyAt(path, prefix, false)
		if err != nil || !validText(identity, 4096) {
			return aclReadTarget{}, ErrInvalid
		}
		if resource == ACLReadAuthMethod {
			if !canonicalHTTPKey(identity) {
				return aclReadTarget{}, ErrUnsupported
			}
		} else if !validID(identity) {
			return aclReadTarget{}, ErrInvalid
		}
		return aclReadTarget{resource: resource}, nil
	}
	return aclReadTarget{}, ErrUnsupported
}

func aclReadQuery(request Request) (map[string]bool, error) {
	target, err := aclTarget(request.Path)
	if err != nil {
		return nil, err
	}
	if request.Method != "GET" || len(request.Body) != 0 {
		return nil, ErrInvalid
	}
	allowed := map[string]bool{}
	if !target.template {
		for _, key := range []string{"index", "wait", "consistent", "stale"} {
			allowed[key] = true
		}
	}
	if target.list && (target.resource == ACLReadToken || target.resource == ACLReadRole) {
		allowed["policy"] = true
	}
	if target.list && target.resource == ACLReadToken {
		allowed["role"], allowed["servicename"] = true, true
	}
	if target.list && (target.resource == ACLReadToken || target.resource == ACLReadBindingRule) {
		allowed["authmethod"] = true
	}
	if target.resource == ACLReadToken && !target.list && !target.self {
		allowed["expanded"] = true
	}
	return allowed, nil
}

func ACLListReadRequest(resource ACLReadResource) (Request, error) {
	path := aclListPath(resource)
	if path == "" {
		return Request{}, ErrUnsupported
	}
	return Request{Method: "GET", Path: path}, nil
}

func ACLTokenSelfReadRequest() Request {
	return Request{Method: "GET", Path: "/v1/acl/token/self"}
}

func ACLItemReadRequest(resource ACLReadResource, identity string) (Request, error) {
	if resource == ACLReadAuthMethod || resource == ACLReadTemplatedPolicy {
		return ACLNameReadRequest(resource, identity)
	}
	if resource != ACLReadToken && resource != ACLReadPolicy && resource != ACLReadRole && resource != ACLReadBindingRule {
		return Request{}, ErrUnsupported
	}
	if !validID(identity) {
		return Request{}, ErrInvalid
	}
	return Request{Method: "GET", Path: "/v1/acl/" + string(resource) + "/" + identity}, nil
}

func ACLNameReadRequest(resource ACLReadResource, name string) (Request, error) {
	if name == "" || !validText(name, 4096) {
		return Request{}, ErrInvalid
	}
	if !canonicalHTTPKey(name) {
		return Request{}, ErrUnsupported
	}
	prefix := "/v1/acl/" + string(resource) + "/name/"
	encoded := url.PathEscape(name)
	switch resource {
	case ACLReadPolicy:
		encoded = url.PathEscape(url.QueryEscape(name))
	case ACLReadRole, ACLReadTemplatedPolicy:
	case ACLReadAuthMethod:
		prefix = "/v1/acl/auth-method/"
	default:
		return Request{}, ErrUnsupported
	}
	request := Request{Method: "GET", Path: prefix + encoded}
	if _, _, _, err := validateRequest(request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func WithACLReadOptions(request Request, options ACLReadOptions) (Request, error) {
	if _, err := aclReadQuery(request); err != nil {
		return Request{}, err
	}
	r := request.Clone()
	if r.Query == nil {
		r.Query = url.Values{}
	}
	for key, value := range map[string]*string{"wait": options.Wait, "policy": options.Policy, "role": options.Role, "authmethod": options.AuthMethod, "servicename": options.ServiceName} {
		if value != nil {
			r.Query.Set(key, *value)
		}
	}
	if options.Index != nil {
		r.Query.Set("index", strconv.FormatUint(*options.Index, 10))
	}
	if options.Expanded {
		r.Query.Set("expanded", "")
	}
	if _, _, _, err := validateRequest(r); err != nil {
		return Request{}, err
	}
	return r, nil
}
