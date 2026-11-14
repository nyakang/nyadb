package kvworkspace

import (
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

// Comparable raw fields preserve incomplete edits and optional native values.
// No credential, connection routing or execution authority is stored here.
type HTTPACLReadInput struct {
	Resource, Lookup, Identity                         string
	Policy, Role, AuthMethod, ServiceName, Index, Wait string
	IncludePolicy, IncludeRole, IncludeAuthMethod      bool
	IncludeServiceName, IncludeIndex, IncludeWait      bool
	Expanded                                           bool
}

type httpACLReadForm struct {
	input, saved HTTPACLReadInput
	path         string
	open         bool
	serial       uint64
}

func defaultHTTPACLReadInput() HTTPACLReadInput {
	return HTTPACLReadInput{Resource: "token", Lookup: "self"}
}

func validHTTPACLReadInput(input HTTPACLReadInput) bool {
	size := 0
	for _, value := range []string{input.Resource, input.Lookup, input.Identity, input.Policy, input.Role, input.AuthMethod, input.ServiceName, input.Index, input.Wait} {
		size += len(value)
		if !utf8.ValidString(value) {
			return false
		}
	}
	return size <= inputLimit
}

func (input HTTPACLReadInput) request() (contract.KVHTTPACLReadRequest, error) {
	r := contract.KVHTTPACLReadRequest{Resource: input.Resource, Lookup: input.Lookup, Identity: input.Identity, Expanded: input.Expanded}
	if !validHTTPACLReadInput(input) || !slices.Contains([]string{"token", "policy", "role", "auth-method", "binding-rule", "templated-policy"}, input.Resource) {
		return r, contract.ErrUnsupported
	}
	switch input.Lookup {
	case "list":
		if input.Identity != "" {
			return r, contract.ErrUnsupported
		}
	case "self":
		if input.Resource != "token" || input.Identity != "" {
			return r, contract.ErrUnsupported
		}
	case "id":
		if input.Identity == "" || input.Resource == "auth-method" || input.Resource == "templated-policy" {
			return r, contract.ErrUnsupported
		}
	case "name":
		if input.Identity == "" || input.Resource == "token" || input.Resource == "binding-rule" {
			return r, contract.ErrUnsupported
		}
	default:
		return r, contract.ErrUnsupported
	}
	if input.Expanded && (input.Resource != "token" || input.Lookup != "id") {
		return r, contract.ErrUnsupported
	}
	if (input.IncludeIndex || input.IncludeWait) && input.Resource == "templated-policy" {
		return r, contract.ErrUnsupported
	}
	if input.IncludeIndex {
		if input.Index == "" {
			return r, contract.ErrUnsupported
		}
		for _, c := range input.Index {
			if c < '0' || c > '9' {
				return r, contract.ErrUnsupported
			}
		}
		n, err := strconv.ParseUint(input.Index, 10, 64)
		if err != nil {
			return r, contract.ErrUnsupported
		}
		r.Index = &n
	}
	if input.IncludeWait {
		if !input.IncludeIndex {
			return r, contract.ErrUnsupported
		}
		r.Wait = new(input.Wait)
	}
	for _, option := range []struct {
		included, allowed bool
		value             string
		target            **string
	}{
		{input.IncludePolicy, input.Lookup == "list" && (input.Resource == "token" || input.Resource == "role"), input.Policy, &r.Policy},
		{input.IncludeRole, input.Lookup == "list" && input.Resource == "token", input.Role, &r.Role},
		{input.IncludeAuthMethod, input.Lookup == "list" && (input.Resource == "token" || input.Resource == "binding-rule"), input.AuthMethod, &r.AuthMethod},
		{input.IncludeServiceName, input.Lookup == "list" && input.Resource == "token", input.ServiceName, &r.ServiceName},
	} {
		if option.included {
			if !option.allowed {
				return r, contract.ErrUnsupported
			}
			*option.target = new(option.value)
		}
	}
	return r, nil
}

func (d *Document) httpACLFormDirty() bool {
	return d.httpACL != nil && d.httpACL.form != nil && d.httpACL.form.input != d.httpACL.form.saved
}

func (s *State) OpenHTTPACLReadForm(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || !d.HTTPACLAvailable() {
		return
	}
	if d.httpACLMutation != nil && d.httpACLMutation.form != nil {
		d.httpACLMutation.form.open = false
	}
	if d.httpACL == nil {
		d.httpACL = &httpACLState{}
	}
	if d.httpACL.form == nil {
		input := defaultHTTPACLReadInput()
		d.httpACL.form = &httpACLReadForm{input: input, saved: input}
	}
	s.HideHTTPACLReceipt(d)
	if d.httpACL.browser != nil {
		d.httpACL.browser.open = false
	}
	d.httpACL.form.open = true
}

func (s *State) SetHTTPACLReadInput(d *Document, input HTTPACLReadInput) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpACL == nil || d.httpACL.form == nil || d.httpACL.form.input == input {
		return
	}
	if !validHTTPACLReadInput(input) {
		d.error = locales.Text("ACL 读取输入无效；原草稿保留")
		return
	}
	d.httpACL.form.input = input
	d.httpACL.form.serial++
	d.httpACL.revision++
	d.permit = nil
}

func (s *State) RunHTTPACLReadForm(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpACL == nil || d.httpACL.form == nil {
		return
	}
	r, err := d.httpACL.form.input.request()
	if err != nil {
		d.error = locales.Text("ACL 读取参数组合无效；请检查资源、查询方式与可选参数")
		return
	}
	s.ReadHTTPACL(d, r)
	if d.job != nil && d.job.kind == "http-acl-read" {
		d.httpACL.form.open = false
		d.httpACL.showRead = true
	}
}
