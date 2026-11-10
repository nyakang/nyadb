package kvworkspace

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// At most a cached page, a cached detail and the latest original attempt are
// retained. They share pointers when identical; there is no browser history.
type LeaseBrowserReceipt struct {
	Scope    contract.KVScope
	Authored contract.KVRequest
	Page     *contract.KVLeasePage
	Detail   *contract.KVLeaseDetail
	Error    string
	preview  string
}

func (r LeaseBrowserReceipt) Clone() LeaseBrowserReceipt {
	r.Scope = r.Scope.Clone()
	if r.Page != nil {
		p := r.Page.Clone()
		r.Page = &p
	}
	if r.Detail != nil {
		d := r.Detail.Clone()
		r.Detail = &d
	}
	return r
}
func (d *Document) LastLeaseBrowserReceipt() (LeaseBrowserReceipt, bool) {
	if d.browserLast == nil {
		return LeaseBrowserReceipt{}, false
	}
	return d.browserLast.Clone(), true
}
func (d *Document) LeasePageReceipt() (LeaseBrowserReceipt, bool) {
	if d.browserPage == nil {
		return LeaseBrowserReceipt{}, false
	}
	return d.browserPage.Clone(), true
}
func (d *Document) LeaseDetailReceipt() (LeaseBrowserReceipt, bool) {
	if d.browserDetail == nil {
		return LeaseBrowserReceipt{}, false
	}
	return d.browserDetail.Clone(), true
}

type leaseProjectionRecord struct {
	ID         string   `json:"id"`
	TTL        string   `json:"observed_ttl"`
	GrantedTTL string   `json:"granted_ttl"`
	Keys       [][]byte `json:"complete_attached_keys"`
	IDs        []string `json:"complete_list_ids"`
}
type leaseResultRecord struct {
	Request              contract.KVRequest     `json:"native_request"`
	Attempted            bool                   `json:"attempted"`
	Completed            bool                   `json:"completed"`
	Unknown              bool                   `json:"write_outcome_unknown"`
	Code                 string                 `json:"code"`
	Header               eventHeaderRecord      `json:"native_header"`
	Native               []byte                 `json:"native_protobuf"`
	JSON                 string                 `json:"protobuf_json"`
	JSONCaptureError     string                 `json:"json_capture_error,omitempty"`
	ProjectionError      string                 `json:"projection_error,omitempty"`
	Lease                *leaseProjectionRecord `json:"lease_projection,omitempty"`
	RequestOmittedFields []string               `json:"request_omitted_fields,omitempty"`
}

func leaseResult(r contract.KVResult) leaseResultRecord {
	out := leaseResultRecord{Request: r.Request, Attempted: r.Attempted, Completed: r.Completed, Unknown: r.WriteOutcomeUnknown, Code: r.Code, Header: eventHeader(r.Header), Native: r.Native, JSON: r.JSON, JSONCaptureError: r.JSONCaptureError, ProjectionError: r.ProjectionError}
	out.RequestOmittedFields = r.RequestOmittedFields
	if r.Reply != nil && r.Reply.Lease != nil {
		l := r.Reply.Lease
		ids := make([]string, len(l.IDs))
		for i, id := range l.IDs {
			ids[i] = strconv.FormatInt(id, 10)
		}
		out.Lease = &leaseProjectionRecord{ID: strconv.FormatInt(l.ID, 10), TTL: strconv.FormatInt(l.TTL, 10), GrantedTTL: strconv.FormatInt(l.GrantedTTL, 10), Keys: l.Keys, IDs: ids}
	}
	return out
}

type leaseObservationRecord struct {
	ID     string            `json:"id"`
	Result leaseResultRecord `json:"result"`
}
type leasePageRecord struct {
	Scope     leaseScopeRecord         `json:"received_scope"`
	Limit     int                      `json:"limit"`
	After     *string                  `json:"exclusive_client_after"`
	List      leaseResultRecord        `json:"original_cluster_list"`
	Rows      []leaseObservationRecord `json:"observations"`
	Complete  bool                     `json:"complete"`
	More      bool                     `json:"more"`
	NextAfter *string                  `json:"next_client_after"`
}

type leaseScopeRecord struct {
	Profile    string            `json:"profile_id"`
	Catalog    string            `json:"catalog"`
	Schema     string            `json:"schema"`
	Generation string            `json:"generation"`
	Prefix     []byte            `json:"connection_prefix"`
	Header     eventHeaderRecord `json:"native_header"`
	Version    string            `json:"product_version"`
}

func leaseScope(s contract.KVScope) leaseScopeRecord {
	return leaseScopeRecord{strconv.FormatInt(s.Namespace.ProfileID, 10), s.Namespace.Catalog, s.Namespace.Schema, strconv.FormatUint(s.Generation, 10), s.Prefix, eventHeader(s.Header), s.Version}
}

func leaseBoundary(id *int64) *string {
	if id == nil {
		return nil
	}
	text := strconv.FormatInt(*id, 10)
	return &text
}
func leasePage(p contract.KVLeasePage) leasePageRecord {
	rows := make([]leaseObservationRecord, len(p.Rows))
	for i, row := range p.Rows {
		rows[i] = leaseObservationRecord{strconv.FormatInt(row.ID, 10), leaseResult(row.Result)}
	}
	return leasePageRecord{leaseScope(p.Scope), p.Request.Limit, leaseBoundary(p.Request.After), leaseResult(p.List), rows, p.Complete, p.More, leaseBoundary(p.NextAfter)}
}
func EncodeLeaseBrowserRecord(r LeaseBrowserReceipt) ([]byte, error) {
	var page *leasePageRecord
	var detail any
	if r.Page != nil {
		p := leasePage(*r.Page)
		page = &p
	}
	if r.Detail != nil {
		detail = struct {
			Scope       leaseScopeRecord  `json:"received_scope"`
			ID          string            `json:"requested_id"`
			IncludeKeys bool              `json:"include_keys"`
			Result      leaseResultRecord `json:"result"`
		}{leaseScope(r.Detail.Scope), strconv.FormatInt(r.Detail.Request.ID, 10), r.Detail.Request.IncludeKeys, leaseResult(r.Detail.Result)}
	}
	raw, err := json.MarshalIndent(struct {
		Version        int                `json:"version"`
		Kind           string             `json:"kind"`
		Profile        string             `json:"profile_id"`
		Catalog        string             `json:"catalog"`
		Schema         string             `json:"schema"`
		Generation     string             `json:"original_generation"`
		Prefix         []byte             `json:"connection_prefix"`
		Header         eventHeaderRecord  `json:"original_native_header"`
		ProductVersion string             `json:"original_product_version"`
		Authored       contract.KVRequest `json:"authored_request"`
		Page           *leasePageRecord   `json:"page,omitempty"`
		Detail         any                `json:"detail,omitempty"`
		Error          string             `json:"error,omitempty"`
	}{1, "etcd-cluster-lease-observation", strconv.FormatInt(r.Scope.Namespace.ProfileID, 10), r.Scope.Namespace.Catalog, r.Scope.Namespace.Schema, strconv.FormatUint(r.Scope.Generation, 10), r.Scope.Prefix, eventHeader(r.Scope.Header), r.Scope.Version, r.Authored, page, detail, r.Error}, "", "  ")
	if len(raw) > 128<<20 {
		return nil, jsondocument.ErrBounds
	}
	return raw, err
}
func leaseBrowserPreview(r LeaseBrowserReceipt) string {
	// The preview omits bytes and full native JSON. Complete copy/export uses
	// the unchanged original receipt, including every list and TTL response.
	var rows []map[string]string
	if r.Page != nil {
		for _, row := range r.Page.Rows[:min(len(r.Page.Rows), 200)] {
			v := map[string]string{"id": strconv.FormatInt(row.ID, 10), "code": row.Result.Code}
			if row.Result.Reply != nil && row.Result.Reply.Lease != nil {
				v["observed_ttl"] = strconv.FormatInt(row.Result.Reply.Lease.TTL, 10)
				v["granted_ttl"] = strconv.FormatInt(row.Result.Reply.Lease.GrantedTTL, 10)
			}
			rows = append(rows, v)
		}
	}
	if r.Detail != nil {
		v := map[string]string{"id": strconv.FormatInt(r.Detail.Request.ID, 10), "code": r.Detail.Result.Code}
		if l := r.Detail.Result.Reply; l != nil && l.Lease != nil {
			v["observed_ttl"] = strconv.FormatInt(l.Lease.TTL, 10)
			v["granted_ttl"] = strconv.FormatInt(l.Lease.GrantedTTL, 10)
			v["attached_key_count"] = strconv.Itoa(len(l.Lease.Keys))
		}
		rows = append(rows, v)
	}
	raw, _ := json.MarshalIndent(struct {
		Rows  []map[string]string `json:"observations"`
		Error string              `json:"error,omitempty"`
	}{rows, clipped(r.Error)}, "", "  ")
	return clipped(string(raw))
}
func (s *State) selectedLeaseBrowserReceipt(d *Document) *LeaseBrowserReceipt {
	if d.browserReceiptChoice == 1 {
		return d.browserPage
	}
	if d.browserReceiptChoice == 2 {
		return d.browserDetail
	}
	return d.browserLast
}
func (s *State) LeaseBrowserRecord(d *Document) ([]byte, error) {
	if !s.owned(d) {
		return nil, jsondocument.ErrInvalid
	}
	r := s.selectedLeaseBrowserReceipt(d)
	if r == nil {
		return nil, jsondocument.ErrInvalid
	}
	return EncodeLeaseBrowserRecord(r.Clone())
}
func (s *State) CopyLeaseBrowserRecord(services ui.Services, d *Document) {
	if !s.active(d) || d.io != nil {
		return
	}
	r := s.selectedLeaseBrowserReceipt(d)
	if r == nil {
		return
	}
	captured := r.Clone()
	s.fileJob(d, "copy-leases", func(ctx context.Context) fileResult {
		if ctx.Err() != nil {
			return fileResult{err: ctx.Err()}
		}
		raw, err := EncodeLeaseBrowserRecord(captured)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return fileResult{clipboard: string(raw), err: err}
	})
	d.io.clipboard = services.WriteClipboard
}
func (s *State) ExportLeaseBrowserRecord(d *Document) {
	if !s.active(d) || d.io != nil {
		return
	}
	r := s.selectedLeaseBrowserReceipt(d)
	if r == nil {
		return
	}
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	captured := r.Clone()
	s.fileJob(d, "export-leases", func(ctx context.Context) fileResult {
		if ctx.Err() != nil {
			return fileResult{err: ctx.Err()}
		}
		raw, err := EncodeLeaseBrowserRecord(captured)
		if err != nil {
			return fileResult{err: err}
		}
		path, err := f.SavePath("lease-observation.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
