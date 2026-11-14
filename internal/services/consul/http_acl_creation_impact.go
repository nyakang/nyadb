package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func validACLCreationImpact(input contract.KVHTTPACLMutationRequest, request contract.KVHTTPACLImpactRequest) bool {
	if input.Operation != "create" || input.Identity != "" || request.Identity != "" || request.Resource != input.Resource || request.MaxRows <= 0 || request.MaxRows > contract.KVHTTPACLImpactMaxRows || request.MaxReads <= 0 || request.MaxReads > contract.KVHTTPACLImpactMaxReads || request.MaxBytes <= 0 || request.MaxBytes > contract.KVHTTPACLImpactMaxBytes {
		return false
	}
	_, err := aclNativeMutationRequest(input)
	return err == nil
}

func aclCreationString(fields map[string]json.RawMessage, key string) (string, error) {
	value, ok := fields[key]
	if !ok {
		return "", nil
	}
	var out string
	if json.Unmarshal(value, &out) != nil {
		return "", ErrInvalid
	}
	return out, nil
}

type aclCreationLink struct{ ID, Name string }

func aclCreationLinks(fields map[string]json.RawMessage, key string) ([]aclCreationLink, error) {
	raw, ok := fields[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var links []aclCreationLink
	if json.Unmarshal(raw, &links) != nil {
		return nil, ErrInvalid
	}
	for _, link := range links {
		if link.ID == "" && link.Name == "" || link.ID != "" && !validID(link.ID) {
			return nil, ErrInvalid
		}
	}
	return links, nil
}

// The initial collection is an original scoped absence/collision observation.
// Details of requested dependencies and an existing AuthMethod upsert follow
// through the same original executor. None of these reads is a native CAS.
func collectACLCreationImpact(ctx context.Context, scope contract.KVScope, input contract.KVHTTPACLMutationRequest, request contract.KVHTTPACLImpactRequest, execute func(contract.KVHTTPACLReadRequest) (contract.KVHTTPACLReadResult, error)) (out contract.KVHTTPACLImpact, err error) {
	creation := input.Clone()
	out = contract.KVHTTPACLImpact{Scope: scope.Clone(), Request: request, Creation: &creation}
	defer func() { out.Bytes = aclImpactCost(out) }()
	if !validACLCreationImpact(input, request) || scope.HTTP.Protocol != "consul-http" || scope.Namespace.ProfileID <= 0 || scope.Generation == 0 {
		return out, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(input.Definition, &fields) != nil {
		return out, ErrInvalid
	}
	name, err := aclCreationString(fields, "Name")
	if err != nil {
		return out, err
	}
	if (input.Resource == "policy" || input.Resource == "role" || input.Resource == "auth-method") && name == "" {
		return out, ErrInvalid
	}
	accessor, err := aclCreationString(fields, "AccessorID")
	if err != nil {
		return out, err
	}
	secret, err := aclCreationString(fields, "SecretID")
	if err != nil {
		return out, err
	}
	read := func(r contract.KVHTTPACLReadRequest) (contract.KVHTTPACLReadResult, error) {
		if err := ctx.Err(); err != nil {
			return contract.KVHTTPACLReadResult{Scope: scope, Request: r}, err
		}
		if len(out.Reads) >= request.MaxReads || aclImpactCost(out) >= request.MaxBytes {
			return contract.KVHTTPACLReadResult{Scope: scope, Request: r}, ErrBounds
		}
		result, err := execute(r)
		if !reflect.DeepEqual(result.Request, r) || !reflect.DeepEqual(result.Scope, scope) {
			return result, ErrProtocol
		}
		item := contract.KVHTTPACLImpactRead{Result: result}
		if result.Receipt.HTTP != nil {
			item.BodySHA256 = aclImpactBodyHash(result.Receipt.HTTP.Body)
		}
		out.Reads = append(out.Reads, item)
		if err != nil {
			return result, err
		}
		if !completeACLImpactRead(result) {
			return result, ErrProtocol
		}
		out.Rows += len(result.Projection.Rows)
		if out.Rows > request.MaxRows || aclImpactCost(out) > request.MaxBytes {
			return result, ErrBounds
		}
		return result, nil
	}
	root, err := read(contract.KVHTTPACLReadRequest{Resource: input.Resource, Lookup: "list"})
	if err != nil {
		return out, err
	}
	for _, row := range root.Projection.Rows {
		switch input.Resource {
		case "policy", "role", "auth-method":
			if row.Name == "" {
				return out, ErrProtocol
			}
			if strings.EqualFold(row.Name, name) {
				if input.Resource != "auth-method" {
					return out, ErrConflict
				}
				if out.UpsertIdentity != "" {
					return out, ErrProtocol
				}
				out.UpsertIdentity = row.Identity
			}
		case "token":
			if accessor == "" && secret == "" {
				continue
			}
			// Chosen IDs share the native accessor/secret collision domain.
			// A redacted or missing secret cannot establish non-collision.
			if row.SecretState != "value" {
				return out, ErrProtocol
			}
			var raw map[string]json.RawMessage
			if json.Unmarshal(row.Raw, &raw) != nil {
				return out, ErrProtocol
			}
			original, err := aclCreationString(raw, "SecretID")
			if err != nil || original == "" {
				return out, ErrProtocol
			}
			for _, chosen := range []string{accessor, secret} {
				if chosen != "" && (strings.EqualFold(chosen, row.Identity) || strings.EqualFold(chosen, original)) {
					return out, ErrConflict
				}
			}
		}
	}
	if out.UpsertIdentity != "" {
		start := len(out.Reads)
		target := request
		target.Identity = out.UpsertIdentity
		observed, e := collectACLImpact(ctx, scope, target, read)
		for _, ref := range observed.References {
			for i := range ref.Locations {
				ref.Locations[i].Read += start
			}
			out.References = append(out.References, ref)
		}
		if e != nil {
			return out, e
		}
		if !observed.Complete {
			return out, ErrProtocol
		}
	}
	// Native ID takes precedence over Name. Name resolution retains its whole
	// original collection; full details retain policy rules and role links.
	lists := map[string]int{input.Resource: 0}
	fetched := map[string]int{}
	var dependency func(string, aclCreationLink) error
	dependency = func(resource string, link aclCreationLink) error {
		id := link.ID
		if id == "" {
			index, ok := lists[resource]
			if !ok {
				index = len(out.Reads)
				if _, err := read(contract.KVHTTPACLReadRequest{Resource: resource, Lookup: "list"}); err != nil {
					return err
				}
				lists[resource] = index
			}
			for _, row := range out.Reads[index].Result.Projection.Rows {
				if row.Name == "" {
					return ErrProtocol
				}
				if strings.EqualFold(row.Name, link.Name) {
					if id != "" {
						return ErrProtocol
					}
					id = row.Identity
				}
			}
			if id == "" {
				return ErrConflict
			}
		}
		key := resource + "/" + id
		if _, ok := fetched[key]; ok {
			return nil
		}
		lookup := "id"
		if resource == "auth-method" {
			lookup = "name"
		}
		index := len(out.Reads)
		detail, err := read(contract.KVHTTPACLReadRequest{Resource: resource, Lookup: lookup, Identity: id})
		if err != nil {
			return err
		}
		if len(detail.Projection.Rows) != 1 || detail.Projection.Rows[0].Identity != id {
			return ErrProtocol
		}
		fetched[key] = index
		out.Dependencies = append(out.Dependencies, contract.KVHTTPACLImpactReference{Resource: resource, Identity: id, Locations: []contract.KVHTTPACLImpactLocation{{Read: index, Row: 0}}})
		if aclImpactCost(out) > request.MaxBytes {
			return ErrBounds
		}
		if resource == "role" {
			var role map[string]json.RawMessage
			if json.Unmarshal(detail.Projection.Rows[0].Raw, &role) != nil {
				return ErrProtocol
			}
			links, err := aclCreationLinks(role, "Policies")
			if err != nil {
				return ErrProtocol
			}
			for _, policy := range links {
				if err := dependency("policy", policy); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if input.Resource == "token" || input.Resource == "role" {
		links, err := aclCreationLinks(fields, "Policies")
		if err != nil {
			return out, err
		}
		for _, link := range links {
			if err := dependency("policy", link); err != nil {
				return out, err
			}
		}
	}
	if input.Resource == "token" {
		links, err := aclCreationLinks(fields, "Roles")
		if err != nil {
			return out, err
		}
		for _, link := range links {
			if err := dependency("role", link); err != nil {
				return out, err
			}
		}
	}
	if input.Resource == "binding-rule" {
		method, err := aclCreationString(fields, "AuthMethod")
		if err != nil || method == "" {
			return out, ErrInvalid
		}
		// AuthMethod identity is its original native name, not a generated ID.
		if err := dependency("auth-method", aclCreationLink{ID: method}); err != nil {
			return out, err
		}
	}
	slices.SortFunc(out.Dependencies, func(a, b contract.KVHTTPACLImpactReference) int {
		return strings.Compare(a.Resource+"/"+a.Identity, b.Resource+"/"+b.Identity)
	})
	if aclImpactCost(out) > request.MaxBytes {
		return out, ErrBounds
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	out.Complete = true
	return out, nil
}

func (l *documentLease) observeACLCreationImpact(ctx context.Context, input contract.KVHTTPACLMutationRequest, request contract.KVHTTPACLImpactRequest) (contract.KVHTTPACLImpact, error) {
	out, err := collectACLCreationImpact(ctx, l.Scope(), input, request, func(r contract.KVHTTPACLReadRequest) (contract.KVHTTPACLReadResult, error) {
		return l.executeACLImpactRead(ctx, r)
	})
	if err == nil {
		err = l.CheckKVLease()
	}
	if err != nil {
		out.Complete = false
	}
	return out, err
}

func compareACLCreationImpact(expected, actual contract.KVHTTPACLImpact) error {
	if !expected.Complete || !actual.Complete || expected.Creation == nil || actual.Creation == nil || !reflect.DeepEqual(expected.Creation, actual.Creation) || expected.Request != actual.Request || !reflect.DeepEqual(expected.Scope, actual.Scope) || len(expected.Reads) != len(actual.Reads) {
		return ErrConflict
	}
	for _, o := range []contract.KVHTTPACLImpact{expected, actual} {
		index := 0
		rebuilt, err := collectACLCreationImpact(context.Background(), o.Scope, *o.Creation, o.Request, func(r contract.KVHTTPACLReadRequest) (contract.KVHTTPACLReadResult, error) {
			if index >= len(o.Reads) {
				return contract.KVHTTPACLReadResult{}, ErrProtocol
			}
			result := o.Reads[index].Result
			index++
			return result, nil
		})
		if err != nil || index != len(o.Reads) || !reflect.DeepEqual(rebuilt, o) {
			return ErrProtocol
		}
	}
	if expected.UpsertIdentity != actual.UpsertIdentity || expected.Rows != actual.Rows || !reflect.DeepEqual(expected.References, actual.References) || !reflect.DeepEqual(expected.Dependencies, actual.Dependencies) {
		return ErrConflict
	}
	for i, r := range expected.Reads {
		if !reflect.DeepEqual(r.Result.Request, actual.Reads[i].Result.Request) || r.BodySHA256 != actual.Reads[i].BodySHA256 {
			return ErrConflict
		}
	}
	return nil
}
