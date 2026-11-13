package kvworkspace

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

// The root evidence lives only in Receipt.Result. This small projection records
// the original local target and indices without retaining another set of values.
type HTTPSessionKeysReceipt struct {
	Request                      contract.KVHTTPSessionKeysRequest
	Matches                      []int
	Bytes                        int64
	ProjectionComplete, Complete bool
}

func (r *HTTPSessionKeysReceipt) clone() *HTTPSessionKeysReceipt {
	if r == nil {
		return nil
	}
	v := *r
	v.Matches = slices.Clone(r.Matches)
	return &v
}

type httpSessionKeysBrowserState struct {
	columns                                   ui.ListState
	receipt                                   *Receipt
	query, appliedQuery, selectedKey, preview string
	indices                                   []int
	page                                      int
	indexed, detail                           bool
}

func validHTTPSessionUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, ch := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
			continue
		}
		if ch < '0' || ch > '9' {
			if ch < 'a' || ch > 'f' {
				return false
			}
		}
	}
	return true
}

func (d *Document) HTTPSessionKeysAvailable() bool {
	_, ok := d.lease.(contract.KVHTTPSessionKeysBrowsing)
	return !d.closed && d.scope.HTTP.Protocol == "consul-http" && ok
}

// This explicit action reads the entire KV root through the original lease.
// Selection/filtering alone never connects, reads keys or prepares destruction.
func (s *State) ObserveSelectedHTTPSessionKeys(d *Document) {
	if !s.check(d) || !d.HTTPSessionKeysAvailable() || d.httpSessionBrowser == nil {
		return
	}
	r := s.selectedReceipt(d)
	row, ok := d.httpSessionBrowser.selected(r)
	if !ok || !validHTTPSessionUUID(row.ID) || r.Scope.Generation != d.scope.Generation || !sameKVRecoveryIdentity(r.Scope, d.scope) {
		d.error = locales.Text("所选 Session 不属于当前原连接观察")
		return
	}
	facet := d.lease.(contract.KVHTTPSessionKeysBrowsing)
	request := contract.KVHTTPSessionKeysRequest{ID: row.ID, MaxRows: contract.KVHTTPSessionKeysMaxRows, MaxBytes: contract.KVHTTPSessionKeysMaxBytes}
	s.start(d, "http-session-keys", d.Request(), func(ctx context.Context) outcome {
		o, err := facet.ObserveHTTPSessionKeys(ctx, request)
		return outcome{sessionKeys: &o, err: err}
	})
	d.job.sessionKeysRequest = request
}

func validHTTPSessionKeysProjection(o contract.KVHTTPSessionKeysObservation) bool {
	request := o.Request
	h := o.Root.HTTP
	if !o.ProjectionComplete || !validHTTPSessionUUID(request.ID) || request.MaxRows < 1 || request.MaxRows > contract.KVHTTPSessionKeysMaxRows || request.MaxBytes < 1 || request.MaxBytes > contract.KVHTTPSessionKeysMaxBytes || o.Bytes < 1 || o.Bytes > request.MaxBytes || o.Scope.HTTP.Protocol != "consul-http" || o.Root.Request.Method != contract.KVConsulHTTPRequestMethod || h == nil || h.Kind != "kv-read" || h.Request.Method != "GET" || h.Request.Path != "/v1/kv/" || h.Request.Body != nil || len(h.Request.Query) != 1 || !slices.Equal(h.Request.Query["recurse"], []string{""}) || !o.Root.Attempted || !o.Root.Completed || o.Root.WriteOutcomeUnknown || !h.Attempted || !h.Ended || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.WriteOutcomeUnknown || h.ProjectionError != "" || h.CaptureError != "" || o.Root.ProjectionError != "" || h.Status != 200 && h.Status != 404 || len(h.Entries) > request.MaxRows || h.Txn != nil || h.Sessions != nil {
		return false
	}
	if h.Status == 404 && len(h.Entries) != 0 {
		return false
	}
	if h.Status == 200 && len(h.Entries) == 0 {
		var entries []json.RawMessage
		if json.Unmarshal(h.Body, &entries) != nil || entries == nil || len(entries) != 0 {
			return false
		}
	}
	matched := 0
	for i, row := range h.Entries {
		if row.Session == request.ID && !row.SessionNull {
			if matched >= len(o.Matches) || o.Matches[matched] != i {
				return false
			}
			matched++
		}
	}
	return matched == len(o.Matches) && o.Matches != nil && o.Complete == (h.Metadata.ACLFiltered == nil || !*h.Metadata.ACLFiltered)
}

func (s *State) consumeHTTPSessionKeys(d *Document, job *operation, result outcome) {
	o := result.sessionKeys
	if o == nil || job.lease != d.lease || o.Request != job.sessionKeysRequest || o.Scope.Generation != job.scope.Generation || d.scope.Generation != job.scope.Generation || !sameKVRecoveryIdentity(o.Scope, job.scope) || !sameKVRecoveryIdentity(d.scope, job.scope) {
		d.error = locales.Text("所选 Session 不属于当前原连接观察")
		return
	}
	valid := validHTTPSessionKeysProjection(*o)
	projection := &HTTPSessionKeysReceipt{Request: o.Request, Bytes: o.Bytes}
	if valid {
		projection.Matches = slices.Clone(o.Matches)
		projection.ProjectionComplete, projection.Complete = true, o.Complete
	}
	receipt := &Receipt{Scope: o.Scope.Clone(), Authored: o.Root.Request, Result: o.Root.Clone(), HTTPSessionKeys: projection}
	if result.err != nil {
		receipt.Error = result.err.Error()
	}
	d.last = receipt
	d.selectedUnknown = false
	d.httpSessions = false
	d.httpSessionBrowser = nil
	d.httpSessionKeysBrowser = nil
	d.httpSessionKeys = valid
	if s.deps.Status != nil {
		s.deps.Status(ReceiptStatus(o.Root))
	}
	// The original unknown write remains separate even when the new observation
	// contains no held keys. A complete scoped observation grants no write.
}

func (b *httpSessionKeysBrowserState) bind(r *Receipt) {
	if b.receipt == r && b.indexed && b.query == b.appliedQuery {
		return
	}
	b.receipt = r
	b.indexed = true
	b.appliedQuery = b.query
	b.page = 0
	b.selectedKey = ""
	b.detail = false
	b.preview = ""
	b.columns.ScrollTo(0, ui.Start)
	b.indices = b.indices[:0]
	if r == nil || r.HTTPSessionKeys == nil || !r.HTTPSessionKeys.ProjectionComplete || r.Result.HTTP == nil {
		return
	}
	query := strings.ToLower(b.query)
	for _, i := range r.HTTPSessionKeys.Matches {
		if i >= 0 && i < len(r.Result.HTTP.Entries) && strings.Contains(strings.ToLower(r.Result.HTTP.Entries[i].Key), query) {
			b.indices = append(b.indices, i)
		}
	}
}

func (b *httpSessionKeysBrowserState) pageCount() int {
	return max(1, (len(b.indices)+httpSessionPageSize-1)/httpSessionPageSize)
}
func (b *httpSessionKeysBrowserState) pageIndices() []int {
	start := min(b.page*httpSessionPageSize, len(b.indices))
	return b.indices[start:min(start+httpSessionPageSize, len(b.indices))]
}
func (b *httpSessionKeysBrowserState) movePage(r *Receipt, page int) bool {
	if r != b.receipt || b.query != b.appliedQuery || page < 0 || page >= b.pageCount() {
		return false
	}
	if b.page != page {
		b.page = page
		b.selectedKey = ""
		b.detail = false
		b.preview = ""
		b.columns.ScrollTo(0, ui.Start)
	}
	return true
}
func (b *httpSessionKeysBrowserState) selectRow(r *Receipt, page, row int) bool {
	indices := b.pageIndices()
	if r != b.receipt || page != b.page || b.query != b.appliedQuery || row < 0 || row >= len(indices) {
		return false
	}
	b.selectedKey = r.Result.HTTP.Entries[indices[row]].Key
	return true
}
func (b *httpSessionKeysBrowserState) selected(r *Receipt) (contract.KVHTTPEntry, bool) {
	if r != b.receipt || b.selectedKey == "" || b.query != b.appliedQuery {
		return contract.KVHTTPEntry{}, false
	}
	for _, i := range b.pageIndices() {
		row := r.Result.HTTP.Entries[i]
		if row.Key == b.selectedKey {
			return row.Clone(), true
		}
	}
	return contract.KVHTTPEntry{}, false
}
