package kvworkspace

import (
	"crypto/sha256"
	"fmt"
	"reflect"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var httpACLMutationLimits = contract.KVHTTPACLImpactLimits{
	MaxRows: contract.KVHTTPACLImpactMaxRows, MaxReads: contract.KVHTTPACLImpactMaxReads, MaxBytes: contract.KVHTTPACLImpactMaxBytes,
}

// Observation survives failed preview/revalidation independently of the last
// attempted write. It is private evidence, never reusable execution authority.
type httpACLMutationObservation struct {
	Request  contract.KVHTTPACLMutationRequest
	Scope    contract.KVScope
	RecordID string
	Evidence contract.KVHTTPACLMutationImpactEvidence
}

func (d *Document) httpACLMutationEvidence() bool {
	return d.httpACLMutation != nil && (d.httpACLMutation.last != nil || d.httpACLMutation.observation != nil || d.httpACLMutation.confirmation != nil)
}

func (d *Document) clearHTTPACLMutationConfirmation() {
	if d.httpACLMutation == nil {
		return
	}
	x := d.httpACLMutation
	if p := x.confirmation; p != nil {
		x.confirmation = nil
		if p.plan != nil {
			_ = p.plan.Close()
		}
	}
	if x.form != nil {
		x.form.checked = false
		x.form.preview = ""
	}
}

func validHTTPACLMutationImpact(o contract.KVHTTPACLImpact, scope contract.KVScope, input contract.KVHTTPACLMutationRequest) bool {
	want := contract.KVHTTPACLImpactRequest{Resource: input.Resource, Identity: input.Identity, MaxRows: httpACLMutationLimits.MaxRows, MaxReads: httpACLMutationLimits.MaxReads, MaxBytes: httpACLMutationLimits.MaxBytes}
	if !o.Complete || !sameEventScope(o.Scope, scope) || o.Request != want || o.Bytes <= 0 || o.Bytes > want.MaxBytes || o.Rows < 0 || o.Rows > want.MaxRows || len(o.Reads) == 0 || len(o.Reads) > want.MaxReads {
		return false
	}
	if input.Operation == "create" {
		if o.Creation == nil || !reflect.DeepEqual(*o.Creation, input) {
			return false
		}
	} else if o.Creation != nil {
		return false
	}
	rows := 0
	for _, read := range o.Reads {
		r := read.Result
		h := r.Receipt.HTTP
		if !sameEventScope(r.Scope, scope) || h == nil || !r.Receipt.Attempted || !r.Receipt.Completed || r.Receipt.WriteOutcomeUnknown || !h.Attempted || !h.Acknowledged || !h.Ended || !h.Captured || !h.EOF || !h.Succeeded || h.WriteOutcomeUnknown || h.Status != 200 || h.ProjectionError != "" || r.Receipt.ProjectionError != "" || h.Kind != "acl-read" || h.Request.Method != "GET" || h.CaptureError != "" || r.ProjectionError != "" || h.Metadata.ACLFiltered != nil && *h.Metadata.ACLFiltered || r.Projection == nil || r.Projection.Null || !r.Projection.Complete || !r.Projection.ProjectionComplete || r.Projection.ContainsRedaction || read.BodySHA256 != fmt.Sprintf("%x", sha256.Sum256(h.Body)) {
			return false
		}
		rows += len(r.Projection.Rows)
	}
	if rows != o.Rows {
		return false
	}
	for _, refs := range [][]contract.KVHTTPACLImpactReference{o.References, o.Dependencies} {
		for _, ref := range refs {
			if ref.Resource == "" || ref.Identity == "" || len(ref.Locations) == 0 {
				return false
			}
			for _, loc := range ref.Locations {
				if loc.Read < 0 || loc.Read >= len(o.Reads) || loc.Row < 0 || loc.Row >= len(o.Reads[loc.Read].Result.Projection.Rows) {
					return false
				}
				r := o.Reads[loc.Read].Result
				if r.Request.Resource != ref.Resource || r.Projection.Rows[loc.Row].Identity != ref.Identity {
					return false
				}
			}
		}
	}
	return true
}

func (s *State) retainHTTPACLMutationObservation(d *Document, job *operation, e contract.KVHTTPACLMutationImpactEvidence, record string) {
	x := d.httpACLMutation
	s.HideHTTPACLMutationReceipt(d)
	x.observation = &httpACLMutationObservation{Request: job.aclMutationRequest.Clone(), Scope: job.scope.Clone(), RecordID: record, Evidence: e.Clone()}
	x.revision++
	d.permit = nil
}

func (s *State) consumeHTTPACLMutationPreview(d *Document, job *operation, out outcome) {
	plan := out.aclMutationPlan
	defer func() {
		if plan != nil {
			_ = plan.Close()
		}
	}()
	x := d.httpACLMutation
	if x == nil {
		return
	}
	draftMatches := x.revision == job.aclMutationRevision && reflect.DeepEqual(x.input, job.aclMutationRequest)
	record := ""
	if plan != nil {
		record = plan.LocalRecordID()
	}
	if out.aclMutationImpact != nil {
		s.retainHTTPACLMutationObservation(d, job, contract.KVHTTPACLMutationImpactEvidence{Preview: *out.aclMutationImpact}, record)
	}
	if out.err != nil || job.canceled || plan == nil || out.aclMutationImpact == nil || record == "" || len(record) > 128 || !draftMatches || job.lease != d.lease || !sameEventScope(job.scope, d.scope) || d.needsRecovery || d.hasUnknownWrites() || d.Request() != job.authored || d.Editor.Buffer.Version() != job.editorRevision || !reflect.DeepEqual(plan.Request(), job.aclMutationRequest) || !reflect.DeepEqual(plan.Impact(), *out.aclMutationImpact) || !validHTTPACLMutationImpact(*out.aclMutationImpact, job.scope, job.aclMutationRequest) {
		x.error, d.error = httpACLMutationError, httpACLMutationError
		if x.form != nil {
			x.form.impactOpen = true
		}
		return
	}
	x.error = ""
	x.confirmation = &httpACLMutationConfirmation{lease: job.lease, scope: job.scope.Clone(), plan: plan, impact: out.aclMutationImpact.Clone(), request: job.aclMutationRequest.Clone(), recordID: record, revision: x.revision, editor: job.authored, editorRevision: job.editorRevision}
	if x.form != nil {
		x.form.checked = false
		x.form.impactOpen = false
	}
	plan = nil
}
