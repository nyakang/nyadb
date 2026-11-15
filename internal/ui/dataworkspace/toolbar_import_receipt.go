package dataworkspace

import (
	"context"
	"errors"
	"io"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
)

// Export streams the complete original archive without loading it into the UI.
// The immutable descriptor/length and cancellation belong to this original
// wizard, even when the picker finishes after the document closes.
func (s *State) exportImportReceipts(services ui.Services, d *Document, picker func(mygo.SaveDialogOptions) (string, error)) {
	if d == nil || d.closed || d.transfer.Busy || d.transfer.Journal == nil || picker == nil || d.transfer.ReceiptExport.Results != nil {
		return
	}
	snapshot, err := d.transfer.Journal.receiptSnapshot()
	if err != nil {
		d.transfer.ReceiptExport.Err = err.Error()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan error, 1)
	d.transfer.ReceiptExport.Cancel, d.transfer.ReceiptExport.Results, d.transfer.ReceiptExport.Err = cancel, out, ""
	go func() {
		path, err := picker(mygo.SaveDialogOptions{Title: locales.Text("导出全部导入回执"), DefaultPath: "import-receipts.jsonl", Filters: []mygo.FileFilter{{Name: "JSONL", Extensions: []string{"jsonl"}}}})
		if err == nil && path != "" {
			err = mysqlDataAtomicFile(ctx, path, func(w io.Writer) error { _, e := io.Copy(w, snapshot); return e })
		}
		err = errors.Join(err, snapshot.Close())
		out <- err
		services.Invalidate()
	}()
}
