package kvworkspace

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

type requestToolDraft struct {
	input, materialized contract.KVRequestToolInput
}

type requestToolState struct {
	open          bool
	kind          contract.KVRequestToolKind
	source        contract.KVRequest
	sourceVersion uint64
	drafts        map[contract.KVRequestToolKind]*requestToolDraft
	revision      uint64
}

func (d *Document) RequestTools() []contract.KVRequestTool {
	if d.closed || d.requestBuilder == nil {
		return nil
	}
	return d.presentation.Clone().Tools
}

func (d *Document) RequestToolInput(kind contract.KVRequestToolKind) (contract.KVRequestToolInput, bool) {
	for _, tool := range d.RequestTools() {
		if tool.Kind == kind {
			if d.requestTools != nil && d.requestTools.drafts[kind] != nil {
				return d.requestTools.drafts[kind].input, true
			}
			return tool.Default, true
		}
	}
	return contract.KVRequestToolInput{}, false
}

func (d *Document) hasRequestToolDraft() bool {
	if d.requestTools != nil {
		for _, draft := range d.requestTools.drafts {
			if draft.input != draft.materialized {
				return true
			}
		}
	}
	return false
}

func (d *Document) requestToolRevision() uint64 {
	if d.requestTools != nil {
		return d.requestTools.revision
	}
	return 0
}

// OpenRequestTool captures the editor being replaced. Inputs remain owned by
// the original document even when the modal closes or the profile is parked.
func (s *State) OpenRequestTool(d *Document, kind contract.KVRequestToolKind) {
	if !s.active(d) || d.Busy() || d.FileBusy() {
		return
	}
	input, ok := d.RequestToolInput(kind)
	if !ok {
		return
	}
	if d.requestTools == nil {
		d.requestTools = &requestToolState{drafts: map[contract.KVRequestToolKind]*requestToolDraft{}}
	}
	p := d.requestTools
	if p.drafts[kind] == nil {
		p.drafts[kind] = &requestToolDraft{input: input, materialized: input}
	}
	s.CancelConfirmation(d)
	p.open, p.kind, p.source = true, kind, d.Request()
	p.sourceVersion = d.Editor.Buffer.Version()
	d.error = ""
}

func (s *State) SetRequestToolInput(d *Document, kind contract.KVRequestToolKind, input contract.KVRequestToolInput) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.requestTools == nil {
		return
	}
	draft := d.requestTools.drafts[kind]
	if draft == nil || draft.input == input {
		return
	}
	if requestToolInputSize(input) > inputLimit {
		d.error = locales.Text("KV 请求文本超过 16 MiB")
		return
	}
	draft.input = input
	d.requestTools.revision++
	s.CancelConfirmation(d)
	d.permit = nil
	d.error = ""
}

// GenerateRequestTool does not acquire a lease, prepare, connect or dispatch.
// Generation replaces only the captured editor; failed validation keeps input.
func (s *State) GenerateRequestTool(d *Document) {
	s.generateRequestTool(d, false)
}

func (s *State) GenerateHTTPHealthWatch(d *Document) {
	s.generateRequestTool(d, true)
}

func (s *State) generateRequestTool(d *Document, healthWatch bool) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.requestTools == nil || !d.requestTools.open || d.requestBuilder == nil {
		return
	}
	p := d.requestTools
	draft := p.drafts[p.kind]
	if draft == nil {
		return
	}
	if p.source != d.Request() || p.sourceVersion != d.Editor.Buffer.Version() {
		d.error = locales.Text("请求文本已更改，请重新打开工具后生成")
		return
	}
	request, err := d.requestBuilder.BuildKVRequest(p.kind, draft.input)
	if healthWatch && err == nil {
		if !d.httpWatchAvailable || !httpHealthRequestTool(p.kind) {
			err = contract.ErrUnsupported
		} else {
			request, err = httpHealthWatchToolRequest(p.kind, draft.input)
		}
	}
	if err != nil || len(request.JSON) > inputLimit || !s.documentMethodAllowed(d, request.Method) {
		d.error = locales.Text("KV 请求无效")
		if err != nil {
			d.error += ": " + err.Error()
		}
		return
	}
	if p.source != d.Request() || p.sourceVersion != d.Editor.Buffer.Version() {
		d.error = locales.Text("请求文本已更改，请重新打开工具后生成")
		return
	}
	s.CancelConfirmation(d)
	s.SetMethod(d, request.Method)
	// A receipt tab may have been selected while the editor already used this
	// method. Generating a request returns to its result view in that case too.
	d.selectedHTTPWatch = request.Method == HTTPWatchMethod
	d.selectedHTTPSessionRenewal = request.Method == HTTPSessionRenewalMethod
	d.Editor.Buffer.Set(request.JSON)
	draft.materialized = draft.input
	p.source, p.open = request, false
	p.sourceVersion = d.Editor.Buffer.Version()
	p.revision++
	d.permit = nil
	d.error = ""
	d.focus = true
}
