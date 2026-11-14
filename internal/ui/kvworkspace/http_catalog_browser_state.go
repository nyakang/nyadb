package kvworkspace

import (
	"bytes"
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func catalogReceiptProjection(r *Receipt) *contract.KVHTTPCatalogList {
	if r == nil || r.Result.HTTP == nil || r.Scope.HTTP.Protocol != "consul-http" {
		return nil
	}
	h := r.Result.HTTP
	p := h.Catalog
	if h.Request.Method != "GET" || h.Request.Body != nil || h.Status != 200 || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.WriteOutcomeUnknown || r.Result.WriteOutcomeUnknown || p == nil || !p.ProjectionComplete {
		return nil
	}
	switch p.Kind {
	case "datacenters", "nodes", "services":
		if h.Request.Path == "/v1/catalog/"+p.Kind {
			return p
		}
	case "node", "service":
		prefix := "/v1/catalog/" + p.Kind + "/"
		if strings.HasPrefix(h.Request.Path, prefix) {
			encoded := strings.TrimPrefix(h.Request.Path, prefix)
			name, err := url.PathUnescape(encoded)
			if err == nil && name != "" && url.PathEscape(name) == encoded {
				if p.Kind == "node" && (p.Null || p.NodeServices != nil && p.NodeServices.Node.Node == name) {
					return p
				}
				if p.Kind == "service" {
					for _, i := range p.Instances {
						if i.ServiceName != name {
							return nil
						}
					}
					return p
				}
			}
		}
	}
	return nil
}

func catalogRowCount(p *contract.KVHTTPCatalogList) int {
	switch p.Kind {
	case "datacenters":
		return len(p.Datacenters)
	case "nodes":
		return len(p.Nodes)
	case "services":
		return len(p.Services)
	case "service":
		return len(p.Instances)
	case "node":
		if p.NodeServices != nil {
			return 1 + len(p.NodeServices.Services)
		}
	}
	return 0
}

func catalogRowRaw(r *Receipt, index int) ([]byte, bool) {
	p := catalogReceiptProjection(r)
	if p == nil || index < 0 || index >= catalogRowCount(p) {
		return nil, false
	}
	switch p.Kind {
	case "nodes":
		return p.Nodes[index].Raw, true
	case "services":
		return p.Services[index].Raw, true
	case "service":
		return p.Instances[index].Raw, true
	case "node":
		if index == 0 {
			return p.NodeServices.Node.Raw, true
		}
		return p.NodeServices.Services[index-1].Raw, true
	case "datacenters":
		// Decode the selected original array value without re-encoding its text.
		// Complete HTTP bytes retain the server's authored JSON escape spelling.
		d := json.NewDecoder(bytes.NewReader(r.Result.HTTP.Body))
		if token, err := d.Token(); err != nil || token != json.Delim('[') {
			return nil, false
		}
		for i := 0; i <= index; i++ {
			var raw json.RawMessage
			if d.Decode(&raw) != nil {
				return nil, false
			}
			if i == index {
				return raw, true
			}
		}
	}
	return nil, false
}

// Selection belongs to one original receipt and native array/map ordinal.
// Local search and paging do not acquire leases or schedule another read.
type httpCatalogBrowserState struct {
	columns                ui.ListState
	receipt                *Receipt
	query, appliedQuery    string
	indexed                bool
	indices                []int
	page, selectedOriginal int
	detail                 bool
	preview                string
}

func (b *httpCatalogBrowserState) bind(r *Receipt) {
	if b.receipt != r {
		b.receipt, b.indexed = r, false
	}
	if b.indexed && b.query == b.appliedQuery {
		return
	}
	b.page, b.selectedOriginal, b.detail, b.preview = 0, -1, false, ""
	b.columns.ScrollTo(0, ui.Start)
	b.indices = b.indices[:0]
	query := strings.ToLower(b.query)
	if p := catalogReceiptProjection(r); p != nil {
		for i := 0; i < catalogRowCount(p); i++ {
			if catalogRowMatches(p, i, query) {
				b.indices = append(b.indices, i)
			}
		}
	}
	b.appliedQuery, b.indexed = b.query, true
}

func catalogRowMatches(p *contract.KVHTTPCatalogList, index int, query string) bool {
	if query == "" {
		return true
	}
	if healthSearch(query, catalogRowCells(p, index)...) {
		return true
	}
	matchMap := func(m map[string]string) bool {
		for k, v := range m {
			if healthSearch(query, k, v) {
				return true
			}
		}
		return false
	}
	switch p.Kind {
	case "nodes":
		return matchMap(p.Nodes[index].Meta) || matchMap(p.Nodes[index].TaggedAddresses)
	case "service":
		i := p.Instances[index]
		return healthSearch(query, i.ServiceTags...) || matchMap(i.ServiceMeta) || matchMap(i.NodeMeta) || matchMap(i.TaggedAddresses)
	case "node":
		if index == 0 {
			return matchMap(p.NodeServices.Node.Meta) || matchMap(p.NodeServices.Node.TaggedAddresses)
		}
		s := p.NodeServices.Services[index-1].Service
		return healthSearch(query, s.Tags...) || matchMap(s.Meta)
	case "services":
		return healthSearch(query, p.Services[index].Tags...)
	}
	return false
}

func (b *httpCatalogBrowserState) pageCount() int {
	return max(1, (len(b.indices)+httpHealthPageSize-1)/httpHealthPageSize)
}
func (b *httpCatalogBrowserState) pageIndices() []int {
	start := min(b.page*httpHealthPageSize, len(b.indices))
	return b.indices[start:min(start+httpHealthPageSize, len(b.indices))]
}
func (b *httpCatalogBrowserState) current(r *Receipt) bool {
	return b.receipt == r && b.indexed && b.query == b.appliedQuery && catalogReceiptProjection(r) != nil
}
func (b *httpCatalogBrowserState) movePage(r *Receipt, page int) bool {
	if !b.current(r) || page < 0 || page >= b.pageCount() {
		return false
	}
	if page != b.page {
		b.page, b.selectedOriginal, b.detail, b.preview = page, -1, false, ""
		b.columns.ScrollTo(0, ui.Start)
	}
	return true
}
func (b *httpCatalogBrowserState) selectRow(r *Receipt, page, row int) bool {
	if !b.current(r) || page != b.page {
		return false
	}
	indices := b.pageIndices()
	if row < 0 || row >= len(indices) {
		return false
	}
	b.selectedOriginal = indices[row]
	return true
}
func (b *httpCatalogBrowserState) selectedIndex(r *Receipt) (int, bool) {
	if !b.current(r) || b.selectedOriginal < 0 || b.selectedOriginal >= catalogRowCount(catalogReceiptProjection(r)) {
		return 0, false
	}
	return b.selectedOriginal, slices.Contains(b.pageIndices(), b.selectedOriginal)
}
func (b *httpCatalogBrowserState) close() { *b = httpCatalogBrowserState{} }
