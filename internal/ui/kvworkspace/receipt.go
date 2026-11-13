package kvworkspace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// Operation records preserve the original scope/request even when there was
// no native completion. Native int64/uint64 values are serialized as strings;
// the full protobuf and its optional native JSON remain separate payloads.
func (s *State) OperationRecord(d *Document) ([]byte, error) {
	if !s.owned(d) {
		return nil, jsondocument.ErrInvalid
	}
	r := s.selectedReceipt(d)
	if r == nil {
		return nil, jsondocument.ErrInvalid
	}
	return encodeOperationReceipt(*r)
}
func encodeOperationReceipt(r Receipt) ([]byte, error) {
	if r.Result.HTTPSessionDestroy != nil {
		return encodeHTTPSessionDestroyReceipt(r)
	}
	if r.Result.HTTP != nil {
		return encodeHTTPOperationReceipt(r)
	}
	type identity struct{ ProfileID, Generation, ClusterID, MemberID, Revision, RaftTerm, Version, Catalog, Schema, PrefixBase64 string }
	h := r.Result.Header
	record := struct {
		Version              int                    `json:"version"`
		Scope                identity               `json:"scope"`
		Method               string                 `json:"method"`
		AuthoredJSON         string                 `json:"authored_json"`
		CanonicalJSON        string                 `json:"canonical_json"`
		Attempted            bool                   `json:"attempted"`
		Completed            bool                   `json:"completed"`
		NativeCaptured       bool                   `json:"native_captured"`
		WriteOutcomeUnknown  bool                   `json:"write_outcome_unknown"`
		Code                 string                 `json:"code"`
		Header               identity               `json:"native_header"`
		Native               []byte                 `json:"native_protobuf"`
		JSON                 json.RawMessage        `json:"protobuf_json"`
		JSONCaptureError     string                 `json:"json_capture_error,omitempty"`
		ProjectionError      string                 `json:"projection_error,omitempty"`
		Error                string                 `json:"error,omitempty"`
		LeaseManagement      *leaseManagementRecord `json:"lease_management,omitempty"`
		AuthManagement       *authManagementRecord  `json:"auth_management,omitempty"`
		Compaction           *compactionRecord      `json:"compaction,omitempty"`
		RequestOmittedFields []string               `json:"request_omitted_fields,omitempty"`
	}{Version: 1, Scope: identity{ProfileID: strconv.FormatInt(r.Scope.Namespace.ProfileID, 10), Generation: strconv.FormatUint(r.Scope.Generation, 10), ClusterID: strconv.FormatUint(r.Scope.Header.ClusterID, 10), MemberID: strconv.FormatUint(r.Scope.Header.MemberID, 10), Revision: strconv.FormatInt(r.Scope.Header.Revision, 10), RaftTerm: strconv.FormatUint(r.Scope.Header.RaftTerm, 10), Version: r.Scope.Version, Catalog: r.Scope.Namespace.Catalog, Schema: r.Scope.Namespace.Schema, PrefixBase64: base64.StdEncoding.EncodeToString(r.Scope.Prefix)}, Method: r.Result.Request.Method, AuthoredJSON: r.Authored.JSON, CanonicalJSON: r.Result.Request.JSON, Attempted: r.Result.Attempted, Completed: r.Result.Completed, WriteOutcomeUnknown: r.Result.WriteOutcomeUnknown, Code: r.Result.Code, Header: identity{ClusterID: strconv.FormatUint(h.ClusterID, 10), MemberID: strconv.FormatUint(h.MemberID, 10), Revision: strconv.FormatInt(h.Revision, 10), RaftTerm: strconv.FormatUint(h.RaftTerm, 10)}, Native: r.Result.Native, JSONCaptureError: r.Result.JSONCaptureError, ProjectionError: r.Result.ProjectionError, Error: r.Error}
	if r.Result.JSON != "" {
		record.JSON = json.RawMessage(r.Result.JSON)
	}
	record.NativeCaptured = r.Result.NativeCaptured
	record.LeaseManagement = managementRecord(r.LeaseManagement)
	record.AuthManagement = authRecord(r.AuthManagement)
	compaction, err := compactionData(r.Compaction)
	if err != nil {
		return nil, err
	}
	record.Compaction = compaction
	record.RequestOmittedFields = r.Result.RequestOmittedFields
	raw, err := json.MarshalIndent(record, "", "  ")
	if len(raw) > 128<<20 {
		return nil, jsondocument.ErrBounds
	}
	return raw, err
}
func (s *State) ExportOperationRecord(d *Document) {
	if !s.active(d) || d.io != nil {
		return
	}
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	raw, err := s.OperationRecord(d)
	if err != nil {
		return
	}
	s.fileJob(d, "export", func(ctx context.Context) fileResult {
		path, err := f.SavePath("operation.nyakv-receipt.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
