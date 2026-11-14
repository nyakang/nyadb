package consul

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func validACLImpactRequest(r contract.KVHTTPACLImpactRequest) bool {
	if r.MaxRows <= 0 || r.MaxRows > contract.KVHTTPACLImpactMaxRows || r.MaxReads <= 0 || r.MaxReads > contract.KVHTTPACLImpactMaxReads || r.MaxBytes <= 0 || r.MaxBytes > contract.KVHTTPACLImpactMaxBytes {
		return false
	}
	switch ACLReadResource(r.Resource) {
	case ACLReadToken, ACLReadPolicy, ACLReadRole, ACLReadAuthMethod, ACLReadBindingRule:
		_, err := ACLItemReadRequest(ACLReadResource(r.Resource), r.Identity)
		return err == nil
	}
	return false
}

func aclImpactTarget(r contract.KVHTTPACLImpactRequest) contract.KVHTTPACLReadRequest {
	lookup := "id"
	if r.Resource == string(ACLReadAuthMethod) {
		lookup = "name"
	}
	return contract.KVHTTPACLReadRequest{Resource: r.Resource, Lookup: lookup, Identity: r.Identity}
}

func aclImpactBodyHash(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// Validate actual raw links, not the membership index that selected this row.
// Missing/null arrays are native empty links. Malformed links are not absence.
func aclImpactLinked(row contract.KVHTTPACLRow, field, identity string) (bool, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(row.Raw, &fields) != nil {
		return false, ErrProtocol
	}
	raw, present := fields[field]
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, nil
	}
	if field == "AuthMethod" {
		var name string
		if json.Unmarshal(raw, &name) != nil {
			return false, ErrProtocol
		}
		return name == identity, nil
	}
	var links []map[string]json.RawMessage
	if json.Unmarshal(raw, &links) != nil {
		return false, ErrProtocol
	}
	found := false
	seen := make(map[string]bool, len(links))
	for _, link := range links {
		var id string
		if json.Unmarshal(link["ID"], &id) != nil || !validID(id) || seen[id] {
			return false, ErrProtocol
		}
		seen[id] = true
		found = found || id == identity
	}
	return found, nil
}

func completeACLImpactRead(r contract.KVHTTPACLReadResult) bool {
	h := r.Receipt.HTTP
	if h == nil || !r.Receipt.Attempted || !r.Receipt.Completed || r.Receipt.WriteOutcomeUnknown || r.Receipt.ProjectionError != "" || !h.Attempted || !h.Ended || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.Status != 200 || h.CaptureError != "" || h.ProjectionError != "" || h.WriteOutcomeUnknown || h.Metadata.ACLFiltered != nil && *h.Metadata.ACLFiltered || r.ProjectionError != "" {
		return false
	}
	// Re-derive the projection from the original receipt before using any row.
	p := projectACLReadResult(r.Scope, r.Request, r.Receipt)
	return p.ProjectionError == "" && p.Projection != nil && p.Projection.Complete && p.Projection.ProjectionComplete && !p.Projection.ContainsRedaction && !p.Projection.Null && reflect.DeepEqual(p.Projection, r.Projection)
}

func aclImpactCost(o contract.KVHTTPACLImpact) int64 {
	n := aclImpactScopeCost(o.Scope) + int64(128+len(o.Request.Resource)+len(o.Request.Identity)+len(o.UpsertIdentity))
	if r := o.Creation; r != nil {
		n += int64(128 + len(r.Resource) + len(r.Operation) + len(r.Identity) + len(r.Definition))
		if r.Description != nil {
			n += int64(24 + len(*r.Description))
		}
	}
	for _, read := range o.Reads {
		n += aclImpactReadCost(read.Result)
	}
	for _, ref := range o.References {
		n += int64(96 + len(ref.Resource) + len(ref.Identity) + len(ref.Locations)*16)
	}
	for _, ref := range o.Dependencies {
		n += int64(96 + len(ref.Resource) + len(ref.Identity) + len(ref.Locations)*16)
	}
	return n
}

func aclImpactScopeCost(s contract.KVScope) int64 {
	h := s.HTTP
	return int64(512 + len(s.Prefix) + len(s.Version) + len(s.Namespace.Catalog) + len(s.Namespace.Schema) + len(h.Protocol) + len(h.Endpoint) + len(h.Datacenter) + len(h.Consistency) + len(h.Auth) + len(h.Configuration) + len(h.AgentID) + len(h.AgentName) + len(h.AgentDatacenter))
}

func aclImpactReadCost(r contract.KVHTTPACLReadResult) int64 {
	return aclReadResultCost(&r) - (1 << 20) + 128 + aclImpactScopeCost(r.Scope)
}

// All steps run under the original document executor. A cancelled or partial
// read returns its evidence and stops traversal; there is no retry or fallback.
func collectACLImpact(ctx context.Context, scope contract.KVScope, input contract.KVHTTPACLImpactRequest, execute func(contract.KVHTTPACLReadRequest) (contract.KVHTTPACLReadResult, error)) (out contract.KVHTTPACLImpact, err error) {
	out.Scope, out.Request = scope.Clone(), input
	defer func() { out.Bytes = aclImpactCost(out) }()
	if !validACLImpactRequest(input) || scope.HTTP.Protocol != "consul-http" || scope.Namespace.ProfileID <= 0 || scope.Generation == 0 {
		return out, ErrInvalid
	}
	currentBytes := aclImpactCost(out)
	seen := map[string]int{}
	read := func(request contract.KVHTTPACLReadRequest, field, identity string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(out.Reads) >= input.MaxReads || currentBytes >= input.MaxBytes {
			return ErrBounds
		}
		r, err := execute(request)
		if !reflect.DeepEqual(r.Request, request) || !reflect.DeepEqual(r.Scope, scope) {
			return ErrProtocol
		}
		item := contract.KVHTTPACLImpactRead{Result: r}
		if r.Receipt.HTTP != nil {
			item.BodySHA256 = aclImpactBodyHash(r.Receipt.HTTP.Body)
		}
		index := len(out.Reads)
		out.Reads = append(out.Reads, item)
		currentBytes += aclImpactReadCost(r)
		if err != nil {
			return err
		}
		if !completeACLImpactRead(r) {
			return ErrProtocol
		}
		out.Rows += len(r.Projection.Rows)
		if out.Rows > input.MaxRows || currentBytes > input.MaxBytes {
			return ErrBounds
		}
		if field == "" {
			if len(r.Projection.Rows) != 1 || r.Projection.Rows[0].Identity != input.Identity {
				return ErrProtocol
			}
			return nil
		}
		for i, row := range r.Projection.Rows {
			linked, err := aclImpactLinked(row, field, identity)
			if err != nil {
				return err
			}
			if !linked {
				continue
			}
			key := request.Resource + "/" + row.Identity
			ref, exists := seen[key]
			if !exists {
				ref = len(out.References)
				seen[key] = ref
				out.References = append(out.References, contract.KVHTTPACLImpactReference{Resource: request.Resource, Identity: row.Identity})
				currentBytes += int64(96 + len(request.Resource) + len(row.Identity))
			}
			out.References[ref].Locations = append(out.References[ref].Locations, contract.KVHTTPACLImpactLocation{Read: index, Row: i})
			currentBytes += 16
			if currentBytes > input.MaxBytes {
				return ErrBounds
			}
		}
		return nil
	}
	if err = read(aclImpactTarget(input), "", ""); err != nil {
		return out, err
	}
	list := func(resource string) contract.KVHTTPACLReadRequest {
		return contract.KVHTTPACLReadRequest{Resource: resource, Lookup: "list"}
	}
	switch ACLReadResource(input.Resource) {
	case ACLReadPolicy:
		tokens, roles := list("token"), list("role")
		tokens.Policy, roles.Policy = new(input.Identity), new(input.Identity)
		if err = read(tokens, "Policies", input.Identity); err != nil {
			return out, err
		}
		if err = read(roles, "Policies", input.Identity); err != nil {
			return out, err
		}
		var identities []string
		for _, ref := range out.References {
			if ref.Resource == "role" {
				identities = append(identities, ref.Identity)
			}
		}
		slices.Sort(identities)
		for _, id := range identities {
			request := list("token")
			request.Role = new(id)
			if err = read(request, "Roles", id); err != nil {
				return out, err
			}
		}
	case ACLReadRole:
		request := list("token")
		request.Role = new(input.Identity)
		if err = read(request, "Roles", input.Identity); err != nil {
			return out, err
		}
	case ACLReadAuthMethod:
		for _, resource := range []string{"binding-rule", "token"} {
			request := list(resource)
			request.AuthMethod = new(input.Identity)
			if err = read(request, "AuthMethod", input.Identity); err != nil {
				return out, err
			}
		}
	}
	slices.SortFunc(out.References, func(a, b contract.KVHTTPACLImpactReference) int {
		return strings.Compare(a.Resource+"/"+a.Identity, b.Resource+"/"+b.Identity)
	})
	err = ctx.Err()
	out.Complete = err == nil
	return out, err
}

func (l *documentLease) observeHTTPACLImpact(ctx context.Context, input contract.KVHTTPACLImpactRequest) (contract.KVHTTPACLImpact, error) {
	out, err := collectACLImpact(ctx, l.Scope(), input, func(request contract.KVHTTPACLReadRequest) (contract.KVHTTPACLReadResult, error) {
		return l.executeACLImpactRead(ctx, request)
	})
	if err == nil {
		err = l.CheckKVLease()
	}
	if err != nil {
		out.Complete = false
	}
	return out, err
}

func (l *documentLease) executeACLImpactRead(ctx context.Context, request contract.KVHTTPACLReadRequest) (contract.KVHTTPACLReadResult, error) {
	prepared, err := l.PrepareHTTPACLRead(request)
	if err != nil {
		return contract.KVHTTPACLReadResult{Scope: l.Scope(), Request: request}, err
	}
	p := prepared.(*httpACLReadPreparation)
	receipt, err := l.executeLocked(ctx, p.plan)
	r := projectACLReadResult(l.Scope(), request, receipt)
	r.LocalRecordID = p.LocalRecordID()
	return r, err
}

func (l *documentLease) ObserveHTTPACLImpact(parent context.Context, input contract.KVHTTPACLImpactRequest) (contract.KVHTTPACLImpact, error) {
	out := contract.KVHTTPACLImpact{Scope: l.Scope(), Request: input}
	if !validACLImpactRequest(input) {
		return out, ErrInvalid
	}
	if !l.ops.TryLock() {
		return out, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	ctx, done, err := l.request(parent)
	if err != nil {
		return out, err
	}
	defer done()
	if l.destroyPlan != nil || l.planReserved != 0 {
		return out, contract.ErrKVBusy
	}
	// Reserve both the bounded observations and one complete terminal receipt
	// before the first read. No partial response is discarded to satisfy limits.
	if err := l.reserveDestroyPlan(2*input.MaxBytes + pendingReceiptBudget + (8 << 20)); err != nil {
		return out, err
	}
	defer l.releaseDestroyPlan()
	return l.observeHTTPACLImpact(ctx, input)
}

// Exact original bodies detect native link changes even when ModifyIndex,
// Hash and X-Consul-Index do not change. Harmless reordering may conflict too;
// callers must obtain a new preview, never silently weaken the expectation.
func CompareACLImpact(expected, actual contract.KVHTTPACLImpact) error {
	if expected.Creation != nil || actual.Creation != nil {
		return compareACLCreationImpact(expected, actual)
	}
	if !expected.Complete || !actual.Complete || !validACLImpactRequest(expected.Request) || expected.Request != actual.Request || !reflect.DeepEqual(expected.Scope, actual.Scope) || len(expected.Reads) != len(actual.Reads) || len(expected.Reads) == 0 {
		return ErrConflict
	}
	for _, o := range []contract.KVHTTPACLImpact{expected, actual} {
		if o.Bytes != aclImpactCost(o) || o.Bytes > o.Request.MaxBytes || len(o.Reads) > o.Request.MaxReads || o.Rows > o.Request.MaxRows {
			return ErrProtocol
		}
		index := 0
		rebuilt, err := collectACLImpact(context.Background(), o.Scope, o.Request, func(request contract.KVHTTPACLReadRequest) (contract.KVHTTPACLReadResult, error) {
			if index >= len(o.Reads) {
				return contract.KVHTTPACLReadResult{}, ErrProtocol
			}
			r := o.Reads[index].Result
			index++
			return r, nil
		})
		if err != nil || index != len(o.Reads) || !reflect.DeepEqual(rebuilt, o) {
			return ErrProtocol
		}
	}
	for i, read := range expected.Reads {
		if !reflect.DeepEqual(read.Result.Request, actual.Reads[i].Result.Request) || read.BodySHA256 != actual.Reads[i].BodySHA256 {
			return ErrConflict
		}
	}
	if expected.Rows != actual.Rows || !reflect.DeepEqual(expected.References, actual.References) {
		return ErrConflict
	}
	return nil
}

var _ contract.KVHTTPACLImpactBrowsing = (*documentLease)(nil)
