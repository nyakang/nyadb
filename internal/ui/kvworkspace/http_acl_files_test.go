package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func aclTransferFixture(t *testing.T) (*State, *Document, *aclStateLease) {
	t.Helper()
	s, d, l, _ := aclWorkspace(t)
	s.ReadHTTPACL(d, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
	consume(t, s, d)
	s.openHTTPACLBrowser(d)
	d.httpACL.browser.selected = 0
	return s, d, l
}

func TestHTTPACLTransferRequiresOriginalConsentAndParksClipboard(t *testing.T) {
	s, d, l := aclTransferFixture(t)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services() }, 720, 520)
	tt.Frame()
	want := string(d.httpACL.last.Projection.Rows[0].Raw)
	if s.confirmHTTPACLTransfer(services, d, true) || !s.prepareHTTPACLTransfer(d, true) || s.confirmHTTPACLTransfer(services, d, false) || d.FileBusy() {
		t.Fatal("ACL transfer bypassed original explicit consent")
	}
	if !s.confirmHTTPACLTransfer(services, d, true) || s.confirmHTTPACLTransfer(services, d, true) {
		t.Fatal("ACL transfer was not one use")
	}
	active := false
	s.deps.Active = func(*Document) bool { return active }
	await(t, d.io.finished)
	s.Consume()
	if tt.Clipboard() != "" || !d.FileBusy() {
		t.Fatal("parked copy published to clipboard")
	}
	active = true
	consume(t, s, d)
	if tt.Clipboard() != want || l.reads.Load() != 1 || l.executes.Load() != 0 {
		t.Fatal("ACL transfer changed bytes or dispatched a request")
	}
	if _, ok := s.ReceiptJSON(d); ok {
		t.Fatal("ACL transfer entered generic receipt copy")
	}
}

func TestHTTPACLTransferRejectsStaleSelectionParkingDisconnectAndNewRead(t *testing.T) {
	for _, change := range []string{"row", "park", "disconnect", "retire", "read"} {
		t.Run(change, func(t *testing.T) {
			s, d, l := aclTransferFixture(t)
			if !s.RevealHTTPACLReceipt(d, d.httpACL.last.LocalRecordID) || !s.prepareHTTPACLTransfer(d, true) {
				t.Fatal("original receipt unavailable")
			}
			d.httpACL.browser.preview, d.httpACL.browser.detail = "synthetic-sensitive-value", true
			switch change {
			case "row":
				d.httpACL.browser.selected = -1
			case "park":
				s.deps.Active = func(*Document) bool { return false }
				s.Consume()
				s.deps.Active = nil
			case "disconnect":
				s.DisconnectProfile(d.profileID)
			case "retire":
				l.closed.Store(true)
				s.Consume()
			case "read":
				s.ReadHTTPACL(d, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
				consume(t, s, d)
			}
			if s.confirmHTTPACLTransfer(ui.Services{}, d, true) || d.FileBusy() {
				t.Fatal("stale original transfer remained authorized")
			}
			if change != "row" && (d.httpACL.revealed != "" || d.httpACL.browser != nil && (d.httpACL.browser.preview != "" || d.httpACL.browser.detail)) {
				t.Fatal("original secrets remained displayed after lifecycle change")
			}
		})
	}
}

func TestHTTPACLExportRetainsCompleteOriginalSnapshotAcrossPickerAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "capture", true: "cancel"}[cancel], func(t *testing.T) {
			s, d, l := aclTransferFixture(t)
			raw := []byte(`{"SecretID":"` + strings.Repeat("private", 12000) + `","precise":18446744073709551615}`)
			d.httpACL.last.Receipt.HTTP.Body = bytes.Clone(raw)
			d.httpACL.last.Receipt.HTTP.Captured = false
			d.httpACL.last.Receipt.HTTP.EOF = false
			d.httpACL.last.Receipt.HTTP.Header = map[string][]string{"X-Original": {"private-header"}}
			original := d.httpACL.last.Clone()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var written bytes.Buffer
			writes := 0
			s.SetFiles(Files{SavePath: func(string) (string, error) {
				close(entered)
				<-release
				return "owned-acl.json", nil
			}, WriteStream: func(path string, write func(io.Writer) error) error {
				writes++
				return write(&written)
			}})
			if !s.prepareHTTPACLTransfer(d, false) || !s.confirmHTTPACLTransfer(ui.Services{}, d, true) {
				t.Fatal("explicit original export failed")
			}
			await(t, entered)
			d.httpACL.last.Receipt.HTTP.Body[0] ^= 1
			d.httpACL.last.Receipt.HTTP.Header["X-Original"][0] = "later header"
			if cancel {
				s.CancelFile(d)
			}
			once.Do(func() { close(release) })
			consume(t, s, d)
			if cancel {
				if writes != 0 || written.Len() != 0 {
					t.Fatal("canceled original picker wrote private bytes")
				}
				return
			}
			var exported struct {
				Version int                          `json:"version"`
				Kind    string                       `json:"kind"`
				Result  contract.KVHTTPACLReadResult `json:"result"`
			}
			if json.Unmarshal(written.Bytes(), &exported) != nil || exported.Version != 1 || exported.Kind != "consul-original-acl-receipt" || !reflect.DeepEqual(exported.Result, original) || writes != 1 || l.reads.Load() != 1 {
				t.Fatal("private export lost original scope, incomplete receipt or full captured bytes")
			}
		})
	}
}

func TestHTTPACLFileErrorsCannotExposeProviderText(t *testing.T) {
	s, d, _ := aclTransferFixture(t)
	s.SetFiles(Files{SavePath: func(string) (string, error) { return "", errors.New("synthetic-private-path-secret") }, WriteStream: func(string, func(io.Writer) error) error { return nil }})
	if !s.prepareHTTPACLTransfer(d, false) || !s.confirmHTTPACLTransfer(ui.Services{}, d, true) {
		t.Fatal("original export unavailable")
	}
	consume(t, s, d)
	if strings.Contains(d.Error(), "synthetic-private") || d.Error() == "" || !d.httpACLContent() {
		t.Fatal("private file failure exposed native error or lost receipt")
	}
	s.fileJob(d, "copy-acl-value", func(ctx context.Context) fileResult { return fileResult{clipboard: "private", err: ctx.Err()} })
	await(t, d.io.finished)
	s.CancelFile(d)
	copied := false
	d.io.clipboard = func(string) { copied = true }
	consume(t, s, d)
	if copied {
		t.Fatal("canceled private copy published after worker completion")
	}
}
