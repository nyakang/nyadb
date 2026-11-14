package kvworkspace

import (
	"bytes"
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (x *httpManagementState) ensureDrafts() {
	if x.draft.Operation == "" {
		x.draft = httpManagementDraft{Operation: "agent-members", Options: "{}"}
		x.saved = x.draft
	}
	if x.drafts == nil {
		x.drafts = map[string]httpManagementDraft{x.draft.Operation: x.draft}
	}
	if x.savedDrafts == nil {
		x.savedDrafts = map[string]httpManagementDraft{x.saved.Operation: x.saved}
	}
}

var capabilityProbes = []contract.KVHTTPManagementRequest{
	{Operation: "agent-self"}, {Operation: "probe-catalog"}, {Operation: "probe-health"}, {Operation: "probe-sessions"},
	{Operation: "probe-acl"}, {Operation: "probe-auth-methods"}, {Operation: "probe-binding-rules"}, {Operation: "probe-acl-templates"},
	{Operation: "namespace-list"}, {Operation: "partition-list"}, {Operation: "config-list", Kind: "service-defaults"},
	{Operation: "intentions-list"}, {Operation: "peering-list"}, {Operation: "exported-services"},
	{Operation: "query-list"}, {Operation: "event-list"}, {Operation: "coordinate-nodes"},
	{Operation: "autopilot-config"}, {Operation: "raft-config"}, {Operation: "keyring-list"}, {Operation: "usage"}, {Operation: "license-get"},
	{Operation: "audit-hash", Body: []byte(`{"Input":"nyadb-capability-probe"}`)},
}

func capabilityCaption(r contract.KVHTTPManagementResult) string {
	h := r.Receipt.HTTP
	if h == nil || !h.Captured || !h.EOF {
		return "未知"
	}
	if (h.Status == 401 || h.Status == 403) && bytes.EqualFold(bytes.TrimSpace(h.Body), []byte("ACL support disabled")) {
		return "已禁用"
	}
	if h.Status == 401 || h.Status == 403 {
		return "权限不足"
	}
	if h.Status == 404 || h.Status == 405 || h.Status == 501 {
		return "不支持"
	}
	if h.Status == 200 && r.Receipt.Completed {
		return "可用"
	}
	return "未知"
}
func (s *State) RunHTTPCapabilities(d *Document) {
	if !s.check(d) || d.FileBusy() || d.httpManagement == nil || !d.HTTPManagementAvailable() {
		return
	}
	d.clearHTTPManagementPlan()
	facet := d.lease.(contract.KVHTTPManaging)
	scope := d.scope.Clone()
	s.start(d, "http-capabilities", contract.KVRequest{}, func(ctx context.Context) outcome {
		var results []contract.KVHTTPManagementResult
		var failure error
		budget := 0
		for _, input := range capabilityProbes {
			// Stop scheduling at 32 MiB. The last bounded native response can
			// add up to 48 MiB; keep its complete original receipt, giving a
			// finite 80 MiB body bound instead of dropping dispatched evidence.
			if budget >= 32<<20 {
				failure = contract.ErrUnsupported
				break
			}
			if err := ctx.Err(); err != nil {
				failure = errors.Join(failure, err)
				break
			}
			plan, err := facet.PrepareHTTPManagement(input.Clone())
			if err != nil || plan == nil {
				failure = errors.Join(failure, err)
				continue
			}
			if plan.Writes() || !sameEventScope(plan.Scope(), scope) {
				failure = contract.ErrRevoked
				break
			}
			r, err := facet.ExecuteHTTPManagement(ctx, plan)
			if r.Receipt.HTTP != nil {
				budget += len(r.Receipt.HTTP.Body)
			}
			results = append(results, r.Clone())
			// HTTP rejections describe capability availability and do not stop
			// independent probes. Transport retirement is acknowledged by lease.
			if err != nil && r.Receipt.HTTP == nil {
				failure = errors.Join(failure, err)
				break
			}
		}
		return outcome{capabilities: results, err: failure}
	})
}
func (s *State) consumeHTTPCapabilities(d *Document, job *operation, out outcome) {
	if d.httpManagement == nil {
		d.httpManagement = &httpManagementState{}
	}
	for _, result := range out.capabilities {
		if result.LocalRecordID == "" || !sameEventScope(result.Scope, job.scope) {
			d.error = contract.ErrRevoked.Error()
			return
		}
	}
	d.httpManagement.capabilities = out.capabilities
	d.httpManagement.revision++
	d.httpManagement.exportConsent = false
	d.permit = nil
}
