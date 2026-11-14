package kvworkspace

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestHTTPArchiveExportIncludesFailedCaptureAndDistinctUnknownRestore(t *testing.T) {
	s, d, _ := httpTestState(t)
	x := &httpArchiveState{original: &contract.KVHTTPArchiveCapture{}, restoreLast: &contract.KVHTTPArchiveRestoreResult{Request: contract.KVHTTPArchiveRestoreRequest{Archive: []byte{1, 2, 3}}, Completed: true}, restoreUnknown: &contract.KVHTTPArchiveRestoreResult{Request: contract.KVHTTPArchiveRestoreRequest{Archive: []byte{0, 255}}, WriteOutcomeUnknown: true}}
	d.httpArchive = x
	var exported []byte
	s.SetFiles(Files{SavePath: func(string) (string, error) { return "private.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		exported = bytes.Clone(b.Bytes())
		return err
	}})
	s.ExportHTTPArchiveReceipts(d)
	if d.FileBusy() {
		t.Fatal("Snapshot export bypassed consent")
	}
	x.exportConsent = true
	s.ExportHTTPArchiveReceipts(d)
	consume(t, s, d)
	var record struct {
		Capture                 *contract.KVHTTPArchiveCapture
		Restore, UnknownRestore *contract.KVHTTPArchiveRestoreResult
	}
	if json.Unmarshal(exported, &record) != nil || record.Capture == nil || !bytes.Equal(record.Restore.Request.Archive, []byte{1, 2, 3}) || !bytes.Equal(record.UnknownRestore.Request.Archive, []byte{0, 255}) || !record.UnknownRestore.WriteOutcomeUnknown || x.exportConsent {
		t.Fatal("Snapshot export lost original evidence")
	}
	s.ClearHTTPArchive(d)
	if d.httpArchive != x || x.restoreUnknown == nil {
		t.Fatal("clear erased unknown restore evidence")
	}
}

func TestHTTPArchiveLateFileInputClearsBytesAfterOriginalTabClose(t *testing.T) {
	s, d, _ := httpTestState(t)
	d.httpArchive = &httpArchiveState{}
	entered, release := make(chan struct{}), make(chan struct{})
	raw := []byte("private complete original archive")
	s.SetFiles(Files{OpenPath: func() (string, error) { return "original.snap", nil }, OpenReader: func(string) (io.ReadCloser, error) {
		close(entered)
		<-release
		return io.NopCloser(bytes.NewReader(raw)), nil
	}})
	s.OpenHTTPArchiveRestoreFile(d)
	<-entered
	finished := d.io.finished
	closed := make(chan struct{})
	go func() { s.Close(d); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("tab close blocked on late file picker")
	}
	close(release)
	await(t, finished)
	if d.httpArchive != nil || d.Editor != nil {
		t.Fatal("late source reattached to closed tab")
	}
}
