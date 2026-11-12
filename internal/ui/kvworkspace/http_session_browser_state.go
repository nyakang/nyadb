package kvworkspace

import (
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

const httpSessionPageSize = 50

type httpSessionTTLFilter uint8

const (
	httpSessionTTLAll httpSessionTTLFilter = iota
	httpSessionTTLConfigured
	httpSessionTTLNone
	httpSessionTTLUnavailable
)

// This UI-thread state only indexes a captured original receipt. Filtering,
// paging and selecting never perform a read or confer write authority.
// The document must release this reference when its original receipt changes
// or closes. A selected row is bound to the receipt pointer and original UUID.
type httpSessionBrowserState struct {
	columns         ui.ListState
	detail, preview string
	receipt         *Receipt
	query           string
	ttl             httpSessionTTLFilter
	appliedQuery    string
	appliedTTL      httpSessionTTLFilter
	indexed         bool
	indices         []int
	page            int
	selectedID      string
}

func sessionReceiptRows(r *Receipt) []contract.KVHTTPSession {
	if r == nil || r.Result.HTTP == nil {
		return nil
	}
	h := r.Result.HTTP
	// Scope identifies the engine; the response Protocol retains the actual
	// HTTP wire version (for example HTTP/1.1), not another engine identity.
	if r.Scope.HTTP.Protocol != "consul-http" || h.Request.Method != "GET" || h.Status != 200 || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.WriteOutcomeUnknown || r.Result.WriteOutcomeUnknown || h.Sessions == nil {
		return nil
	}
	path := h.Request.Path
	if path != "/v1/session/list" && (!strings.HasPrefix(path, "/v1/session/info/") || len(path) == len("/v1/session/info/")) && (!strings.HasPrefix(path, "/v1/session/node/") || len(path) == len("/v1/session/node/")) {
		return nil
	}
	return h.Sessions.Rows
}

func sessionTTLClass(row contract.KVHTTPSession) httpSessionTTLFilter {
	if !slices.Contains(row.PresentFields, "TTL") || slices.Contains(row.NullFields, "TTL") {
		return httpSessionTTLUnavailable
	}
	if row.TTL == "" {
		return httpSessionTTLNone
	}
	duration, err := time.ParseDuration(row.TTL)
	if err != nil || duration < 0 {
		return httpSessionTTLUnavailable
	}
	if duration == 0 {
		return httpSessionTTLNone
	}
	return httpSessionTTLConfigured
}

func (b *httpSessionBrowserState) bind(r *Receipt) {
	if b.receipt != r {
		b.receipt = r
		b.indexed = false
	}
	if b.indexed && b.appliedQuery == b.query && b.appliedTTL == b.ttl {
		return
	}
	b.page, b.selectedID = 0, ""
	b.columns.ScrollTo(0, ui.Start)
	b.detail, b.preview = "", ""
	b.indices = b.indices[:0]
	query := strings.ToLower(b.query)
	for i, row := range sessionReceiptRows(r) {
		if b.ttl != httpSessionTTLAll && b.ttl != sessionTTLClass(row) {
			continue
		}
		matches := query == ""
		for _, value := range []string{row.ID, row.Name, row.Node, row.TTL, row.Behavior, row.Namespace, row.Partition} {
			if !matches && strings.Contains(strings.ToLower(value), query) {
				matches = true
			}
		}
		if matches {
			b.indices = append(b.indices, i)
		}
	}
	b.appliedQuery, b.appliedTTL, b.indexed = b.query, b.ttl, true
}

func (b *httpSessionBrowserState) pageCount() int {
	return max(1, (len(b.indices)+httpSessionPageSize-1)/httpSessionPageSize)
}

func (b *httpSessionBrowserState) pageIndices() []int {
	start := min(b.page*httpSessionPageSize, len(b.indices))
	return b.indices[start:min(start+httpSessionPageSize, len(b.indices))]
}

func (b *httpSessionBrowserState) movePage(r *Receipt, page int) bool {
	if r != b.receipt || !b.indexed || b.query != b.appliedQuery || b.ttl != b.appliedTTL || page < 0 || page >= b.pageCount() {
		return false
	}
	if page != b.page {
		b.page, b.selectedID = page, ""
		b.columns.ScrollTo(0, ui.Start)
		b.detail, b.preview = "", ""
	}
	return true
}

func (b *httpSessionBrowserState) selectRow(r *Receipt, page, row int) bool {
	if r != b.receipt || page != b.page || b.query != b.appliedQuery || b.ttl != b.appliedTTL || !b.indexed {
		return false
	}
	indices := b.pageIndices()
	rows := sessionReceiptRows(r)
	if row < 0 || row >= len(indices) || indices[row] >= len(rows) {
		return false
	}
	b.selectedID = rows[indices[row]].ID
	return true
}

func (b *httpSessionBrowserState) selected(r *Receipt) (contract.KVHTTPSession, bool) {
	if r != b.receipt || b.selectedID == "" || b.query != b.appliedQuery || b.ttl != b.appliedTTL {
		return contract.KVHTTPSession{}, false
	}
	rows := sessionReceiptRows(r)
	for _, i := range b.pageIndices() {
		if i >= len(rows) {
			return contract.KVHTTPSession{}, false
		}
		row := rows[i]
		if row.ID == b.selectedID {
			return row.Clone(), true
		}
	}
	return contract.KVHTTPSession{}, false
}

func (b *httpSessionBrowserState) close() { *b = httpSessionBrowserState{} }
