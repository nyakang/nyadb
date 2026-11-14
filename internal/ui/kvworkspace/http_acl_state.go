package kvworkspace

import (
	"context"
	"reflect"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const httpACLReadError = "ACL request failed; original private receipt retained"
const httpACLRecoveryError = "Original ACL recovery failed; private receipt retained"

// This private state is not a request-editor method or a public chooser tool.
// Native secrets never enter Receipt, generic clipboard/export or status text.
type httpACLState struct {
	last     *contract.KVHTTPACLReadResult
	error    string
	valid    bool
	revealed string
	revision uint64
	browser  *httpACLBrowser
	transfer *httpACLTransfer
	form     *httpACLReadForm
	showRead bool
}

func (d *Document) httpACLRevision() uint64 {
	if d.httpACL == nil {
		return 0
	}
	return d.httpACL.revision
}

func (d *Document) httpACLContent() bool { return d.httpACL != nil && d.httpACL.last != nil }

func (d *Document) HTTPACLAvailable() bool {
	_, ok := d.lease.(contract.KVHTTPACLReading)
	return !d.closed && d.method == contract.KVConsulHTTPRequestMethod && d.scope.HTTP.Protocol == "consul-http" && ok
}

// Reveal is bound to the exact private receipt the user selected. New reads,
// parking, disconnect and recovery hide the old reveal; none executes a read.
func (s *State) RevealHTTPACLReceipt(d *Document, recordID string) bool {
	if !s.active(d) || !d.httpACLContent() || recordID == "" || recordID != d.httpACL.last.LocalRecordID {
		return false
	}
	d.httpACL.revealed = recordID
	return true
}

func (s *State) HideHTTPACLReceipt(d *Document) {
	s.HideHTTPACLMutationReceipt(d)
	if s.owned(d) && d.httpACL != nil {
		d.httpACL.revealed = ""
		d.httpACL.transfer = nil
		if d.httpACL.browser != nil {
			d.httpACL.browser.preview = ""
			d.httpACL.browser.detail = false
		}
	}
}

func (s *State) RevealedHTTPACLReceipt(d *Document) (contract.KVHTTPACLReadResult, bool) {
	if !s.active(d) || !d.httpACLContent() || d.httpACL.revealed == "" || d.httpACL.revealed != d.httpACL.last.LocalRecordID {
		return contract.KVHTTPACLReadResult{}, false
	}
	return d.httpACL.last.Clone(), true
}

func (s *State) ReadHTTPACL(d *Document, request contract.KVHTTPACLReadRequest) {
	if !s.check(d) || d.FileBusy() || !d.HTTPACLAvailable() {
		return
	}
	input := request.Clone()
	lease := d.lease
	facet := lease.(contract.KVHTTPACLReading)
	plan, err := facet.PrepareHTTPACLRead(input.Clone())
	if err != nil || plan == nil {
		d.error = httpACLReadError
		return
	}
	recordID := plan.LocalRecordID()
	if recordID == "" || len(recordID) > 128 || !reflect.DeepEqual(plan.Request(), input) {
		d.error = contract.ErrRevoked.Error()
		return
	}
	s.HideHTTPACLReceipt(d)
	s.start(d, "http-acl-read", contract.KVRequest{}, func(ctx context.Context) outcome {
		r, err := facet.ExecuteHTTPACLRead(ctx, plan)
		return outcome{aclRead: &r, err: err}
	})
	d.job.aclRequest, d.job.aclRecordID = input, recordID
}

func (s *State) consumeHTTPACLRead(d *Document, job *operation, out outcome) {
	if d.httpACL == nil {
		d.httpACL = &httpACLState{}
	}
	x := d.httpACL
	showRead := x.showRead
	x.showRead = false
	s.HideHTTPACLReceipt(d)
	x.error = ""
	if out.err != nil {
		x.error, d.error = httpACLReadError, httpACLReadError
	}
	if out.aclRead == nil || !out.aclRead.Receipt.Attempted {
		if showRead && x.form != nil {
			x.form.open = true
		}
		return
	}
	r := out.aclRead.Clone()
	x.valid = sameEventScope(r.Scope, job.scope) && r.LocalRecordID == job.aclRecordID && reflect.DeepEqual(r.Request, job.aclRequest)
	if !x.valid {
		r.Projection = nil
		r.ProjectionError = "ACL projection unavailable; original receipt retained"
		x.error, d.error = contract.ErrRevoked.Error(), contract.ErrRevoked.Error()
	}
	x.last = &r
	x.browser = nil
	if showRead {
		x.browser = &httpACLBrowser{open: true, selected: -1}
	}
	x.revision++
	d.permit = nil
}

func (d *Document) expectedHTTPACLRecovery() *contract.KVHTTPACLReadResult {
	if !d.httpACLContent() {
		return nil
	}
	r := d.httpACL.last.Clone()
	return &r
}

func sameHTTPACLRecovery(expected, actual *contract.KVHTTPACLReadResult, scope contract.KVScope) bool {
	if expected != nil {
		return actual != nil && reflect.DeepEqual(expected, actual)
	}
	return actual == nil || actual.LocalRecordID != "" && actual.Receipt.Attempted && sameKVRecoveryIdentity(actual.Scope, scope) && actual.Scope.Generation != 0 && actual.Scope.Generation <= scope.Generation
}

func (s *State) consumeHTTPACLRecovery(d *Document, job *operation, out *outcome) {
	s.HideHTTPACLReceipt(d)
	if out.err != nil {
		return
	}
	if !sameHTTPACLRecovery(job.aclRecovery, out.recoveryACL, job.scope) {
		if out.lease != nil {
			out.lease.Close()
			out.lease = nil
		}
		out.err, d.error = contract.ErrRevoked, contract.ErrRevoked.Error()
		return
	}
	if out.recoveryACL != nil && !d.httpACLContent() {
		r := out.recoveryACL.Clone()
		d.httpACL = &httpACLState{last: &r, valid: true, revision: 1}
		d.permit = nil
	}
}
