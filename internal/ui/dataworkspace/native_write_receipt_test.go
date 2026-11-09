package dataworkspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func TestDataNativeWriteReceiptExportOwnsOriginalAndCloseCancelsLatePicker(t *testing.T) {
	s := New(Dependencies{})
	d := s.OpenObject(1, contract.ObjectRef{ProfileID: 7, Catalog: "original", Kind: "label", Name: "owned"}, contract.Capabilities{Documents: true})
	defer s.CloseAll()
	original := `{"CommitAttempted":true,"CommitConfirmed":false,"original":"猫"}`
	d.nativeReceipt.JSON = original
	entered, release := make(chan struct{}), make(chan struct{})
	path := filepath.Join(t.TempDir(), "receipt.json")
	s.exportNativeWriteReceipt(ui.Services{}, d, func(mygo.SaveDialogOptions) (string, error) { close(entered); <-release; return path, nil })
	<-entered
	d.nativeReceipt.JSON = `{"new":"replacement"}`
	close(release)
	select {
	case err := <-d.nativeReceipt.Results:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receipt export stalled")
	}
	d.nativeReceipt.Cancel()
	d.nativeReceipt.Cancel, d.nativeReceipt.Results = nil, nil
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != original {
		t.Fatal("receipt export adopted a later write", err)
	}
	entered, release = make(chan struct{}), make(chan struct{})
	if err = os.WriteFile(path, []byte("preserve original file"), 0600); err != nil {
		t.Fatal(err)
	}
	s.exportNativeWriteReceipt(ui.Services{}, d, func(mygo.SaveDialogOptions) (string, error) { close(entered); <-release; return path, nil })
	<-entered
	results := d.nativeReceipt.Results
	s.Close(d)
	close(release)
	select {
	case err = <-results:
		if err == nil {
			t.Fatal("closed receipt export wrote a late destination")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closed receipt export stalled")
	}
	raw, err = os.ReadFile(path)
	if err != nil || string(raw) != "preserve original file" || d.nativeReceipt.JSON != "" || d.nativeReceipt.Results != nil {
		t.Fatal("close retained receipt or replaced original destination", err)
	}
}

func TestDataNativeWriteReceiptPreviewIsBoundedWithoutNarrowingOwnedReceipt(t *testing.T) {
	raw := strings.Repeat("猫", 30000)
	preview := nativeWriteReceiptPreview(raw)
	if !utf8.ValidString(preview) || len(preview) > 66000 || !strings.Contains(preview, "…") || len(raw) != 90000 {
		t.Fatal("receipt preview narrowed the owned bytes or split Unicode")
	}
}

func TestDataNativeUnknownCloseUsesOriginalGuardAndCannotRecoverOrReplay(t *testing.T) {
	r := Runtime{NativeReceipts: true}
	s := New(Dependencies{Runtime: func() Runtime { return r }})
	d := s.OpenObject(1, contract.NamespaceRef{ProfileID: 12, Catalog: "original"}.Object("label", "owned"), contract.Capabilities{Documents: true})
	defer s.Close(d)
	// Missing receipt bytes must not trap an incomplete/uncertain document.
	d.edit.Uncertain = &contract.DataEditResult{CommitUncertain: true, Incomplete: true}
	d.edit.Err = "native commit acknowledgement lost"
	d.edit.Drafts = map[string]*mysqlRowDraft{"original": {Key: "original", Kind: "update"}}
	closed := 0
	s.deps.CloseDocument = func(_ ui.Services, original *Document) {
		if original != d || original.Object().Catalog != "original" || s.GuardLeave(original, nil) {
			t.Fatal("native close lost the original document or guard authorization")
		}
		closed++
		s.Close(original)
	}
	d.edit.Busy = true
	s.closeNativeWriteDocument(ui.Services{}, d)
	if closed != 0 || d.nativeReceipt.CloseApproved {
		t.Fatal("native close preceded executor acknowledgement")
	}
	d.edit.Busy = false
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Toolbar(c, func() { s.nativeWriteReceiptActions(c, d) })
		s.mysqlDataEditStatus(c, d)
		s.Modals(c)
	}, 1280, 800)
	tt.Frame()
	if tt.HasText(locales.Text("核验提交结果")) {
		t.Fatal("missing native verifier exposed a recovery command")
	}
	if err := tt.Click(locales.Text("关闭原写入文档")); err != nil {
		t.Fatal(err)
	}
	if !d.LeavePending() {
		t.Fatal("native close decision lost workbench leave guard")
	}
	if err := tt.Click(locales.Text("取消")); err != nil {
		t.Fatal(err)
	}
	if closed != 0 || !d.HasDraft() || !s.GuardLeave(d, nil) || d.edit.Uncertain == nil {
		t.Fatal("canceling close discarded or unblocked unknown native write")
	}
	if err := tt.Click(locales.Text("关闭原写入文档")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(locales.Text("关闭并放弃本地草稿与证据")); err != nil {
		t.Fatal(err)
	}
	if closed != 1 || !d.closed || d.nativeReceipt.JSON != "" || d.edit.Uncertain != nil || len(d.edit.Drafts) != 0 {
		t.Fatal("native close retained drafts/evidence or damaged another owner")
	}
}
