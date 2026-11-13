package kvworkspace

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/locales"
)

const leaseGrantMethod = "/etcdserverpb.Lease/LeaseGrant"
const leaseRevokeMethod = "/etcdserverpb.Lease/LeaseRevoke"

// LeaseManagementReceipt supplements the original one-shot receipt. A revoke
// observation is evidence of an earlier read, never a server-side precondition.
type LeaseManagementReceipt struct {
	DocumentID                uint64
	Kind                      string
	Scope                     contract.KVScope
	RequestedID, RequestedTTL int64
	Observation               *contract.KVLeaseDetail
}

func (r *LeaseManagementReceipt) clone() *LeaseManagementReceipt {
	if r == nil {
		return nil
	}
	c := *r
	c.Scope = r.Scope.Clone()
	if r.Observation != nil {
		o := r.Observation.Clone()
		c.Observation = &o
	}
	return &c
}

type leaseManagementDraft struct {
	kind, id, ttl string
	lease         contract.KVRequestLease
	scope         contract.KVScope
	editor        contract.KVRequest
	open          bool
}

func managementInteger(text string) (int64, error) {
	if len(text) > 20 {
		return 0, jsondocument.ErrInvalid
	}
	raw, _ := json.Marshal(text)
	return signedLeaseInt(raw)
}

func leaseManagementRequest(kind string, id, ttl int64) contract.KVRequest {
	f := map[string]string{"ID": strconv.FormatInt(id, 10)}
	if kind == leaseGrantMethod {
		f["TTL"] = strconv.FormatInt(ttl, 10)
	}
	raw, _ := json.Marshal(f)
	return contract.KVRequest{Method: kind, JSON: string(raw)}
}

// Parse only the fields this form authorizes, including protobuf's zero defaults.
func managementRequestEqual(a, b contract.KVRequest) bool {
	if a.Method != b.Method {
		return false
	}
	parse := func(r contract.KVRequest) (int64, int64, error) {
		var f map[string]json.RawMessage
		if jsondocument.Decode([]byte(r.JSON), &f, inputLimit) != nil || f == nil {
			return 0, 0, jsondocument.ErrInvalid
		}
		for k := range f {
			if k != "ID" && k != "id" && (r.Method != leaseGrantMethod || k != "TTL" && k != "ttl") {
				return 0, 0, jsondocument.ErrInvalid
			}
		}
		if f["ID"] != nil && f["id"] != nil || f["TTL"] != nil && f["ttl"] != nil {
			return 0, 0, jsondocument.ErrInvalid
		}
		id, ttl := int64(0), int64(0)
		for _, field := range []string{"ID", "TTL"} {
			raw := f[field]
			if raw == nil {
				if field == "ID" {
					raw = f["id"]
				} else {
					raw = f["ttl"]
				}
			}
			if raw == nil {
				continue
			}
			n, err := signedLeaseInt(raw)
			if err != nil {
				return 0, 0, err
			}
			if field == "ID" {
				id = n
			} else {
				ttl = n
			}
		}
		return id, ttl, nil
	}
	ai, at, ae := parse(a)
	bi, bt, be := parse(b)
	return ae == nil && be == nil && ai == bi && at == bt
}

func (s *State) OpenLeaseGrant(d *Document) {
	if !s.check(d) || !d.LeaseBrowsingAvailable() || d.hasUnknownWrites() {
		return
	}
	s.CancelConfirmation(d)
	d.leaseManagement = &leaseManagementDraft{kind: leaseGrantMethod, id: "0", ttl: "60", lease: d.lease, scope: d.scope.Clone(), editor: d.Request(), open: true}
	d.permit = nil
}

func (s *State) OpenLeaseRevoke(d *Document) {
	if !s.check(d) || !d.LeaseBrowsingAvailable() || d.hasUnknownWrites() {
		return
	}
	id := int64(0)
	if d.browserReceiptChoice == 1 && d.browserPage != nil && sameEventScope(d.browserPage.Scope, d.scope) {
		id = d.browserSelectedID
	} else if d.browserDetail != nil && d.browserDetail.Detail != nil && sameEventScope(d.browserDetail.Scope, d.scope) {
		id = d.browserDetail.Detail.Request.ID
	}
	s.CancelConfirmation(d)
	d.leaseManagement = &leaseManagementDraft{kind: leaseRevokeMethod, id: strconv.FormatInt(id, 10), lease: d.lease, scope: d.scope.Clone(), editor: d.Request(), open: true}
	d.permit = nil
}

func (s *State) PrepareLeaseManagement(d *Document) {
	if !s.check(d) || d.leaseManagement == nil || d.hasUnknownWrites() {
		return
	}
	p := d.leaseManagement
	if p.lease != d.lease || p.editor != d.Request() || !sameEventScope(p.scope, d.scope) {
		d.leaseManagement = nil
		return
	}
	id, err := managementInteger(p.id)
	if err != nil || p.kind == leaseRevokeMethod && id == 0 {
		d.error = locales.Text("请输入完整的租约 ID；撤销 ID 不能为零")
		return
	}
	if p.kind == leaseGrantMethod {
		ttl, err := managementInteger(p.ttl)
		if err != nil || ttl <= 0 {
			d.error = locales.Text("授予 TTL 必须为正整数秒")
			return
		}
		s.prepareLeaseManagementConfirmation(d, p, &LeaseManagementReceipt{DocumentID: d.id, Kind: p.kind, Scope: p.scope.Clone(), RequestedID: id, RequestedTTL: ttl})
		return
	}
	browser, ok := p.lease.(contract.KVLeaseBrowsing)
	if !ok || p.kind != leaseRevokeMethod {
		return
	}
	request := contract.KVLeaseDetailRequest{ID: id, IncludeKeys: true}
	raw, _ := json.Marshal(map[string]any{"ID": strconv.FormatInt(id, 10), "keys": true})
	authored := contract.KVRequest{Method: LeaseDetailMethod, JSON: string(raw)}
	s.start(d, "lease-revoke-observe", authored, func(ctx context.Context) outcome {
		o, err := browser.ObserveLease(ctx, request)
		return outcome{leaseDetail: &o, err: err}
	})
	d.job.managementDraft = p
	d.job.management = &LeaseManagementReceipt{DocumentID: d.id, Kind: p.kind, Scope: p.scope.Clone(), RequestedID: id}
}

func (s *State) prepareLeaseManagementConfirmation(d *Document, draft *leaseManagementDraft, r *LeaseManagementReceipt) {
	authored := leaseManagementRequest(r.Kind, r.RequestedID, r.RequestedTTL)
	plan, err := draft.lease.Prepare(authored)
	if err != nil || plan == nil || !plan.Writes() || !managementRequestEqual(authored, plan.Snapshot()) {
		d.error = locales.Text("KV 请求无效")
		if err != nil {
			d.error += ": " + err.Error()
		}
		return
	}
	d.leaseManagement = nil
	d.error = ""
	d.clearConfirmation()
	d.confirmation = &confirmation{lease: draft.lease, plan: plan, authored: authored, editor: draft.editor, management: r.clone(), open: true}
}

func (s *State) consumeRevokeObservation(d *Document, job *operation, out outcome) {
	readJob := *job
	readJob.kind = "lease-detail"
	s.consumeLeaseBrowser(d, &readJob, out)
	p := job.managementDraft
	if p == nil || d.leaseManagement != p || p.editor != d.Request() || p.lease != d.lease || p.lease != job.lease || !sameEventScope(p.scope, job.scope) || !sameEventScope(d.scope, job.scope) || d.hasUnknownWrites() || !s.check(d) || out.err != nil || out.leaseDetail == nil || !sameEventScope(out.leaseDetail.Scope, job.scope) || out.leaseDetail.Request != (contract.KVLeaseDetailRequest{ID: job.management.RequestedID, IncludeKeys: true}) || !validLeaseObservation(out.leaseDetail.Result, job.management.RequestedID, true) {
		return
	}
	r := job.management.clone()
	o := out.leaseDetail.Clone()
	r.Observation = &o
	s.prepareLeaseManagementConfirmation(d, p, r)
}

// A shortcut prepares the existing continuous authorization. Starting remains
// a separate checkbox and confirmation; selection never changes a running plan.
func (s *State) PrepareRenewalFromLeaseDetail(d *Document) {
	if !s.check(d) || d.hasUnknownWrites() || d.browserDetail == nil || d.browserDetail.Detail == nil || !sameEventScope(d.browserDetail.Scope, d.scope) {
		return
	}
	r := d.browserDetail.Detail
	if !validLeaseObservation(r.Result, r.Request.ID, r.Request.IncludeKeys) || r.Result.Reply.Lease.TTL < 0 {
		return
	}
	s.SetMethod(d, RenewalMethod)
	d.Editor.Buffer.Set(leaseManagementRequest(leaseRevokeMethod, r.Request.ID, 0).JSON)
	s.PrepareRenewal(d)
}

func (s *State) ReadGrantedLease(d *Document) {
	if !s.check(d) || d.last == nil || d.last.Error != "" || !sameEventScope(d.last.Scope, d.scope) {
		return
	}
	id, ok := acknowledgedGrantID(d.last)
	if !ok {
		return
	}
	raw, _ := json.Marshal(map[string]any{"ID": strconv.FormatInt(id, 10), "keys": true})
	s.SetMethod(d, LeaseDetailMethod)
	d.Editor.Buffer.Set(string(raw))
	s.RunLeaseBrowser(d)
}

func acknowledgedGrantID(receipt *Receipt) (int64, bool) {
	if receipt == nil || receipt.Error != "" {
		return 0, false
	}
	r := receipt.Result
	if !r.Attempted || !r.Completed || r.WriteOutcomeUnknown || len(r.Native) == 0 || r.ProjectionError != "" || r.Request.Method != leaseGrantMethod || r.Reply == nil || r.Reply.Kind != "lease_grant" || r.Reply.Lease == nil || r.Reply.Lease.ID == 0 || r.Reply.Lease.TTL <= 0 || r.Reply.Lease.Error != "" {
		return 0, false
	}
	return r.Reply.Lease.ID, true
}
