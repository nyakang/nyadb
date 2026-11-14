package kvworkspace

import (
	"bytes"
	"context"

	"github.com/egoist/mygo/ui"
)

func (s *State) capturedCatalogValue(d *Document) ([]byte, bool) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpCatalogBrowser == nil {
		return nil, false
	}
	r := s.selectedReceipt(d)
	if r == nil || r.Scope.Namespace.ProfileID != d.profileID {
		return nil, false
	}
	index, ok := d.httpCatalogBrowser.selectedIndex(r)
	if !ok {
		return nil, false
	}
	raw, ok := catalogRowRaw(r, index)
	return bytes.Clone(raw), ok
}

func (s *State) CopySelectedHTTPCatalogValue(services ui.Services, d *Document) {
	raw, ok := s.capturedCatalogValue(d)
	if !ok {
		return
	}
	s.fileJob(d, "copy-catalog-value", func(ctx context.Context) fileResult { return fileResult{clipboard: string(raw), err: ctx.Err()} })
	d.io.clipboard = services.WriteClipboard
}

func (s *State) ExportSelectedHTTPCatalogValue(d *Document) {
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	raw, ok := s.capturedCatalogValue(d)
	if !ok {
		return
	}
	s.fileJob(d, "export-catalog-value", func(ctx context.Context) fileResult {
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		path, err := f.SavePath("original-catalog-value.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
