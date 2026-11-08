package shell

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/meilisearch"
	"github.com/nyakang/nyadb/locales"
)

func keyPanelRelay(t *testing.T, parsed connections.URIImport, after func(http.ResponseWriter, *http.Request, meilisearch.Response) bool) connections.Profile {
	t.Helper()
	cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
	upstream, err := meilisearch.Open(context.Background(), cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(upstream.Close)
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(io.LimitReader(r.Body, (8<<20)+1))
		if e != nil {
			w.WriteHeader(502)
			return
		}
		response, e := upstream.Do(r.Context(), meilisearch.Request{Method: r.Method, Path: r.URL.RequestURI(), Body: body})
		if e != nil && response.Status == 0 {
			w.WriteHeader(502)
			return
		}
		if after(w, r, response) {
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	t.Cleanup(relay.Close)
	ca := filepath.Join(t.TempDir(), "original-key-relay.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint, _ := url.Parse(relay.URL)
	port, _ := strconv.Atoi(endpoint.Port())
	p := parsed.Profile
	p.Host, p.Port, p.TLSMode = endpoint.Hostname(), uint16(port), "verify-full"
	p.MeilisearchOptions.ContextPath, p.MeilisearchOptions.CAFile, p.MeilisearchOptions.ServerName = "", ca, "example.com"
	p.MeilisearchOptions.ClientCertFile, p.MeilisearchOptions.ClientKeyFile = "", ""
	return p
}
func keyPanelAwait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("original native key request not acknowledged")
	}
}
func TestMeilisearchNativePublicKeysReadParkingCancelAndRecovery(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	entered, delivered, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	canceling, acknowledged, releaseCancel := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	var once, onceCancel sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	unblockCancel := func() { onceCancel.Do(func() { close(releaseCancel) }) }
	defer unblock()
	defer unblockCancel()
	p := keyPanelRelay(t, parsed, func(w http.ResponseWriter, r *http.Request, response meilisearch.Response) bool {
		if r.Method != "GET" || r.URL.Path != "/keys" {
			return false
		}
		switch reads.Add(1) {
		case 1:
			close(entered)
			<-release
			w.Header().Set("Content-Type", response.ContentType)
			w.WriteHeader(response.Status)
			_, _ = w.Write(response.Body)
			close(delivered)
			return true
		case 2:
			close(canceling)
			<-r.Context().Done()
			close(acknowledged)
			<-releaseCancel
			return true
		}
		return false
	})
	s, tt, profiles, p, services := keyPanelState(t, p, parsed.Password)
	other := p
	other.ID, other.Name = 0, "Parked original key profile"
	other, err := profiles.Create(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	// Open without waiting so that the captured read can be parked first.
	s.openKeys(*services, p.ID)
	d := s.currentTab().object
	owner := s.currentTab()
	keyPanelAwait(t, entered)
	s.selectProfileByName(other.Name)
	unblock()
	keyPanelAwait(t, delivered)
	for range 10 {
		tt.Frame()
	}
	if !d.Loading() || s.objectWorkspace.KeysPage(d).Raw != "" || s.selectedProfileID != other.ID {
		t.Fatal("original parked key read was consumed by another profile")
	}
	s.selectProfileByName(p.Name)
	s.selectTab(owner.id)
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if reads.Load() != 1 || parsed.Password != "" && s.objectWorkspace.KeysPage(d).Raw == "" || parsed.Password == "" && d.Error() == "" {
		t.Fatal("parked original key response lost or replayed")
	}
	if err = keyPanelAction(tt, "刷新密钥列表", false); err != nil {
		t.Fatal(err)
	}
	keyPanelAwait(t, canceling)
	s.objectWorkspace.CancelKeys(d)
	if !d.Loading() {
		t.Fatal("public cancel returned before original executor acknowledgement")
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	keyPanelAwait(t, acknowledged)
	unblockCancel()
	before := reads.Load()
	if err = keyPanelAction(tt, "恢复密钥连接", false); err != nil {
		t.Fatal(err)
	}
	if d.Loading() || reads.Load() != before {
		t.Fatal("public key recovery automatically read keys")
	}
	if err = keyPanelAction(tt, "刷新密钥列表", false); err != nil {
		t.Fatal(err)
	}
	taskPanelWait(t, tt, func() bool { return !d.Loading() })
	if reads.Load() != 3 {
		t.Fatal("explicit key recovery read did not use original profile")
	}
	s.closeTabID(owner.id)
	tt.Frame()
	if d.KeysMode() || !s.runtime.IsConnected(p.ID) {
		t.Fatal("original key close damaged replacement executor/session")
	}
	t.Log("public parked original-profile key responses wait activation, cancellation waits original executor acknowledgement, recovery transfers cache without key I/O, explicit read uses original scope and close releases original document")
}
func TestMeilisearchNativePublicKeysUnknownWriteParkingCancelAndRecovery(t *testing.T) {
	parsed := meilisearchPublicFixture(t, "NYADB_MEILISEARCH_CONFIG")
	for _, fault := range []string{"parked", "canceled", "lost", "closed"} {
		t.Run(fault, func(t *testing.T) {
			entered, delivered, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var writes atomic.Int32
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			p := keyPanelRelay(t, parsed, func(w http.ResponseWriter, r *http.Request, response meilisearch.Response) bool {
				if r.Method != "POST" || r.URL.Path != "/keys" {
					return false
				}
				writes.Add(1)
				if response.Status != 201 {
					t.Error("real native synchronous key creation not demonstrated")
				}
				close(entered)
				if fault == "lost" {
					conn, _, e := w.(http.Hijacker).Hijack()
					if e == nil {
						_ = conn.Close()
					}
					close(delivered)
					return true
				}
				if fault == "canceled" || fault == "closed" {
					<-r.Context().Done()
					<-release
					return true
				}
				<-release
				w.Header().Set("Content-Type", response.ContentType)
				w.WriteHeader(response.Status)
				_, _ = w.Write(response.Body)
				close(delivered)
				return true
			})
			s, tt, profiles, p, services := keyPanelState(t, p, parsed.Password)
			d := keyPanelOpen(t, s, tt)
			owner := s.currentTab()
			other := p
			other.ID, other.Name = 0, "Parked key mutation target"
			other, e := profiles.Create(context.Background(), other)
			if e != nil {
				t.Fatal(e)
			}
			s.loadProfiles()
			if e = keyPanelAction(tt, "新建 API 密钥", false); e != nil {
				t.Fatal(e)
			}
			name := "owned_once_" + fault + "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
			taskPanelField(t, tt, "密钥名称", name)
			if e = tt.Click(locales.Text("预览密钥更改")); e != nil {
				t.Fatal(e)
			}
			if e = tt.Click(locales.Text("确认提交密钥操作")); e != nil {
				t.Fatal(e)
			}
			keyPanelAwait(t, entered)
			switch fault {
			case "parked":
				s.selectProfileByName(other.Name)
				unblock()
				keyPanelAwait(t, delivered)
				for range 10 {
					tt.Frame()
				}
				if !d.Loading() || s.objectWorkspace.KeyResult(d).State != "" || s.selectedProfileID != other.ID {
					t.Fatal("parked original key write delivered into different profile")
				}
				s.selectProfileByName(p.Name)
				s.selectTab(owner.id)
			case "canceled":
				s.objectWorkspace.CancelKeys(d)
				if !d.Loading() {
					t.Fatal("public key mutation cancel cleared busy before executor acknowledgement")
				}
			case "closed":
				// Close the captured real session while keeping its document for
				// outcome settlement, exactly as an unexpected disconnect does.
				s.runtime.Disconnect(p.ID)
			}
			taskPanelWait(t, tt, func() bool { return !d.Loading() })
			unblock()
			result := s.objectWorkspace.KeyResult(d)
			if fault == "parked" {
				if result.State != "completed" || result.Status != 201 || d.DesignDirty() {
					t.Fatal("parked synchronous creation lost original completion", d.Error())
				}
			} else if result.State != "uncertain" || result.Request == "" || !d.DesignDirty() || !s.objectWorkspace.KeysUncertain(d) {
				t.Fatal("public lost/canceled/closed key mutation lost unknown request/draft", d.Error(), result.State)
			}
			if writes.Load() != 1 {
				t.Fatal("original key mutation replayed")
			}
			if fault == "closed" {
				if e = tt.Click(locales.Text("继续处理密钥")); e != nil {
					t.Fatal(e)
				}
				fresh, e := s.runtime.ConnectSession(context.Background(), p, parsed.Password)
				if e != nil {
					t.Fatal(e)
				}
				s.sessions.Current().ProfileID, s.sessions.Current().Status, s.sessions.Current().Generation = p.ID, "已连接", fresh.Generation()
			}
			if e = keyPanelAction(tt, "恢复密钥连接", false); e != nil {
				t.Fatal(e)
			}
			if d.Loading() || writes.Load() != 1 || fault != "parked" && s.objectWorkspace.CanCreateKey(d) {
				t.Fatal("public explicit recovery replayed or enabled unknown write")
			}
			if e = keyPanelAction(tt, "复制密钥请求", true); e != nil {
				t.Fatal(e)
			}
			if tt.Clipboard() != result.Request {
				t.Fatal("unknown original key request copy changed evidence")
			}
			services.WriteClipboard("[redacted]")
			keyPanelAssertNoSecret(t, s, d, parsed.Password)
			// Independent original-native enumeration proves one creation;
			// the recovered production document does not issue this read.
			cfg, _ := connections.MeilisearchProfileConfig(parsed.Profile)
			observer, e := meilisearch.Open(context.Background(), cfg, parsed.Password)
			if e != nil {
				t.Fatal(e)
			}
			count := 0
			for offset := 0; ; offset += 100 {
				fields := keyPanelNative(t, observer, "GET", "/keys?offset="+strconv.Itoa(offset)+"&limit=100", nil)
				var rows []map[string]any
				var total int
				if json.Unmarshal(fields["results"], &rows) != nil || json.Unmarshal(fields["total"], &total) != nil {
					t.Fatal("independent native key enumeration unavailable")
				}
				for _, row := range rows {
					if row["name"] == name {
						count++
					}
				}
				if offset+len(rows) >= total {
					break
				}
			}
			observer.Close()
			if count != 1 {
				t.Fatal("independent original-native observer did not confirm exactly one key")
			}
			if fault != "parked" {
				if e = tt.Click(locales.Text("密钥草稿")); e != nil {
					t.Fatal(e)
				}
				if !strings.Contains(s.objectWorkspace.KeyResult(d).Request, name) {
					t.Fatal("unknown original request lost immutable creation intent")
				}
				if e = tt.Click(locales.Text("放弃密钥草稿")); e != nil {
					t.Fatal(e)
				}
				if e = keyPanelAction(tt, "刷新密钥列表", false); e != nil {
					t.Fatal(e)
				}
				taskPanelWait(t, tt, func() bool { return !d.Loading() })
				if s.objectWorkspace.CanCreateKey(d) || writes.Load() != 1 {
					t.Fatal("current explicit key read enabled unknown replay")
				}
			}
		})
	}
	t.Log("public synchronous key write parks with original profile; lost/canceled/session-close writes settle once, keep draft/request/uncertainty, explicit recovery and reads never replay, independent native observer proves one key")
}
