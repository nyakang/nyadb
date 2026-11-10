package kvworkspace

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type authRequestRecord struct {
	Request       contract.KVRequest `json:"native_request"`
	OmittedFields []string           `json:"request_omitted_fields,omitempty"`
}

type authStepRecord struct {
	Result leaseResultRecord `json:"original_result"`
	Error  string            `json:"error,omitempty"`
}

type authManagementRecord struct {
	DocumentID string              `json:"original_document_id"`
	Scope      leaseScopeRecord    `json:"original_scope"`
	Requests   []authRequestRecord `json:"planned_requests"`
	Steps      []authStepRecord    `json:"original_step_receipts"`
	Semantics  string              `json:"execution_semantics"`
}

func authRecord(r *AuthManagementReceipt) *authManagementRecord {
	if r == nil {
		return nil
	}
	out := &authManagementRecord{DocumentID: strconv.FormatUint(r.DocumentID, 10), Scope: leaseScope(r.Scope), Semantics: "Separate native RPCs, not an atomic transaction. Each confirmed step retains its original receipt. A failed or unknown step stops later requests; recovery never replays any step. Native user/role names and ranges are not CAS or incarnation preconditions."}
	for _, request := range r.Requests {
		out.Requests = append(out.Requests, authRequestRecord{Request: request.Request, OmittedFields: request.OmittedFields})
	}
	for _, step := range r.Steps {
		out.Steps = append(out.Steps, authStepRecord{Result: leaseResult(step.Result), Error: step.Error})
	}
	return out
}

// A preview is an explicitly redacted record, not a runnable request envelope.
// It never changes the browser document's editor or saved-file baseline.
func (s *State) ExportAuthPreview(d *Document) {
	if !s.active(d) || d.io != nil || d.confirmation == nil || d.confirmation.auth == nil {
		return
	}
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	raw, err := json.MarshalIndent(authRecord(d.confirmation.auth.receipt), "", "  ")
	if err != nil {
		d.error = err.Error()
		return
	}
	s.fileJob(d, "export", func(ctx context.Context) fileResult {
		path, err := f.SavePath("auth-preview.nyakv-auth-preview.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
