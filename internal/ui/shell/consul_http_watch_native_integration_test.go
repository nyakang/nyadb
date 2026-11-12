package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulNativePublicShellHTTPWatchOriginalTabsRecordsParkingDisconnectAndRecovery(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	s, tt, p, services := consulShellGridConnect(t, p, secret)
	writer := consulShellMutationSeed(t, s, p)
	key := "native/shell-watch/" + t.Name() + "/key"
	put, _ := consul.KeyPutRequest(key, []byte{0, 255}, nil, nil)
	request, err := consul.DocumentRequest(put)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := writer.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Execute(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("native document unavailable")
	}
	first := s.currentTab()
	d := first.kv
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	methods := s.kvWorkspace.MethodsFor(d)
	found := false
	for _, m := range methods {
		if m.Path == kvworkspace.HTTPWatchMethod {
			found = true
		}
	}
	if !found {
		t.Fatal("public Watch method unavailable")
	}
	s.kvWorkspace.ChooseMethod(d, kvworkspace.HTTPWatchMethod)
	raw, err := json.Marshal(map[string]any{"target": key, "prefix": false, "observation_prefix": "native/shell-watch/" + t.Name(), "start_index": "0", "wait": "1s", "max_observations": 128, "max_bytes": "268435456"})
	if err != nil {
		t.Fatal(err)
	}
	d.Editor.Buffer.Set(string(raw))
	s.kvWorkspace.Run(d)
	consulShellGridWait(t, tt, func() bool { return d.HTTPWatchStatus().Observations > 0 })
	// Lifecycle screenshots must not exhaust the separately tested receipt budget.
	screenshot := func(stage string) {
		for _, lang := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return lang })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				view := ui.NewTester(func(c *ui.Context) { s.View(c) }, size[0], size[1])
				view.Frame()
				if stage == "observing" && d.HTTPWatchStatus().Ended {
					t.Fatal("Watch ended before explicit stop", d.HTTPWatchStatus())
				}
				for _, label := range []string{"启动 HTTP Watch", "停止 HTTP Watch", "Watch 原目标", "Watch 实际范围"} {
					r, ok := view.Find(locales.Text(label))
					if !ok || r.W <= 0 || r.H <= 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("Shell Watch control clipped", stage, lang, size, label, r)
					}
				}
				folder := os.Getenv("NYADB_CONSUL_WATCH_IMAGES")
				if folder == "" {
					continue
				}
				if err := os.MkdirAll(folder, 0700); err != nil {
					t.Fatal(err)
				}
				f, err := os.Create(filepath.Join(folder, fmt.Sprintf("%s-%dx%d-shell-watch-%s.png", lang, size[0], size[1], stage)))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(f, view.Image())
				ce := f.Close()
				if err != nil || ce != nil {
					t.Fatal(err, ce)
				}
			}
		}
		i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	}
	screenshot("observing")
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("second original tab unavailable")
	}
	second := s.currentTab()
	consulShellGridWait(t, tt, func() bool { return !second.kv.Busy() })
	before := d.HTTPWatchStatus().Observations
	s.kvWorkspace.StopHTTPWatch(d)
	if !d.Busy() {
		t.Fatal("parked stop dropped original operation", d.HTTPWatchStatus())
	}
	s.kvWorkspace.Consume()
	if d.HTTPWatchStatus().Observations != before || s.currentTab() != second {
		t.Fatal("parked original result changed active tab")
	}
	s.selectTab(first.id)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	screenshot("stopped")
	s.kvWorkspace.CopyHTTPWatchRecord(services, d)
	consulShellGridWait(t, tt, func() bool { return !d.FileBusy() })
	if !bytes.Contains([]byte(tt.Clipboard()), []byte("original_http")) {
		t.Fatal("Shell complete clipboard missing")
	}
	text := d.Request()
	original := d.Scope()
	s.sessions.Disconnect(p.ID)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if d.Request() != text || !d.NeedsRecovery() || s.currentTab() != first {
		t.Fatal("disconnect rewrote original tab")
	}
	if err := s.runtime.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.kvWorkspace.Recover(d)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if d.NeedsRecovery() || !d.HTTPWatchStatus().Ended || d.Request() != text || d.Scope().Generation == original.Generation {
		t.Fatal("recovery replayed or did not bind fresh lease", d.Error())
	}
	screenshot("recovered")
	raw, err = s.kvWorkspace.HTTPWatchRecord(d)
	if err != nil || !bytes.Contains(raw, []byte("AP8=")) {
		t.Fatal("recovery lost original bytes", err)
	}
}
