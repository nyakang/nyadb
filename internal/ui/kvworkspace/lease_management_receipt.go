package kvworkspace

import (
	"context"
	"encoding/json"
	"strconv"
)

type leaseManagementRecord struct {
	Document        string                      `json:"original_document_id"`
	Kind            string                      `json:"method"`
	Scope           leaseScopeRecord            `json:"original_scope"`
	RequestedID     string                      `json:"requested_id"`
	RequestedTTL    string                      `json:"requested_ttl"`
	Observation     *leaseManagementObservation `json:"earlier_revoke_observation,omitempty"`
	RevokeSemantics string                      `json:"revoke_semantics,omitempty"`
}
type leaseManagementObservation struct {
	Scope       leaseScopeRecord  `json:"received_scope"`
	ID          string            `json:"requested_id"`
	IncludeKeys bool              `json:"requested_complete_keys"`
	Result      leaseResultRecord `json:"original_result"`
}

func managementRecord(r *LeaseManagementReceipt) *leaseManagementRecord {
	if r == nil {
		return nil
	}
	out := &leaseManagementRecord{Document: strconv.FormatUint(r.DocumentID, 10), Kind: r.Kind, Scope: leaseScope(r.Scope), RequestedID: strconv.FormatInt(r.RequestedID, 10), RequestedTTL: strconv.FormatInt(r.RequestedTTL, 10)}
	if r.Observation != nil {
		o := r.Observation
		out.Observation = &leaseManagementObservation{Scope: leaseScope(o.Scope), ID: strconv.FormatInt(o.Request.ID, 10), IncludeKeys: o.Request.IncludeKeys, Result: leaseResult(o.Result)}
		out.RevokeSemantics = "Native LeaseRevoke by ID; earlier keys may change and the ID may be reused before execution. No CAS or incarnation precondition. An absent later TTL does not settle an unknown original write."
	}
	return out
}

func managementObservationJSON(r *LeaseManagementReceipt) string {
	raw, _ := json.MarshalIndent(managementRecord(r), "", "  ")
	return string(raw)
}

// Saving a management preview uses the existing version-one native request
// format without changing the browser editor's path or saved-text baseline.
func (s *State) ExportLeaseManagementRequest(d *Document) {
	if !s.active(d) || d.io != nil || d.confirmation == nil || d.confirmation.management == nil {
		return
	}
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	raw, err := EncodeRequest(d.confirmation.authored)
	if err != nil {
		d.error = err.Error()
		return
	}
	s.fileJob(d, "export", func(ctx context.Context) fileResult {
		path, err := f.SavePath("lease-request.nyakv")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
