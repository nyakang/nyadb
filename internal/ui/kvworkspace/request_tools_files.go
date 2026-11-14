package kvworkspace

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

func requestToolTextFields(input contract.KVRequestToolInput) []string {
	s := input.HTTPSession
	h := input.HTTPHealth
	a := input.HTTPAgentService
	check := input.HTTPAgentCheck
	ttl := input.HTTPAgentTTL
	maintenance := input.HTTPAgentMaintenance
	catalog := input.HTTPCatalog
	return []string{input.Key, input.ValueBase64, input.Flags, input.CompareIndex, input.TransactionJSON, s.ID, s.Name, s.Node, s.TTL, s.LockDelay, s.Behavior, s.NodeChecksJSON, h.State, h.Node, h.Service, h.Passing, h.Filter, h.Near, h.Index, h.Wait, input.HTTPAgent.Filter, a.ID, a.DefinitionJSON, a.ReplaceExistingChecks, check.ID, check.DefinitionJSON, ttl.ID, ttl.NoteJSON, ttl.DefinitionJSON, maintenance.ID, maintenance.Enable, maintenance.ReasonJSON, catalog.Node, catalog.Service, catalog.Filter, catalog.Near, catalog.Index, catalog.Wait}
}

func requestToolInputSize(input contract.KVRequestToolInput) int {
	size := 0
	for _, value := range requestToolTextFields(input) {
		size += len(value)
	}
	return size
}

// Tool inputs are drafts, independent from the generated request and execution
// receipts. Exporting incomplete fields never validates or executes a request.
func encodeRequestToolInput(kind contract.KVRequestToolKind, input contract.KVRequestToolInput) ([]byte, error) {
	if requestToolInputSize(input) > inputLimit {
		return nil, jsondocument.ErrBounds
	}
	for _, value := range requestToolTextFields(input) {
		if !utf8.ValidString(value) {
			return nil, jsondocument.ErrInvalid
		}
	}
	raw, err := json.MarshalIndent(struct {
		Version int                         `json:"version"`
		Kind    contract.KVRequestToolKind  `json:"tool"`
		Input   contract.KVRequestToolInput `json:"input"`
	}{1, kind, input}, "", "  ")
	if len(raw) > inputLimit {
		return nil, jsondocument.ErrBounds
	}
	return raw, err
}

func (s *State) CopyRequestToolInput(services ui.Services, d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.requestTools == nil || !d.requestTools.open {
		return
	}
	kind := d.requestTools.kind
	input := d.requestTools.drafts[kind].input
	s.fileJob(d, "copy-request-tool", func(ctx context.Context) fileResult {
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		raw, err := encodeRequestToolInput(kind, input)
		if err == nil {
			err = ctx.Err()
		}
		return fileResult{clipboard: string(raw), err: err}
	})
	d.io.clipboard = services.WriteClipboard
}

func (s *State) ExportRequestToolInput(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.requestTools == nil || !d.requestTools.open {
		return
	}
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	kind := d.requestTools.kind
	input := d.requestTools.drafts[kind].input
	s.fileJob(d, "export", func(ctx context.Context) fileResult {
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		raw, err := encodeRequestToolInput(kind, input)
		if err != nil {
			return fileResult{err: err}
		}
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		path, err := f.SavePath(string(kind) + ".nyakv-tool.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
