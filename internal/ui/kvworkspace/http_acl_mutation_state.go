package kvworkspace

import (
	"context"
	"reflect"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const httpACLMutationError = "ACL write failed; original private receipt retained"

type httpACLMutationConfirmation struct {
	lease          contract.KVRequestLease
	scope          contract.KVScope
	plan           contract.KVHTTPACLMutationPlan
	impact         contract.KVHTTPACLImpact
	request        contract.KVHTTPACLMutationRequest
	recordID       string
	revision       uint64
	editor         contract.KVRequest
	editorRevision uint64
}

// Authored definitions and generated secrets remain private. Unknown writes
// retain their original receipt even after another ACL read or pure recovery.
type httpACLMutationState struct {
	form           *httpACLMutationForm
	transfer       *httpACLMutationTransfer
	input, saved   contract.KVHTTPACLMutationRequest
	confirmation   *httpACLMutationConfirmation
	last           *contract.KVHTTPACLMutationResult
	observation    *httpACLMutationObservation
	revision       uint64
	valid, unknown bool
	revealed       string
	error          string
}

func (d *Document) HTTPACLMutationAvailable() bool {
	_, ok := d.lease.(contract.KVHTTPACLMutating)
	_, planning := d.lease.(contract.KVHTTPACLMutationPlanning)
	return !d.closed && d.method == contract.KVConsulHTTPRequestMethod && d.scope.HTTP.Protocol == "consul-http" && ok && planning
}
func (d *Document) httpACLMutationContent() bool {
	return d.httpACLMutation != nil && d.httpACLMutation.last != nil
}
func (d *Document) httpACLMutationDirty() bool {
	return d.httpACLMutation != nil && (!reflect.DeepEqual(d.httpACLMutation.input, d.httpACLMutation.saved) || d.httpACLMutation.form != nil && d.httpACLMutation.form.input != d.httpACLMutation.form.saved)
}
func (d *Document) httpACLMutationRevision() uint64 {
	if d.httpACLMutation == nil {
		return 0
	}
	return d.httpACLMutation.revision
}

func validHTTPACLMutationInput(input contract.KVHTTPACLMutationRequest) bool {
	size := len(input.Definition)
	for _, value := range []string{input.Resource, input.Operation, input.Identity} {
		size += len(value)
		if !utf8.ValidString(value) {
			return false
		}
	}
	if input.Description != nil {
		size += len(*input.Description)
		if !utf8.ValidString(*input.Description) {
			return false
		}
	}
	return size <= inputLimit
}

func (s *State) SetHTTPACLMutationInput(d *Document, input contract.KVHTTPACLMutationRequest) {
	if !s.active(d) || d.Busy() || d.FileBusy() || !d.HTTPACLMutationAvailable() {
		return
	}
	if !validHTTPACLMutationInput(input) {
		d.error = httpACLMutationError
		return
	}
	if d.httpACLMutation == nil {
		d.httpACLMutation = &httpACLMutationState{}
	}
	x := d.httpACLMutation
	if reflect.DeepEqual(x.input, input) {
		return
	}
	x.input = input.Clone()
	x.transfer = nil
	d.clearHTTPACLMutationConfirmation()
	x.revision++
	d.permit = nil
}

// Preparation reads the original bounded impact asynchronously. Only an active
// original document can admit the completed plan for explicit confirmation.
func (s *State) PrepareHTTPACLMutation(d *Document) {
	if !s.check(d) || d.FileBusy() || !d.HTTPACLMutationAvailable() || d.hasUnknownWrites() || d.httpACLMutation == nil {
		return
	}
	d.clearConfirmation()
	x := d.httpACLMutation
	input := x.input.Clone()
	facet := d.lease.(contract.KVHTTPACLMutationPlanning)
	editor, revision, draft := d.Request(), d.Editor.Buffer.Version(), x.revision
	s.HideHTTPACLReceipt(d)
	if x.form != nil {
		x.form.checked = false
		x.form.receipt = false
	}
	s.start(d, "http-acl-mutation-preview", editor, func(ctx context.Context) outcome {
		plan, impact, err := facet.PrepareHTTPACLMutationPlan(ctx, input.Clone(), httpACLMutationLimits)
		impact = impact.Clone()
		return outcome{aclMutationPlan: plan, aclMutationImpact: &impact, err: err}
	})
	d.job.aclMutationRequest, d.job.aclMutationRevision, d.job.editorRevision = input, draft, revision
}

func validHTTPACLMutationConfirmation(d *Document, p *httpACLMutationConfirmation) bool {
	return p != nil && p.plan != nil && d.httpACLMutation != nil && p.lease == d.lease && sameEventScope(p.scope, d.scope) && !d.needsRecovery && !d.hasUnknownWrites() && p.revision == d.httpACLMutation.revision && reflect.DeepEqual(p.request, d.httpACLMutation.input) && p.editor == d.Request() && p.editorRevision == d.Editor.Buffer.Version() && p.recordID == p.plan.LocalRecordID() && reflect.DeepEqual(p.plan.Request(), p.request) && reflect.DeepEqual(p.plan.Impact(), p.impact)
}

func (s *State) ConfirmHTTPACLMutation(d *Document, checked bool) {
	if !s.check(d) || d.FileBusy() || !checked || d.httpACLMutation == nil {
		return
	}
	x := d.httpACLMutation
	p := x.confirmation
	x.confirmation = nil
	if !validHTTPACLMutationConfirmation(d, p) {
		if p != nil && p.plan != nil {
			_ = p.plan.Close()
		}
		return
	}
	facet := p.lease.(contract.KVHTTPACLMutating)
	s.HideHTTPACLReceipt(d)
	s.start(d, "http-acl-mutation", contract.KVRequest{}, func(ctx context.Context) outcome {
		defer p.plan.Close()
		r, err := facet.ExecuteHTTPACLMutation(ctx, p.plan)
		r = r.Clone()
		return outcome{aclMutation: &r, err: err}
	})
	d.job.aclMutationRequest = p.request.Clone()
	d.job.aclMutationRecordID = p.recordID
	d.job.aclMutationPreview = &p.impact
}

func (s *State) HideHTTPACLMutationReceipt(d *Document) {
	if s.owned(d) && d.httpACLMutation != nil {
		d.httpACLMutation.revealed = ""
		d.httpACLMutation.transfer = nil
		if d.httpACLMutation.form != nil {
			d.httpACLMutation.form.preview = ""
			d.httpACLMutation.form.impactPreview = ""
		}
	}
}
func (s *State) RevealHTTPACLMutationReceipt(d *Document, record string) bool {
	if !s.active(d) || !d.httpACLMutationContent() || record == "" || record != d.httpACLMutation.last.LocalRecordID {
		return false
	}
	d.httpACLMutation.revealed = record
	return true
}
func (s *State) RevealedHTTPACLMutationReceipt(d *Document) (contract.KVHTTPACLMutationResult, bool) {
	if !s.active(d) || !d.httpACLMutationContent() || d.httpACLMutation.revealed == "" || d.httpACLMutation.revealed != d.httpACLMutation.last.LocalRecordID {
		return contract.KVHTTPACLMutationResult{}, false
	}
	return d.httpACLMutation.last.Clone(), true
}

func (s *State) consumeHTTPACLMutation(d *Document, job *operation, out outcome) {
	if d.httpACLMutation == nil {
		d.httpACLMutation = &httpACLMutationState{}
	}
	x := d.httpACLMutation
	s.HideHTTPACLMutationReceipt(d)
	x.error = ""
	if out.err != nil {
		x.error, d.error = httpACLMutationError, httpACLMutationError
	}
	if out.aclMutation != nil && out.aclMutation.Impact != nil {
		s.retainHTTPACLMutationObservation(d, job, *out.aclMutation.Impact, out.aclMutation.LocalRecordID)
	}
	if out.aclMutation == nil || !out.aclMutation.Receipt.Attempted {
		if x.form != nil && x.observation != nil {
			x.form.impactOpen = true
		}
		return
	}
	r := out.aclMutation.Clone()
	x.valid = sameEventScope(r.Scope, job.scope) && r.LocalRecordID == job.aclMutationRecordID && reflect.DeepEqual(r.Request, job.aclMutationRequest) && r.Impact != nil && job.aclMutationPreview != nil && reflect.DeepEqual(r.Impact.Preview, *job.aclMutationPreview) && r.Impact.Revalidated
	if !x.valid {
		r.Projection = nil
		r.ProjectionError = "ACL mutation projection unavailable; original receipt retained"
		x.error, d.error = contract.ErrRevoked.Error(), contract.ErrRevoked.Error()
	}
	x.unknown = x.unknown || !x.valid || r.Receipt.WriteOutcomeUnknown || r.Receipt.HTTP != nil && r.Receipt.HTTP.WriteOutcomeUnknown
	x.last = &r
	x.revision++
	x.confirmation = nil
	d.permit = nil
}

func (d *Document) expectedHTTPACLMutationRecovery() *contract.KVHTTPACLMutationResult {
	if !d.httpACLMutationContent() {
		return nil
	}
	r := d.httpACLMutation.last.Clone()
	return &r
}
func sameHTTPACLMutationRecovery(expected, actual *contract.KVHTTPACLMutationResult, scope contract.KVScope) bool {
	if expected != nil {
		return actual != nil && reflect.DeepEqual(expected, actual)
	}
	return actual == nil || actual.LocalRecordID != "" && actual.Receipt.Attempted && sameKVRecoveryIdentity(actual.Scope, scope) && actual.Scope.Generation != 0 && actual.Scope.Generation <= scope.Generation
}
func (s *State) consumeHTTPACLMutationRecovery(d *Document, job *operation, out *outcome) {
	s.HideHTTPACLMutationReceipt(d)
	if out.err != nil {
		return
	}
	if !sameHTTPACLMutationRecovery(job.aclMutationRecovery, out.recoveryACLMutation, job.scope) {
		if out.lease != nil {
			out.lease.Close()
			out.lease = nil
		}
		out.err, d.error = contract.ErrRevoked, contract.ErrRevoked.Error()
		return
	}
	if out.recoveryACLMutation != nil && !d.httpACLMutationContent() {
		r := out.recoveryACLMutation.Clone()
		if d.httpACLMutation == nil {
			d.httpACLMutation = &httpACLMutationState{}
		}
		x := d.httpACLMutation
		x.last = &r
		x.valid = true
		x.unknown = x.unknown || r.Receipt.WriteOutcomeUnknown || r.Receipt.HTTP != nil && r.Receipt.HTTP.WriteOutcomeUnknown
		x.revision++
		d.permit = nil
	}
}
