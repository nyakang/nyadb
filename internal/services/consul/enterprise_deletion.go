package consul

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

var namespaceConfigKinds = []string{"api-gateway", "bound-api-gateway", "file-system-certificate", "http-route", "ingress-gateway", "inline-certificate", "jwt-provider", "service-defaults", "service-intentions", "service-resolver", "service-router", "service-splitter", "tcp-route", "terminating-gateway"}
var partitionConfigKinds = []string{"control-plane-request-limit", "exported-services", "mesh", "product-usage", "proxy-defaults", "rate-limit", "sameness-group"}

type httpEnterpriseDeletionPreparation struct {
	*httpManagementPreparation
	impact   contract.KVHTTPEnterpriseImpact
	mu       sync.Mutex
	closed   bool
	reserved int64
}

func (p *httpEnterpriseDeletionPreparation) Impact() contract.KVHTTPEnterpriseImpact {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.impact.Clone()
}
func (p *httpEnterpriseDeletionPreparation) Close() error {
	p.owner.ops.Lock()
	defer p.owner.ops.Unlock()
	p.closeLocked()
	return nil
}
func (p *httpEnterpriseDeletionPreparation) closeLocked() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.impact = contract.KVHTTPEnterpriseImpact{}
	p.mu.Unlock()
	receiptBudgetMu.Lock()
	p.owner.s.reserved -= p.reserved
	p.owner.planReserved -= p.reserved
	p.reserved = 0
	receiptBudgetMu.Unlock()
	if p.owner.enterprisePlan == p {
		p.owner.enterprisePlan = nil
	}
}
func (l *documentLease) PrepareHTTPEnterpriseDeletion(parent context.Context, in contract.KVHTTPManagementRequest) (contract.KVHTTPEnterpriseDeletionPreparation, contract.KVHTTPEnterpriseImpact, error) {
	out := contract.KVHTTPEnterpriseImpact{Scope: l.Scope(), Request: in.Clone()}
	if in.Operation != "namespace-delete" && in.Operation != "partition-delete" {
		return nil, out, ErrUnsupported
	}
	if in.Identity == "default" || in.Identity == "" || strings.ContainsAny(in.Identity, "/ *?\\") {
		return nil, out, ErrInvalid
	}
	native, err := ManagementRequest(in)
	if err != nil {
		return nil, out, err
	}
	if l.s.cfg.ReadOnly {
		return nil, out, ErrUnsupported
	}
	if l.writeUnknown.Load() {
		return nil, out, contract.ErrKVWriteUnknown
	}
	if !l.ops.TryLock() {
		return nil, out, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	if l.enterprisePlan != nil || l.destroyPlan != nil || l.aclMutationPlan != nil {
		return nil, out, contract.ErrKVBusy
	}
	reservation := int64(128 << 20)
	receiptBudgetMu.Lock()
	if l.cacheBytes+l.archiveBytes()+l.planReserved+reservation+pendingReceiptBudget > documentReceiptBudget || l.s.retained+l.s.reserved+reservation+pendingReceiptBudget > l.s.budget {
		receiptBudgetMu.Unlock()
		return nil, out, ErrBounds
	}
	l.s.reserved += reservation
	l.planReserved += reservation
	receiptBudgetMu.Unlock()
	retained := false
	defer func() {
		if !retained {
			receiptBudgetMu.Lock()
			l.s.reserved -= reservation
			l.planReserved -= reservation
			receiptBudgetMu.Unlock()
		}
	}()
	ctx, done, err := l.request(parent)
	if err != nil {
		return nil, out, err
	}
	defer done()
	if err = l.connect(ctx); err != nil {
		return nil, out, err
	}
	out, err = l.observeEnterpriseImpact(ctx, in)
	if err != nil || !out.Complete {
		return nil, out, errors.Join(err, ErrProtocol)
	}
	envelope, err := documentRequestEnvelope(native)
	if err != nil {
		return nil, out, err
	}
	raw := &documentPreparation{owner: l, native: native, request: envelope, write: true, privateManagement: true, managementRequest: in.Clone(), aclRecordID: rand.Text()}
	base := &httpManagementPreparation{owner: l, input: in.Clone(), scope: l.Scope(), plan: raw}
	p := &httpEnterpriseDeletionPreparation{httpManagementPreparation: base, impact: out.Clone(), reserved: reservation}
	l.enterprisePlan = p
	retained = true
	return p, out, nil
}
func (l *documentLease) observeEnterpriseImpact(ctx context.Context, in contract.KVHTTPManagementRequest) (out contract.KVHTTPEnterpriseImpact, err error) {
	out = contract.KVHTTPEnterpriseImpact{Scope: l.Scope(), Request: in.Clone(), Complete: true}
	budget, rows := 0, 0
	reads := 0
	read := func(resource, path, namespace, partition string, keys bool) []byte {
		if ctx.Err() != nil || reads >= 256 || budget >= 64<<20 {
			out.Complete = false
			out.Missing = append(out.Missing, resource)
			return nil
		}
		reads++
		query := url.Values{}
		if l.s.cfg.Datacenter != "" {
			query.Set("dc", l.s.cfg.Datacenter)
		}
		if partition != "" {
			query.Set("partition", partition)
		}
		if namespace != "" {
			query.Set("ns", namespace)
		}
		if keys {
			query.Set("keys", "")
		}
		query.Set("consistent", "")
		// Scope authority is exclusive to this private inventory plan. Generic
		// request/query decoders cannot construct enterprise-impact preparations.
		native := Plan{owner: l.client, request: Request{Method: "GET", Path: path, Query: query}, kind: "enterprise-impact", used: &atomic.Bool{}}
		response, e := l.client.Execute(ctx, native)
		r := documentResult(contract.KVRequest{}, response)
		h := r.HTTP
		budget += int(resultCost(&r))
		o := contract.KVHTTPEnterpriseImpactObservation{Resource: resource, Namespace: namespace, Partition: partition, HTTP: r.Clone()}
		var v any
		if e == nil && h.Status == 200 && jsondocument.Decode(h.Body, &v, responseLimit) == nil {
			switch collection := v.(type) {
			case []any:
				o.Count = len(collection)
				o.Complete = true
			case map[string]any:
				o.Count = len(collection)
				o.Complete = true
			}
		}
		if keys && h.Status == 404 && h.Captured && h.EOF {
			o.Count = 0
			o.Complete = true
		}
		if h.Metadata.ACLFiltered != nil && *h.Metadata.ACLFiltered {
			out.ACLFiltered = true
			o.Complete = false
		}
		rows += o.Count
		if rows > 10000 || budget > 64<<20 {
			o.Complete = false
		}
		if !o.Complete {
			out.Complete = false
			out.Missing = append(out.Missing, resource)
		}
		out.Observations = append(out.Observations, o)
		if !l.client.Alive() {
			err = l.markLost(e)
		}
		return bytes.Clone(h.Body)
	}
	ns, p := in.Identity, l.s.cfg.Partition
	namespaces := []string{ns}
	if in.Operation == "partition-delete" {
		p = in.Identity
		raw := read("namespaces", "/v1/namespaces", "", p, false)
		var list []struct{ Name string }
		if json.Unmarshal(raw, &list) != nil || list == nil || len(list) > 10000 {
			out.Complete = false
			return out, err
		}
		namespaces = nil
		seen := map[string]bool{}
		for _, item := range list {
			if item.Name == "" || strings.ContainsAny(item.Name, "/ *?\\") || seen[item.Name] {
				out.Complete = false
				out.Missing = append(out.Missing, "namespace-identity")
				continue
			}
			seen[item.Name] = true
			namespaces = append(namespaces, item.Name)
		}
		if !seen["default"] {
			out.Complete = false
			out.Missing = append(out.Missing, "namespace-default")
		}
		read("nodes", "/v1/catalog/nodes", "", p, false)
		read("peerings", "/v1/peerings", "", p, false)
		for _, kind := range partitionConfigKinds {
			read("config:"+kind, "/v1/config/"+kind, "default", p, false)
		}
	}
	for _, namespace := range namespaces {
		if ctx.Err() != nil || reads >= 256 || budget >= 64<<20 {
			out.Complete = false
			out.Missing = append(out.Missing, "remaining-namespaces")
			break
		}
		for _, resource := range []struct {
			name, path string
			keys       bool
		}{{"services", "/v1/catalog/services", false}, {"kv", "/v1/kv/", true}, {"checks", "/v1/health/state/any", false}, {"sessions", "/v1/session/list", false}, {"intentions", "/v1/connect/intentions", false}, {"acl-tokens", "/v1/acl/tokens", false}, {"acl-policies", "/v1/acl/policies", false}, {"acl-roles", "/v1/acl/roles", false}, {"acl-auth-methods", "/v1/acl/auth-methods", false}, {"acl-binding-rules", "/v1/acl/binding-rules", false}} {
			read(resource.name, resource.path, namespace, p, resource.keys)
		}
		for _, kind := range namespaceConfigKinds {
			read("config:"+kind, "/v1/config/"+kind, namespace, p, false)
		}
	}
	if ctx.Err() != nil {
		out.Complete = false
		err = errors.Join(err, ctx.Err())
	}
	return out, err
}
func sameEnterpriseImpact(a, b contract.KVHTTPEnterpriseImpact) bool {
	if !a.Complete || !b.Complete || a.ACLFiltered || b.ACLFiltered || !slices.Equal(a.Missing, b.Missing) || len(a.Observations) != len(b.Observations) {
		return false
	}
	for i, left := range a.Observations {
		right := b.Observations[i]
		if left.Resource != right.Resource || left.Namespace != right.Namespace || left.Partition != right.Partition || left.Count != right.Count || left.HTTP.HTTP.Status != right.HTTP.HTTP.Status {
			return false
		}
		var lv, rv any
		if left.HTTP.HTTP.Status == 404 && right.HTTP.HTTP.Status == 404 {
			continue
		}
		if jsondocument.Decode(left.HTTP.HTTP.Body, &lv, responseLimit) != nil || jsondocument.Decode(right.HTTP.HTTP.Body, &rv, responseLimit) != nil {
			return false
		}
		l, _ := json.Marshal(lv)
		r, _ := json.Marshal(rv)
		if !bytes.Equal(l, r) {
			return false
		}
	}
	return true
}
