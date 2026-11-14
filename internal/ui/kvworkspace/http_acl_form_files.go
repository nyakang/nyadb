package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/locales"
)

type httpACLFormEnvelope struct {
	Version int              `json:"version"`
	Kind    string           `json:"kind"`
	Input   HTTPACLReadInput `json:"input"`
}

func encodeHTTPACLReadInput(input HTTPACLReadInput) ([]byte, error) {
	if !validHTTPACLReadInput(input) {
		return nil, jsondocument.ErrBounds
	}
	raw, err := json.MarshalIndent(httpACLFormEnvelope{1, "consul-acl-read", input}, "", "  ")
	if len(raw) > inputLimit {
		return nil, jsondocument.ErrBounds
	}
	return raw, err
}

func decodeHTTPACLReadInput(raw []byte) (HTTPACLReadInput, error) {
	var fields map[string]json.RawMessage
	if jsondocument.Decode(raw, &fields, inputLimit) != nil || len(fields) != 3 || string(fields["version"]) != "1" || string(fields["kind"]) != `"consul-acl-read"` {
		return HTTPACLReadInput{}, jsondocument.ErrInvalid
	}
	var inputFields map[string]json.RawMessage
	if json.Unmarshal(fields["input"], &inputFields) != nil {
		return HTTPACLReadInput{}, jsondocument.ErrInvalid
	}
	t := reflect.TypeFor[HTTPACLReadInput]()
	if len(inputFields) != t.NumField() {
		return HTTPACLReadInput{}, jsondocument.ErrInvalid
	}
	for i := range t.NumField() {
		value, ok := inputFields[t.Field(i).Name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return HTTPACLReadInput{}, jsondocument.ErrInvalid
		}
	}
	var input HTTPACLReadInput
	if json.Unmarshal(fields["input"], &input) != nil || !validHTTPACLReadInput(input) {
		return HTTPACLReadInput{}, jsondocument.ErrInvalid
	}
	return input, nil
}

func (s *State) httpACLFormFile(d *Document, kind string, services ui.Services) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpACL == nil || d.httpACL.form == nil {
		return
	}
	form, f := d.httpACL.form, s.deps.Files
	input := form.input
	if kind == "open-acl-input" {
		if f.OpenPath == nil || f.OpenReader == nil {
			return
		}
		s.fileJob(d, kind, func(ctx context.Context) fileResult {
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
			loaded, err := decodeHTTPACLReadInput(raw)
			if err == nil {
				err = ctx.Err()
			}
			return fileResult{path: path, aclForm: &loaded, err: err}
		})
	} else {
		raw, err := encodeHTTPACLReadInput(input)
		if err != nil {
			d.error = locales.Text("ACL 读取输入无效；原草稿保留")
			return
		}
		switch kind {
		case "copy-acl-input":
			s.fileJob(d, kind, func(ctx context.Context) fileResult { return fileResult{clipboard: string(raw), err: ctx.Err()} })
			d.io.clipboard = services.WriteClipboard
		case "save-acl-input", "save-as-acl-input":
			path := form.path
			if kind == "save-as-acl-input" {
				path = ""
			}
			if path == "" && f.SavePath == nil || f.WriteStream == nil {
				return
			}
			s.fileJob(d, kind, func(ctx context.Context) fileResult {
				if err := ctx.Err(); err != nil {
					return fileResult{err: err}
				}
				if path == "" {
					var err error
					path, err = f.SavePath("acl-read.nyakv-input.json")
					if err != nil || path == "" {
						return fileResult{err: err}
					}
				}
				return fileResult{path: path, aclForm: &input, err: writeCaptured(ctx, f, path, raw)}
			})
		default:
			return
		}
	}
	d.io.aclForm, d.io.aclSerial = form, form.serial
}

func (s *State) consumeHTTPACLFormFile(d *Document, job *fileOperation, r fileResult) bool {
	if job.kind != "open-acl-input" && job.kind != "copy-acl-input" && job.kind != "save-acl-input" && job.kind != "save-as-acl-input" {
		return false
	}
	if r.err != nil || job.canceled {
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			d.error = locales.Text("ACL 文件操作失败；原回执保留")
		}
		return true
	}
	if job.kind == "copy-acl-input" {
		if job.clipboard != nil {
			job.clipboard(r.clipboard)
		}
		return true
	}
	if r.path == "" || r.aclForm == nil || d.httpACL == nil || d.httpACL.form != job.aclForm {
		return true
	}
	form := d.httpACL.form
	if job.kind == "open-acl-input" {
		if form.serial != job.aclSerial {
			d.error = locales.Text("ACL 草稿已更改，未载入所选文件")
			return true
		}
		s.HideHTTPACLReceipt(d)
		form.input = *r.aclForm
		form.serial++
		d.httpACL.revision++
		d.permit = nil
	}
	form.saved, form.path = *r.aclForm, r.path
	return true
}
