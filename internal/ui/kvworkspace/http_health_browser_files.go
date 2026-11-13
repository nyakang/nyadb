package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/egoist/mygo/ui"
)

// Decode the original array position as JSON bytes, preserving every unknown
// field and exact number. Callers use an immutable captured response body.
func originalHealthRow(body []byte, index int) ([]byte, error) {
	if index < 0 {
		return nil, errors.New("invalid original health row")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	if token, err := d.Token(); err != nil || token != json.Delim('[') {
		return nil, errors.New("invalid original health response")
	}
	for i := 0; d.More(); i++ {
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return nil, err
		}
		if i == index {
			return raw, nil
		}
	}
	return nil, errors.New("original health row absent")
}

func (s *State) capturedHealthRow(d *Document) ([]byte, int, bool) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpHealthBrowser == nil {
		return nil, 0, false
	}
	r := s.healthBrowserReceipt(d)
	if r == nil || r.Scope.Namespace.ProfileID != d.profileID {
		return nil, 0, false
	}
	index, ok := d.httpHealthBrowser.selectedIndex(r)
	if !ok {
		return nil, 0, false
	}
	return bytes.Clone(r.Result.HTTP.Body), index, true
}

func (s *State) CopySelectedHTTPHealthRow(services ui.Services, d *Document) {
	body, index, ok := s.capturedHealthRow(d)
	if !ok {
		return
	}
	s.fileJob(d, "copy-health-row", func(ctx context.Context) fileResult {
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		raw, err := originalHealthRow(body, index)
		if err == nil {
			err = ctx.Err()
		}
		return fileResult{clipboard: string(raw), err: err}
	})
	d.io.clipboard = services.WriteClipboard
}

func (s *State) ExportSelectedHTTPHealthRow(d *Document) {
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	body, index, ok := s.capturedHealthRow(d)
	if !ok {
		return
	}
	s.fileJob(d, "export-health-row", func(ctx context.Context) fileResult {
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		raw, err := originalHealthRow(body, index)
		if err != nil {
			return fileResult{err: err}
		}
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		path, err := f.SavePath("original-health-row.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
