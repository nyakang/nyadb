package kvworkspace

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

const httpHealthPageSize = 50

func (s *State) healthBrowserReceipt(d *Document) *Receipt {
	if d.selectedHTTPWatch && d.httpWatch != nil {
		return d.httpWatch.healthReceipt
	}
	return s.selectedReceipt(d)
}

type httpHealthStatusFilter uint8

const (
	httpHealthAll httpHealthStatusFilter = iota
	httpHealthPassing
	httpHealthWarning
	httpHealthCritical
	httpHealthMaintenance
	httpHealthOther
)

// Selection identifies an original response position, never a globally unique
// CheckID or ServiceID. Qualified duplicates and repeated node checks remain
// distinct rows. This state only indexes an immutable original UI receipt.
type httpHealthBrowserState struct {
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

func healthReceiptProjection(r *Receipt) *contract.KVHTTPHealthList {
	if r == nil || r.Result.HTTP == nil || r.Scope.HTTP.Protocol != "consul-http" {
		return nil
	}
	h := r.Result.HTTP
	if h.Request.Method != "GET" || h.Status != 200 || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.WriteOutcomeUnknown || r.Result.WriteOutcomeUnknown || h.Health == nil || !h.Health.ProjectionComplete {
		return nil
	}
	for _, kind := range []string{"state", "node", "checks", "service"} {
		prefix := "/v1/health/" + kind + "/"
		if strings.HasPrefix(h.Request.Path, prefix) && len(h.Request.Path) > len(prefix) {
			if kind == "service" && h.Health.Kind == "service-instances" || kind != "service" && h.Health.Kind == "checks" {
				return h.Health
			}
		}
	}
	return nil
}

func healthCheckMatchesStatus(c contract.KVHTTPHealthCheck, filter httpHealthStatusFilter) bool {
	if filter == httpHealthAll {
		return true
	}
	if filter == httpHealthMaintenance {
		return c.Maintenance()
	}
	if !slices.Contains(c.PresentFields, "Status") || slices.Contains(c.NullFields, "Status") {
		return filter == httpHealthOther
	}
	switch filter {
	case httpHealthPassing:
		return c.Status == "passing"
	case httpHealthWarning:
		return c.Status == "warning"
	case httpHealthCritical:
		return c.Status == "critical"
	case httpHealthOther:
		return c.Status != "passing" && c.Status != "warning" && c.Status != "critical"
	}
	return false
}

func healthSearch(query string, values ...string) bool {
	if query == "" {
		return true
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func healthRowCount(p *contract.KVHTTPHealthList) int {
	if p == nil {
		return 0
	}
	if p.Kind == "service-instances" {
		return len(p.Instances)
	}
	return len(p.Checks)
}

func (b *httpHealthBrowserState) bind(r *Receipt) {
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
	p := healthReceiptProjection(r)
	if p != nil {
		if p.Kind == "checks" {
			for i, c := range p.Checks {
				if healthCheckMatchesStatus(c, b.status) && (healthSearch(query, c.Node, c.CheckID, c.Name, c.Status, c.Notes, c.Output, c.ServiceID, c.ServiceName, c.Type, c.Namespace, c.Partition) || healthSearch(query, c.ServiceTags...)) {
					b.indices = append(b.indices, i)
				}
			}
		} else {
			for i, v := range p.Instances {
				statusMatches := b.status == httpHealthAll
				searchMatches := healthSearch(query, v.Node.Node, v.Node.ID, v.Node.Address, v.Node.Datacenter, v.Service.Service, v.Service.ID, v.Service.Address, v.Service.Namespace, v.Service.Partition) || healthSearch(query, v.Service.Tags...)
				for _, c := range v.Checks {
					statusMatches = statusMatches || healthCheckMatchesStatus(c, b.status)
					searchMatches = searchMatches || healthSearch(query, c.CheckID, c.Name, c.Status, c.Notes, c.Output)
				}
				for k, value := range v.Service.Meta {
					searchMatches = searchMatches || healthSearch(query, k, value)
				}
				if statusMatches && searchMatches {
					b.indices = append(b.indices, i)
				}
			}
		}
	}
	b.appliedQuery, b.appliedStatus, b.indexed = b.query, b.status, true
}

func (b *httpHealthBrowserState) pageCount() int {
	return max(1, (len(b.indices)+httpHealthPageSize-1)/httpHealthPageSize)
}
func (b *httpHealthBrowserState) pageIndices() []int {
	start := min(b.page*httpHealthPageSize, len(b.indices))
	return b.indices[start:min(start+httpHealthPageSize, len(b.indices))]
}
func (b *httpHealthBrowserState) current(r *Receipt) bool {
	return b.receipt == r && b.indexed && b.query == b.appliedQuery && b.status == b.appliedStatus && healthReceiptProjection(r) != nil
}
func (b *httpHealthBrowserState) movePage(r *Receipt, page int) bool {
	if !b.current(r) || page < 0 || page >= b.pageCount() {
		return false
	}
	if page != b.page {
		b.page, b.selectedOriginal, b.detail, b.preview = page, -1, false, ""
		b.columns.ScrollTo(0, ui.Start)
	}
	return true
}
func (b *httpHealthBrowserState) selectRow(r *Receipt, page, row int) bool {
	if !b.current(r) || page != b.page {
		return false
	}
	indices := b.pageIndices()
	if row < 0 || row >= len(indices) || indices[row] >= healthRowCount(healthReceiptProjection(r)) {
		return false
	}
	b.selectedOriginal = indices[row]
	return true
}
func (b *httpHealthBrowserState) selectedIndex(r *Receipt) (int, bool) {
	if !b.current(r) || b.selectedOriginal < 0 || b.selectedOriginal >= healthRowCount(healthReceiptProjection(r)) {
		return 0, false
	}
	return b.selectedOriginal, slices.Contains(b.pageIndices(), b.selectedOriginal)
}
func (b *httpHealthBrowserState) close() { *b = httpHealthBrowserState{} }
