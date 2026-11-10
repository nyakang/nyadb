package kvworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

// Browsing shares the original request lease. It never reserves another
// physical channel, changes the one-shot receipt or clears unknown writes.
func (d *Document) LeaseBrowsingAvailable() bool {
	_, ok := d.lease.(contract.KVLeaseBrowsing)
	return !d.closed && ok
}
func (s *State) RunLeaseBrowser(d *Document) {
	if !s.check(d) {
		return
	}
	browser, ok := d.lease.(contract.KVLeaseBrowsing)
	if !ok {
		d.error = locales.Text("此连接不支持租约浏览")
		return
	}
	authored := d.Request()
	if authored.Method == LeaseBrowseMethod {
		request, err := ParseLeasePageRequest(authored.JSON)
		if err != nil {
			d.error = locales.Text("KV 请求无效")
			return
		}
		s.start(d, "lease-browse", authored, func(ctx context.Context) outcome {
			page, err := browser.BrowseLeases(ctx, request.Clone())
			return outcome{leasePage: &page, err: err}
		})
	} else if authored.Method == LeaseDetailMethod {
		request, err := ParseLeaseDetailRequest(authored.JSON)
		if err != nil {
			d.error = locales.Text("KV 请求无效")
			return
		}
		s.start(d, "lease-detail", authored, func(ctx context.Context) outcome {
			detail, err := browser.ObserveLease(ctx, request)
			return outcome{leaseDetail: &detail, err: err}
		})
	}
}
func leasePageRequestEqual(a, b contract.KVLeasePageRequest) bool {
	return a.Limit == b.Limit && (a.After == nil && b.After == nil || a.After != nil && b.After != nil && *a.After == *b.After)
}
func validLeaseObservation(r contract.KVResult, id int64, keys bool) bool {
	if !r.Completed || !r.Attempted || r.WriteOutcomeUnknown || r.ProjectionError != "" || r.Reply == nil || r.Reply.Kind != "lease_ttl" || r.Reply.Lease == nil {
		return false
	}
	l := r.Reply.Lease
	if l.ID != id || id == 0 || l.TTL < -1 || l.GrantedTTL < 0 || !keys && len(l.Keys) != 0 {
		return false
	}
	seen := map[string]bool{}
	for _, key := range l.Keys {
		if len(key) == 0 || len(key) > 4096 || seen[string(key)] {
			return false
		}
		seen[string(key)] = true
	}
	return true
}
func validLeasePage(p contract.KVLeasePage) bool {
	if !p.Complete || p.Request.Limit < 1 || p.Request.Limit > 200 || len(p.Rows) > p.Request.Limit || !p.List.Completed || !p.List.Attempted || p.List.WriteOutcomeUnknown || p.List.ProjectionError != "" || p.List.Reply == nil || p.List.Reply.Kind != "lease_list" || p.List.Reply.Lease == nil {
		return false
	}
	ids := slices.Clone(p.List.Reply.Lease.IDs)
	if len(ids) > 10000 {
		return false
	}
	slices.Sort(ids)
	for i, id := range ids {
		if id == 0 || i > 0 && id == ids[i-1] {
			return false
		}
	}
	if p.Request.After != nil {
		ids = ids[sort.Search(len(ids), func(i int) bool { return ids[i] > *p.Request.After }):]
	}
	return validLeaseWindow(p, ids)
}
func validLeaseWindow(p contract.KVLeasePage, ids []int64) bool {
	if p.More != (len(ids) > p.Request.Limit) {
		return false
	}
	ids = ids[:min(len(ids), p.Request.Limit)]
	if len(ids) != len(p.Rows) {
		return false
	}
	for i, row := range p.Rows {
		if row.ID != ids[i] || !validLeaseObservation(row.Result, row.ID, false) {
			return false
		}
	}
	if p.More {
		return p.NextAfter != nil && len(ids) > 0 && *p.NextAfter == ids[len(ids)-1]
	}
	return p.NextAfter == nil
}
func (s *State) consumeLeaseBrowser(d *Document, job *operation, out outcome) {
	r := &LeaseBrowserReceipt{Scope: job.scope.Clone(), Authored: job.authored, Page: out.leasePage, Detail: out.leaseDetail}
	if out.err != nil {
		r.Error = out.err.Error()
	}
	valid := false
	if job.kind == "lease-browse" && r.Page != nil {
		request, err := ParseLeasePageRequest(job.authored.JSON)
		valid = err == nil && sameEventScope(r.Page.Scope, job.scope) && leasePageRequestEqual(r.Page.Request, request) && validLeasePage(*r.Page)
	}
	if job.kind == "lease-detail" && r.Detail != nil {
		request, err := ParseLeaseDetailRequest(job.authored.JSON)
		valid = err == nil && sameEventScope(r.Detail.Scope, job.scope) && r.Detail.Request == request && validLeaseObservation(r.Detail.Result, request.ID, request.IncludeKeys)
	}
	current := sameEventScope(d.scope, job.scope) && d.lease != nil && d.lease == job.lease && d.lease.CheckKVLease() == nil && !d.needsRecovery
	if !valid && r.Error == "" {
		r.Error = contract.ErrRevoked.Error()
	}
	if !current {
		r.Error = errors.Join(out.err, contract.ErrKVLeaseLost).Error()
	}
	r.preview = leaseBrowserPreview(*r)
	d.browserLast = r
	d.selectedBrowser, d.selectedRenewal, d.selectedEvents, d.selectedUnknown = true, false, false, false
	d.browserReceiptChoice = 0
	d.permit = nil
	if valid && current && out.err == nil {
		if r.Page != nil {
			d.browserPage = r
			d.browserRow = -1
			d.browserSelectedID = 0
			d.browserReceiptChoice = 1
		} else {
			d.browserDetail = r
			d.browserKeyRow = -1
			d.browserReceiptChoice = 2
		}
	}
	if r.Error != "" {
		d.error = r.Error
	}
}
func (s *State) SelectLease(d *Document, id int64) {
	if !s.active(d) || d.Busy() || d.browserPage == nil || d.browserPage.Page == nil {
		return
	}
	for i, row := range d.browserPage.Page.Rows {
		if row.ID == id {
			d.browserSelectedID, d.browserRow = id, i
			return
		}
	}
}
func (s *State) ReadSelectedLease(d *Document) {
	if !s.check(d) || d.browserSelectedID == 0 || d.browserPage == nil || !sameEventScope(d.browserPage.Scope, d.scope) {
		return
	}
	raw, _ := json.MarshalIndent(map[string]any{"ID": strconv.FormatInt(d.browserSelectedID, 10), "keys": true}, "", "  ")
	s.SetMethod(d, LeaseDetailMethod)
	d.Editor.Buffer.Set(string(raw))
	s.RunLeaseBrowser(d)
}
func (s *State) NextLeasePage(d *Document) {
	if !s.check(d) || d.browserPage == nil || d.browserPage.Page == nil || !sameEventScope(d.browserPage.Scope, d.scope) {
		return
	}
	p := d.browserPage.Page
	if !p.Complete || !p.More || p.NextAfter == nil {
		return
	}
	raw, _ := json.MarshalIndent(map[string]any{"limit": p.Request.Limit, "after": strconv.FormatInt(*p.NextAfter, 10)}, "", "  ")
	s.SetMethod(d, LeaseBrowseMethod)
	d.Editor.Buffer.Set(string(raw))
	s.RunLeaseBrowser(d)
}
func (s *State) ClearLeaseBrowser(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() {
		return
	}
	d.browserLast, d.browserPage, d.browserDetail = nil, nil, nil
	d.browserSelectedID, d.browserRow = 0, -1
	d.permit = nil
}
