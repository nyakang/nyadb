package shell

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulNativePublicShellSessionDestroyOriginalConfirmationParkingAndReceipts(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	s, tt, p, _ := consulShellGridConnect(t, p, secret)
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("original Shell KV document unavailable")
	}
	first := s.currentTab()
	d := first.kv
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	run := func(kind contract.KVRequestToolKind, input contract.KVRequestToolInput, write bool) {
		t.Helper()
		s.kvWorkspace.OpenRequestTool(d, kind)
		s.kvWorkspace.SetRequestToolInput(d, kind, input)
		s.kvWorkspace.GenerateRequestTool(d)
		s.kvWorkspace.Run(d)
		if write {
			s.kvWorkspace.Confirm(d, true)
		}
		consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	}
	run(contract.KVToolHTTPSessionCreate, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{IncludeName: true, Name: t.Name(), TTL: "10m", NodeChecksJSON: "[]", LockDelay: "0s", Behavior: "delete"}}, true)
	created, ok := d.LastReceipt()
	if !ok || created.Result.HTTP == nil || created.Result.HTTP.SessionID == "" {
		t.Fatal("original native UUID missing", d.Error())
	}
	id := created.Result.HTTP.SessionID
	key := "outside-shell-destroy/" + t.Name() + "/key"
	run(contract.KVToolHTTPAcquire, contract.KVRequestToolInput{Key: key, ValueBase64: "AP+A", Flags: "18446744073709551615", HTTPSession: contract.KVHTTPSessionToolInput{ID: id}}, true)
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("second Shell document unavailable")
	}
	second := s.currentTab()
	consulShellGridWait(t, tt, func() bool { return !second.kv.Busy() })
	s.selectTab(first.id)
	run(contract.KVToolHTTPSessionList, contract.KVRequestToolInput{}, false)
	tt.Frame()
	for _, label := range []string{"Session 列表", "筛选已返回的 Session"} {
		if err := tt.Click(locales.Text(label)); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	tt.Type(id)
	tt.Frame()
	if err := tt.Click(id); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !d.HTTPSessionDestroyAvailable() {
		t.Fatal("Shell original lease lost planning facet")
	}
	original := d.Request()
	s.kvWorkspace.PrepareSelectedHTTPSessionDestroy(d)
	s.selectTab(second.id)
	for range 3 {
		tt.Frame()
	}
	if !d.Busy() || s.currentTab() != second {
		t.Fatal("parked preview selected/consumed another document")
	}
	s.selectTab(first.id)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	preview, ok := d.LastReceipt()
	if !ok || preview.Result.HTTPSessionDestroy == nil || preview.Result.HTTPSessionDestroy.Preview.Request.ID != id || len(preview.Result.HTTPSessionDestroy.Preview.Keys.Matches) != 1 || d.Request() != original {
		t.Fatal("original Shell preview lost identity", d.Error())
	}
	capture := func(stage string, labels ...string) {
		t.Helper()
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				view := ui.NewTester(func(c *ui.Context) { s.View(c) }, size[0], size[1])
				view.Frame()
				for _, label := range labels {
					r, found := view.Find(locales.Text(label))
					if !found || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("Shell destroy control clipped", stage, language, size, label, r)
					}
				}
				if dir := os.Getenv("NYADB_CONSUL_SESSION_DESTROY_IMAGES"); dir != "" {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-shell-destroy-%s.png", language, size[0], size[1], stage)))
					if err != nil {
						t.Fatal(err)
					}
					err = png.Encode(f, view.Image())
					closeErr := f.Close()
					if err != nil || closeErr != nil {
						t.Fatal(err, closeErr)
					}
				}
			}
		}
		i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	}
	capture("confirm", "确认销毁原 Session", "我确认销毁此原 Session", "确认销毁 Session")
	s.kvWorkspace.Confirm(d, true)
	s.selectTab(second.id)
	for range 3 {
		tt.Frame()
	}
	if !d.Busy() || s.currentTab() != second {
		t.Fatal("parked original executor changed active document")
	}
	s.selectTab(first.id)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	result, ok := d.LastReceipt()
	if !ok || result.Result.HTTP == nil || result.Result.WriteOutcomeUnknown || !bytes.Equal(bytes.TrimSpace(result.Result.HTTP.Body), []byte("true")) || result.Result.HTTPSessionDestroy == nil || !result.Result.HTTPSessionDestroy.Revalidated || result.Result.HTTPSessionDestroy.Preview.Request.ID != id {
		t.Fatal("Shell lost original complete PUT evidence", d.Error())
	}
	capture("put", "Session 销毁原始记录", "选择原始步骤回执", "复制完整操作记录")
	before, err := s.kvWorkspace.OperationRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(locales.Text("复制完整操作记录")); err != nil {
		t.Fatal(err)
	}
	consulShellGridWait(t, tt, func() bool { return !d.FileBusy() })
	if tt.Clipboard() != string(before) {
		t.Fatal("Shell copied different original destroy record")
	}
}
