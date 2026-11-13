package kvworkspace

import (
	"bytes"
	"context"

	"github.com/egoist/mygo/ui"
)

func (s *State) capturedAgentValue(d *Document) ([]byte, bool) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpAgentBrowser == nil {
		return nil, false
	}
	r := s.selectedReceipt(d)
	if r == nil || r.Scope.Namespace.ProfileID != d.profileID {
		return nil, false
	}
	index, ok := d.httpAgentBrowser.selectedIndex(r)
	if !ok {
		return nil, false
	}
	return bytes.Clone(agentReceiptProjection(r).Rows[index].Raw), true
}

func (s *State) CopySelectedHTTPAgentValue(services ui.Services, d *Document) {
	raw, ok := s.capturedAgentValue(d)
	if !ok {
		return
	}
	s.fileJob(d, "copy-agent-value", func(ctx context.Context) fileResult {
		return fileResult{clipboard: string(raw), err: ctx.Err()}
	})
	d.io.clipboard = services.WriteClipboard
}

func (s *State) ExportSelectedHTTPAgentValue(d *Document) {
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	raw, ok := s.capturedAgentValue(d)
	if !ok {
		return
	}
	s.fileJob(d, "export-agent-value", func(ctx context.Context) fileResult {
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		path, err := f.SavePath("original-agent-value.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
