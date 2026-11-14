package kvworkspace

import (
	"slices"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type HTTPACLMutationInput struct {
	Resource, Operation, Identity, Definition, Description string
	IncludeDescription                                     bool
}
type httpACLMutationForm struct {
	input, saved  HTTPACLMutationInput
	path          string
	serial        uint64
	open, receipt bool
	preview       string
	impactPreview string
	impactOpen    bool
	impactPage    int
	checked       bool
}

func validHTTPACLMutationForm(input HTTPACLMutationInput) bool {
	size := 0
	for _, v := range []string{input.Resource, input.Operation, input.Identity, input.Definition, input.Description} {
		size += len(v)
		if !utf8.ValidString(v) {
			return false
		}
	}
	return size <= inputLimit
}
func (input HTTPACLMutationInput) request() (contract.KVHTTPACLMutationRequest, error) {
	r := contract.KVHTTPACLMutationRequest{Resource: input.Resource, Operation: input.Operation}
	if !validHTTPACLMutationForm(input) || !slices.Contains([]string{"token", "policy", "role", "auth-method", "binding-rule"}, input.Resource) {
		return r, contract.ErrUnsupported
	}
	switch input.Operation {
	case "create":
		r.Definition = []byte(input.Definition)
	case "update":
		r.Identity = input.Identity
		r.Definition = []byte(input.Definition)
	case "delete":
		r.Identity = input.Identity
	case "clone":
		if input.Resource != "token" {
			return r, contract.ErrUnsupported
		}
		r.Identity = input.Identity
		if input.IncludeDescription {
			r.Description = new(input.Description)
		}
	default:
		return r, contract.ErrUnsupported
	}
	if input.Operation != "create" && r.Identity == "" {
		return r, contract.ErrUnsupported
	}
	return r, nil
}
func mutationFormInput(r contract.KVHTTPACLMutationRequest) HTTPACLMutationInput {
	i := HTTPACLMutationInput{Resource: r.Resource, Operation: r.Operation, Identity: r.Identity, Definition: string(r.Definition)}
	if r.Description != nil {
		i.IncludeDescription = true
		i.Description = *r.Description
	}
	return i
}
func (s *State) OpenHTTPACLMutationForm(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || !d.HTTPACLMutationAvailable() {
		return
	}
	if d.httpACLMutation == nil {
		input := contract.KVHTTPACLMutationRequest{Resource: "token", Operation: "create", Definition: []byte(`{}`)}
		d.httpACLMutation = &httpACLMutationState{input: input, saved: input.Clone()}
	}
	x := d.httpACLMutation
	if x.form == nil {
		i := mutationFormInput(x.input)
		x.form = &httpACLMutationForm{input: i, saved: mutationFormInput(x.saved)}
	}
	s.HideHTTPACLReceipt(d)
	d.clearConfirmation()
	x.form.open = true
	x.form.checked = false
	x.form.receipt = false
	x.form.impactOpen = false
	if d.httpACL != nil {
		if d.httpACL.form != nil {
			d.httpACL.form.open = false
		}
		if d.httpACL.browser != nil {
			d.httpACL.browser.open = false
		}
	}
}
func (s *State) SetHTTPACLMutationForm(d *Document, input HTTPACLMutationInput) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpACLMutation == nil || d.httpACLMutation.form == nil {
		return
	}
	if !validHTTPACLMutationForm(input) {
		d.error = httpACLMutationError
		return
	}
	x := d.httpACLMutation
	f := x.form
	if f.input == input {
		return
	}
	f.input = input
	f.serial++
	f.checked = false
	d.clearHTTPACLMutationConfirmation()
	x.transfer = nil
	x.revision++
	d.permit = nil
	if r, err := input.request(); err == nil {
		x.input = r.Clone()
	}
}
func (s *State) PrepareHTTPACLMutationForm(d *Document) {
	if !s.active(d) || d.httpACLMutation == nil || d.httpACLMutation.form == nil {
		return
	}
	r, err := d.httpACLMutation.form.input.request()
	if err != nil {
		d.error = httpACLMutationError
		return
	}
	s.SetHTTPACLMutationInput(d, r)
	s.PrepareHTTPACLMutation(d)
}
func (s *State) openHTTPACLMutationReceipt(d *Document) {
	if !s.active(d) || !d.httpACLMutationContent() {
		return
	}
	x := d.httpACLMutation
	if x.form == nil {
		x.form = &httpACLMutationForm{input: mutationFormInput(x.input), saved: mutationFormInput(x.saved)}
	}
	s.HideHTTPACLReceipt(d)
	x.form.open = true
	x.form.receipt = true
}
