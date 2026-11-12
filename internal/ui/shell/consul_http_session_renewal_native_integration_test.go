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
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/ui/kvworkspace"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulNativePublicShellHTTPSessionRenewalOriginalTabsParkingDisconnectAndRecovery(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	p, secret := consulShellGridProfile(t)
	s, tt, p, services := consulShellGridConnect(t, p, secret)
	writer := consulShellMutationSeed(t, s, p)
	request, err := consul.DocumentRequest(consul.Request{Method: "PUT", Path: "/v1/session/create", Body: []byte(`{"Name":"shell-original-renewal","TTL":"10s","LockDelay":"0s","NodeChecks":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := writer.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	out, err := writer.Execute(context.Background(), plan)
	if err != nil || out.HTTP == nil || out.HTTP.SessionID == "" {
		t.Fatal("native shell Session create failed", err)
	}
	id := out.HTTP.SessionID
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("native document unavailable")
	}
	first := s.currentTab()
	d := first.kv
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	found := false
	for _, method := range s.kvWorkspace.MethodsFor(d) {
		if method.Path == kvworkspace.HTTPSessionRenewalMethod {
			found = true
		}
	}
	if !found {
		t.Fatal("Shell public renewal provider missing")
	}
	s.kvWorkspace.ChooseMethod(d, kvworkspace.HTTPSessionRenewalMethod)
	s.kvWorkspace.SetHTTPSessionRenewalInput(d, kvworkspace.HTTPSessionRenewalInput{SessionID: id, Interval: "2s", MaxRenewals: "128", MaxBytes: "268435456"})
	// Each original receipt includes fixed accounting overhead. Reserve enough for
	// screenshots and parked ACK checks; budget exhaustion has separate native tests.
	screenshot := func(stage string) {
		for _, lang := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return lang })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				view := ui.NewTester(func(c *ui.Context) { s.View(c) }, size[0], size[1])
				view.Frame()
				if stage == "running" && d.HTTPSessionRenewalStatus().Ended {
					t.Fatal("renewal ended before explicit stop", d.HTTPSessionRenewalStatus())
				}
				labels := []string{"预览 HTTP Session 续租", "停止 HTTP Session 续租", "Session UUID", "续租间隔"}
				if _, ok := d.HTTPSessionRenewalConfirmation(); ok {
					labels = []string{"确认 HTTP Session 续租", "我授权按以上 UUID、间隔、次数和预算续租", "确认并开始续租", "取消"}
				}
				for _, label := range labels {
					r, ok := view.Find(locales.Text(label))
					if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("Shell renewal control clipped", stage, lang, size, label, r)
					}
				}
				dir := os.Getenv("NYADB_CONSUL_SESSION_RENEWAL_IMAGES")
				if dir == "" {
					continue
				}
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-shell-session-renewal-%s.png", lang, size[0], size[1], stage)))
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
	s.kvWorkspace.PrepareHTTPSessionRenewal(d)
	if _, ok := d.HTTPSessionRenewalConfirmation(); !ok {
		t.Fatal("Shell confirmation missing", d.Error())
	}
	screenshot("confirmation")
	s.kvWorkspace.ConfirmHTTPSessionRenewal(d, false)
	if d.Busy() {
		t.Fatal("Shell bypassed checkbox")
	}
	s.kvWorkspace.ConfirmHTTPSessionRenewal(d, true)
	consulShellGridWait(t, tt, func() bool { return d.HTTPSessionRenewalStatus().Observations >= 2 })
	screenshot("running")
	if !s.OpenKVRequests(p.ID) {
		t.Fatal("second tab unavailable")
	}
	second := s.currentTab()
	consulShellGridWait(t, tt, func() bool { return !second.kv.Busy() })
	before := d.HTTPSessionRenewalStatus()
	s.kvWorkspace.StopHTTPSessionRenewal(d)
	if !d.Busy() {
		t.Fatal("parked stop discarded operation", d.HTTPSessionRenewalStatus())
	}
	s.kvWorkspace.Consume()
	if d.HTTPSessionRenewalStatus() != before || s.currentTab() != second {
		t.Fatal("parked renewal changed active tab")
	}
	s.selectTab(first.id)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	screenshot("stopped")
	original, scope := d.Request(), d.Scope()
	s.kvWorkspace.CopyHTTPSessionRenewalRecord(services, d)
	consulShellGridWait(t, tt, func() bool { return !d.FileBusy() })
	want, err := s.kvWorkspace.HTTPSessionRenewalRecord(d)
	if err != nil || tt.Clipboard() != string(want) {
		t.Fatal("Shell complete copy missing", err)
	}
	s.sessions.Disconnect(p.ID)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	if d.Request() != original || !d.NeedsRecovery() || s.currentTab() != first {
		t.Fatal("disconnect replaced original tab")
	}
	if err := s.runtime.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.kvWorkspace.Recover(d)
	consulShellGridWait(t, tt, func() bool { return !d.Busy() })
	got, err := s.kvWorkspace.HTTPSessionRenewalRecord(d)
	if err != nil || !bytes.Equal(want, got) || d.NeedsRecovery() || !d.HTTPSessionRenewalStatus().Ended || d.Scope().Generation == scope.Generation {
		t.Fatal("Shell recovery restarted or relabeled original", err, d.Error())
	}
	screenshot("recovered")
	// Dispose an inactive original tab without selecting or replacing the second.
	s.selectTab(second.id)
	s.closeTabID(first.id)
	s.kvWorkspace.DecideLeave(ui.Services{}, true)
	if s.currentTab() != second {
		t.Fatal("inactive renewal close changed active tab")
	}
	if _, err := s.kvWorkspace.HTTPSessionRenewalRecord(d); err == nil {
		t.Fatal("closed document retained archive")
	}
	provider, err := s.runtime.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := provider.ReserveKV(9599)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup.Close()
	request, err = consul.DocumentRequest(consul.Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
	if err != nil {
		t.Fatal(err)
	}
	var cleanupPlan contract.KVPreparation
	cleanupPlan, err = cleanup.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cleanup.Execute(context.Background(), cleanupPlan); err != nil {
		t.Fatal(err)
	}
}
