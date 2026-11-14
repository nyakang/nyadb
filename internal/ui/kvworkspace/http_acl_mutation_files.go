package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/locales"
)

type httpACLMutationTransfer struct {
	kind        string
	form        *httpACLMutationForm
	input       HTTPACLMutationInput
	receipt     *contract.KVHTTPACLMutationResult
	observation *httpACLMutationObservation
	revision    uint64
	raw         []byte
	checked     bool
}

func encodeHTTPACLMutationInput(input HTTPACLMutationInput) ([]byte, error) {
	if !validHTTPACLMutationForm(input) {
		return nil, jsondocument.ErrBounds
	}
	raw, err := json.MarshalIndent(struct {
		Version int                  `json:"version"`
		Kind    string               `json:"kind"`
		Input   HTTPACLMutationInput `json:"input"`
	}{1, "consul-acl-write", input}, "", "  ")
	if len(raw) > inputLimit {
		return nil, jsondocument.ErrBounds
	}
	return raw, err
}
func decodeHTTPACLMutationInput(raw []byte) (HTTPACLMutationInput, error) {
	var fields map[string]json.RawMessage
	if jsondocument.Decode(raw, &fields, inputLimit) != nil || len(fields) != 3 || string(fields["version"]) != "1" || string(fields["kind"]) != `"consul-acl-write"` {
		return HTTPACLMutationInput{}, jsondocument.ErrInvalid
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(fields["input"], &members) != nil {
		return HTTPACLMutationInput{}, jsondocument.ErrInvalid
	}
	typ := reflect.TypeFor[HTTPACLMutationInput]()
	if len(members) != typ.NumField() {
		return HTTPACLMutationInput{}, jsondocument.ErrInvalid
	}
	for i := range typ.NumField() {
		v, ok := members[typ.Field(i).Name]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return HTTPACLMutationInput{}, jsondocument.ErrInvalid
		}
	}
	var input HTTPACLMutationInput
	if json.Unmarshal(fields["input"], &input) != nil || !validHTTPACLMutationForm(input) {
		return input, jsondocument.ErrInvalid
	}
	return input, nil
}
func (s *State) prepareHTTPACLMutationTransfer(d *Document, kind string) bool {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpACLMutation == nil {
		return false
	}
	x := d.httpACLMutation
	x.transfer = nil
	p := &httpACLMutationTransfer{kind: kind, form: x.form, revision: x.revision}
	var err error
	switch kind {
	case "copy-acl-write-input", "save-acl-write-input", "save-as-acl-write-input":
		if x.form == nil {
			return false
		}
		p.input = x.form.input
		p.raw, err = encodeHTTPACLMutationInput(p.input)
	case "copy-acl-write-impact", "export-acl-write-impact":
		if x.observation == nil {
			return false
		}
		p.observation = x.observation
		p.raw, err = json.MarshalIndent(struct {
			Version     int                         `json:"version"`
			Kind        string                      `json:"kind"`
			Observation *httpACLMutationObservation `json:"observation"`
		}{1, "consul-original-acl-impact", x.observation}, "", "  ")
	case "copy-acl-write-receipt", "export-acl-write-receipt":
		if x.last == nil {
			return false
		}
		p.receipt = x.last
		p.raw, err = json.MarshalIndent(struct {
			Version int                              `json:"version"`
			Kind    string                           `json:"kind"`
			Result  contract.KVHTTPACLMutationResult `json:"result"`
		}{1, "consul-original-acl-write-receipt", x.last.Clone()}, "", "  ")
	default:
		return false
	}
	if err != nil {
		d.error = locales.Text("ACL 文件操作失败；原回执保留")
		return false
	}
	x.transfer = p
	return true
}
func (s *State) confirmHTTPACLMutationTransfer(services ui.Services, d *Document, checked bool) bool {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpACLMutation == nil || !checked {
		return false
	}
	x := d.httpACLMutation
	p := x.transfer
	x.transfer = nil
	if p == nil || p.revision != x.revision || p.form != x.form || p.receipt != nil && p.receipt != x.last || p.observation != nil && p.observation != x.observation || p.receipt == nil && p.observation == nil && (x.form == nil || p.input != x.form.input) {
		return false
	}
	raw := bytes.Clone(p.raw)
	f := s.deps.Files
	if p.kind == "copy-acl-write-input" || p.kind == "copy-acl-write-receipt" || p.kind == "copy-acl-write-impact" {
		s.fileJob(d, p.kind, func(ctx context.Context) fileResult { return fileResult{clipboard: string(raw), err: ctx.Err()} })
		d.io.clipboard = services.WriteClipboard
	} else {
		path := ""
		if p.kind == "save-acl-write-input" {
			path = x.form.path
		}
		if f.WriteStream == nil || path == "" && f.SavePath == nil {
			return false
		}
		s.fileJob(d, p.kind, func(ctx context.Context) fileResult {
			if err := ctx.Err(); err != nil {
				return fileResult{err: err}
			}
			if path == "" {
				name := "acl-write.nyakv-request.json"
				if p.observation != nil {
					name = "original-acl-impact.nyakv-receipt.json"
				}
				if p.receipt != nil {
					name = "original-acl-write.nyakv-receipt.json"
				}
				var err error
				path, err = f.SavePath(name)
				if err != nil || path == "" {
					return fileResult{err: err}
				}
			}
			return fileResult{path: path, aclMutationForm: &p.input, err: writeCaptured(ctx, f, path, raw)}
		})
	}
	d.io.aclMutationForm = p.form
	return true
}
func (s *State) openHTTPACLMutationInput(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpACLMutation == nil || d.httpACLMutation.form == nil {
		return
	}
	f := s.deps.Files
	if f.OpenPath == nil || f.OpenReader == nil {
		return
	}
	x := d.httpACLMutation
	form := x.form
	d.clearConfirmation()
	x.transfer = nil
	s.fileJob(d, "open-acl-write-input", func(ctx context.Context) fileResult {
		path, err := f.OpenPath()
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		if err = ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		reader, err := f.OpenReader(path)
		if err != nil {
			return fileResult{err: err}
		}
		defer reader.Close()
		raw, err := io.ReadAll(io.LimitReader(reader, inputLimit+1))
		if err != nil {
			return fileResult{err: err}
		}
		input, err := decodeHTTPACLMutationInput(raw)
		if err == nil {
			err = ctx.Err()
		}
		return fileResult{path: path, aclMutationForm: &input, err: err}
	})
	d.io.aclMutationForm = form
	d.io.aclSerial = form.serial
}
func (s *State) consumeHTTPACLMutationFile(d *Document, job *fileOperation, r fileResult) bool {
	switch job.kind {
	case "copy-acl-write-input", "copy-acl-write-receipt", "save-acl-write-input", "save-as-acl-write-input", "export-acl-write-receipt", "export-acl-write-impact", "copy-acl-write-impact", "open-acl-write-input":
	default:
		return false
	}
	if r.err != nil || job.canceled {
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			d.error = locales.Text("ACL 文件操作失败；原回执保留")
		}
		return true
	}
	if job.kind == "copy-acl-write-input" || job.kind == "copy-acl-write-receipt" || job.kind == "copy-acl-write-impact" {
		if job.clipboard != nil {
			job.clipboard(r.clipboard)
		}
		return true
	}
	if job.kind == "export-acl-write-impact" || job.kind == "export-acl-write-receipt" || r.path == "" || r.aclMutationForm == nil || d.httpACLMutation == nil || d.httpACLMutation.form != job.aclMutationForm {
		return true
	}
	x := d.httpACLMutation
	form := x.form
	if job.kind == "open-acl-write-input" {
		if form.serial != job.aclSerial {
			d.error = locales.Text("ACL 草稿已更改，未载入所选文件")
			return true
		}
		s.HideHTTPACLReceipt(d)
		form.input = *r.aclMutationForm
		form.serial++
		x.revision++
		d.clearHTTPACLMutationConfirmation()
		d.permit = nil
		if input, err := form.input.request(); err == nil {
			x.input = input
		}
	}
	form.saved, form.path = *r.aclMutationForm, r.path
	if saved, err := form.saved.request(); err == nil {
		x.saved = saved
	}
	return true
}
