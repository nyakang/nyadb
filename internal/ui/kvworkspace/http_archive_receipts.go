package kvworkspace

import (
	"context"
	"encoding/json"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Export all complete original facts, including failed storage and unknown
// restoration. The receipt includes the exact restore input and is private.
func (s *State) ExportHTTPArchiveReceipts(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpArchive == nil || !d.httpArchive.exportConsent {
		return
	}
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	x := d.httpArchive
	x.exportConsent = false
	raw, err := json.MarshalIndent(struct {
		Version                 int
		Kind                    string
		Capture                 *contract.KVHTTPArchiveCapture
		Restore, UnknownRestore *contract.KVHTTPArchiveRestoreResult
		Publications            []contract.KVSnapshotFileResult
	}{1, "consul-snapshot-private-receipts", x.original, x.restoreLast, x.restoreUnknown, x.files}, "", "  ")
	if err != nil || len(raw) > 192<<20 {
		return
	}
	s.fileJob(d, "http-archive-receipts-export", func(ctx context.Context) fileResult {
		path, err := f.SavePath("consul-snapshot-private-receipts.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{err: writeCaptured(ctx, f, path, raw)}
	})
}
