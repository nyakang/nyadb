package shell

import (
	"bytes"
	"context"
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

func TestConsulNativePublicShellSessionHeldKeysOriginalSelectionParkingReceiptsAndRecovery(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	s, tt, p, _ := consulShellGridConnect(t, p, secret)
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("original Shell Session document unavailable")
	}
	first := s.currentTab()
	d := first.kv
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	runTool := func(kind contract.KVRequestToolKind, input contract.KVRequestToolInput, write bool) {
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
	runTool(contract.KVToolHTTPSessionCreate, contract.KVRequestToolInput{HTTPSession: contract.KVHTTPSessionToolInput{IncludeName: true, Name: t.Name(), TTL: "10m", NodeChecksJSON: "[]", LockDelay: "0s"}}, true)
	created, ok := d.LastReceipt()
	if !ok || created.Result.HTTP == nil || created.Result.HTTP.SessionID == "" {
		t.Fatal("original Shell create UUID missing", d.Error())
	}
	id := created.Result.HTTP.SessionID
	key := "outside-shell-held/" + t.Name() + "/key"
	runTool(contract.KVToolHTTPAcquire, contract.KVRequestToolInput{Key: key, ValueBase64: "AP+A", Flags: "18446744073709551615", HTTPSession: contract.KVHTTPSessionToolInput{ID: id}}, true)
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("second original Shell document unavailable")
	}
	second := s.currentTab()
	consulShellGridWait(t, tt, func() bool { return !second.kv.Busy() })
	s.selectTab(first.id)
	tt.Frame()
	if err := tt.Click(locales.Text("KV 请求工具")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem(locales.Text("列出 HTTP Sessions")); err != nil {
		t.Fatal("original Session read menu missing", err)
	}
	s.kvWorkspace.GenerateRequestTool(d)
	s.kvWorkspace.Run(d)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	tt.Frame()
	if err := tt.Click(locales.Text("Session 列表")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(locales.Text("筛选已返回的 Session")); err != nil {
		t.Fatal(err)
	}
	tt.Type(id)
	tt.Frame()
	if err := tt.Click(id); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
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
						t.Fatal("original Shell held-key control clipped", stage, language, size, label, r)
					}
				}
				if dir := os.Getenv("NYADB_CONSUL_SESSION_HELD_KEYS_IMAGES"); dir != "" {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-shell-held-keys-%s.png", language, size[0], size[1], stage)))
					if err != nil {
						t.Fatal(err)
					}
					err, closeErr := png.Encode(f, view.Image()), f.Close()
					if err != nil || closeErr != nil {
						t.Fatal(err, closeErr)
					}
				}
			}
		}
		i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	}
	capture("selected-session", "读取所选 Session 的持有键（全根目录）", "返回原 HTTP 回执")
	request := d.Request()
	s.kvWorkspace.ObserveSelectedHTTPSessionKeys(d)
	if !d.Busy() {
		t.Fatal("explicit original held-key read did not enter executor lifecycle")
	}
	s.selectTab(second.id)
	for range 3 {
		tt.Frame()
	}
	if !d.Busy() || s.currentTab() != second {
		t.Fatal("parked held-key completion selected or consumed another document")
	}
	s.selectTab(first.id)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	observed, ok := d.LastReceipt()
	if !ok || observed.HTTPSessionKeys == nil || !observed.HTTPSessionKeys.Complete || len(observed.HTTPSessionKeys.Matches) != 1 || observed.HTTPSessionKeys.Request.ID != id || d.Request() != request || observed.Result.HTTP.Entries[observed.HTTPSessionKeys.Matches[0]].Key != key {
		t.Fatal("Shell routed original held-key UUID/evidence to another document", d.Error())
	}
	capture("list", "筛选已返回的持有键", "查看所选原始详情", "返回原 HTTP 回执")
	tt.Frame()
	if err := tt.Click(key); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(locales.Text("查看所选原始详情")); err != nil {
		t.Fatal(err)
	}
	capture("detail", "返回持有键列表", "持有键原始详情", "返回原 HTTP 回执")
	tt.Frame()
	if err := tt.Click(locales.Text("返回原 HTTP 回执")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(locales.Text("复制完整操作记录")); err != nil {
		t.Fatal(err)
	}
	consulShellGridWait(t, tt, func() bool { return !d.FileBusy() })
	before, err := s.kvWorkspace.OperationRecord(d)
	if err != nil || tt.Clipboard() != string(before) {
		t.Fatal("Shell copied a different original complete held-key receipt", err)
	}
	capture("receipt", "复制完整操作记录")
	originalGeneration := d.Scope().Generation
	s.sessions.Disconnect(p.ID)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if !d.NeedsRecovery() || d.Request() != request {
		t.Fatal("Shell disconnect discarded original read text or receipt")
	}
	if err := s.runtime.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.kvWorkspace.Recover(d)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	after, err := s.kvWorkspace.OperationRecord(d)
	if err != nil || d.NeedsRecovery() || d.Scope().Generation == originalGeneration || !bytes.Equal(before, after) {
		t.Fatal("Shell recovery replayed or relabeled original held-key evidence", err)
	}
	capture("recovered", "运行 KV 请求", "复制完整操作记录")
	s.selectTab(second.id)
	s.closeTabID(first.id)
	s.kvWorkspace.DecideLeave(ui.Services{}, true)
	if s.currentTab() != second || d.Editor != nil {
		t.Fatal("inactive held-key close retained resources or changed active document")
	}
}
