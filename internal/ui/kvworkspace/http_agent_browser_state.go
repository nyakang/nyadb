package kvworkspace

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func agentReceiptProjection(r *Receipt) *contract.KVHTTPAgentList {
	if r == nil || r.Result.HTTP == nil || r.Scope.HTTP.Protocol != "consul-http" {
		return nil
	}
	h := r.Result.HTTP
	if h.Request.Method != "GET" || h.Request.Body != nil || h.Status != 200 || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.WriteOutcomeUnknown || r.Result.WriteOutcomeUnknown || h.Agent == nil || !h.Agent.ProjectionComplete {
		return nil
	}
	if h.Request.Path == "/v1/agent/services" && h.Agent.Kind == "services" || h.Request.Path == "/v1/agent/checks" && h.Agent.Kind == "checks" {
		return h.Agent
	}
	return nil
}

// A selected row belongs to this immutable original receipt and map key.
// Filtering and paging only index that receipt; neither schedules an Agent read.
type httpAgentBrowserState struct {
	columns                ui.ListState
	receipt                *Receipt
	query, appliedQuery    string
	status, appliedStatus  httpHealthStatusFilter
	indexed                bool
	indices                []int
	page, selectedOriginal int
	detail                 bool
	preview                string
}

func (b *httpAgentBrowserState) bind(r *Receipt) {
	if b.receipt != r {
		b.receipt, b.indexed = r, false
	}
	if b.indexed && b.query == b.appliedQuery && b.status == b.appliedStatus {
		return
	}
	b.page, b.selectedOriginal, b.detail, b.preview = 0, -1, false, ""
	b.columns.ScrollTo(0, ui.Start)
	b.indices = b.indices[:0]
	query := strings.ToLower(b.query)
	if p := agentReceiptProjection(r); p != nil {
		for i, e := range p.Rows {
			statusMatches := p.Kind != "checks" || healthCheckMatchesStatus(contract.KVHTTPHealthCheck{CheckID: e.ID, Status: e.Status, PresentFields: e.PresentFields, NullFields: e.NullFields}, b.status)
			matches := healthSearch(query, e.Key, e.ID, e.Name, e.Node, e.ServiceID, e.Service, e.Status, e.Notes, e.Output, e.Kind, e.Type, e.Address, e.Namespace, e.Partition) || healthSearch(query, e.Tags...)
			for k, v := range e.Meta {
				matches = matches || healthSearch(query, k, v)
			}
			if statusMatches && matches {
				b.indices = append(b.indices, i)
			}
		}
	}
	b.appliedQuery, b.appliedStatus, b.indexed = b.query, b.status, true
}

func (b *httpAgentBrowserState) pageCount() int {
	return max(1, (len(b.indices)+httpHealthPageSize-1)/httpHealthPageSize)
}
func (b *httpAgentBrowserState) pageIndices() []int {
	start := min(b.page*httpHealthPageSize, len(b.indices))
	return b.indices[start:min(start+httpHealthPageSize, len(b.indices))]
}
func (b *httpAgentBrowserState) current(r *Receipt) bool {
	return b.receipt == r && b.indexed && b.query == b.appliedQuery && b.status == b.appliedStatus && agentReceiptProjection(r) != nil
}
func (b *httpAgentBrowserState) movePage(r *Receipt, page int) bool {
	if !b.current(r) || page < 0 || page >= b.pageCount() {
		return false
	}
	if page != b.page {
		b.page, b.selectedOriginal, b.detail, b.preview = page, -1, false, ""
		b.columns.ScrollTo(0, ui.Start)
	}
	return true
}
func (b *httpAgentBrowserState) selectRow(r *Receipt, page, row int) bool {
	if !b.current(r) || page != b.page {
		return false
	}
	indices := b.pageIndices()
	if row < 0 || row >= len(indices) || indices[row] >= len(agentReceiptProjection(r).Rows) {
		return false
	}
	b.selectedOriginal = indices[row]
	return true
}
func (b *httpAgentBrowserState) selectedIndex(r *Receipt) (int, bool) {
	if !b.current(r) || b.selectedOriginal < 0 || b.selectedOriginal >= len(agentReceiptProjection(r).Rows) {
		return 0, false
	}
	return b.selectedOriginal, slices.Contains(b.pageIndices(), b.selectedOriginal)
}
func (b *httpAgentBrowserState) close() { *b = httpAgentBrowserState{} }
