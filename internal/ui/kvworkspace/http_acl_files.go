package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

// A transfer is one local authorization for a captured original receipt. It
// carries no read or write authority and never accepts provider error text.
type httpACLTransfer struct {
	original *contract.KVHTTPACLReadResult
	revision uint64
	row      int
	copy     bool
	checked  bool
	raw      []byte
}

func (s *State) prepareHTTPACLTransfer(d *Document, copyValue bool) bool {
	if !s.active(d) || d.Busy() || d.FileBusy() || !d.httpACLContent() {
		return false
	}
	x := d.httpACL
	x.transfer = nil
	p := &httpACLTransfer{original: x.last, revision: x.revision, row: -1, copy: copyValue}
	if copyValue {
		if !x.valid || x.browser == nil || x.last.Projection == nil || !x.last.Projection.ProjectionComplete {
			return false
		}
		p.row = x.browser.selected
		if p.row < 0 || p.row >= len(x.last.Projection.Rows) {
			return false
		}
		p.raw = bytes.Clone(x.last.Projection.Rows[p.row].Raw)
		if !utf8.Valid(p.raw) {
			return false
		}
	} else {
		if s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil {
			return false
		}
		var err error
		p.raw, err = json.MarshalIndent(struct {
			Version int                          `json:"version"`
			Kind    string                       `json:"kind"`
			Result  contract.KVHTTPACLReadResult `json:"result"`
		}{1, "consul-original-acl-receipt", x.last.Clone()}, "", "  ")
		if err != nil {
			d.error = locales.Text("ACL 文件操作失败；原回执保留")
			return false
		}
	}
	x.transfer = p
	return true
}

func (s *State) confirmHTTPACLTransfer(services ui.Services, d *Document, checked bool) bool {
	if !s.active(d) || d.Busy() || d.FileBusy() || !d.httpACLContent() || d.httpACL.transfer == nil || !checked {
		return false
	}
	x := d.httpACL
	p := x.transfer
	x.transfer = nil // one use, including stale selections
	if p.original != x.last || p.revision != x.revision || p.copy && (x.browser == nil || p.row != x.browser.selected) {
		return false
	}
	raw := bytes.Clone(p.raw)
	if p.copy {
		s.fileJob(d, "copy-acl-value", func(ctx context.Context) fileResult {
			return fileResult{clipboard: string(raw), err: ctx.Err()}
		})
		d.io.clipboard = services.WriteClipboard
	} else {
		f := s.deps.Files
		if f.SavePath == nil || f.WriteStream == nil {
			return false
		}
		s.fileJob(d, "export-acl-receipt", func(ctx context.Context) fileResult {
			if err := ctx.Err(); err != nil {
				return fileResult{err: err}
			}
			path, err := f.SavePath("original-acl.nyakv-receipt.json")
			if err != nil || path == "" {
				return fileResult{err: err}
			}
			return fileResult{err: writeCaptured(ctx, f, path, raw)}
		})
	}
	return true
}

func (s *State) consumeHTTPACLFile(d *Document, job *fileOperation, r fileResult) bool {
	if s.consumeHTTPACLFormFile(d, job, r) {
		return true
	}
	if job.kind != "copy-acl-value" && job.kind != "export-acl-receipt" {
		return false
	}
	if r.err != nil {
		if !errors.Is(r.err, context.Canceled) {
			d.error = locales.Text("ACL 文件操作失败；原回执保留")
		}
		return true
	}
	if job.kind == "copy-acl-value" && !job.canceled && job.clipboard != nil {
		job.clipboard(r.clipboard)
	}
	return true
}
