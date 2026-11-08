package connectioneditor

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/meilisearch"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func meilisearchNativeProfileConfig(t *testing.T, name string) (connections.MeilisearchConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_MEILISEARCH_PROFILE_IT") != "1" {
		t.Skip("requires owned native Meilisearch profile fixture")
	}
	raw, err := os.ReadFile(os.Getenv(name))
	if err != nil {
		t.Fatal("private Meilisearch configuration unavailable")
	}
	var probe struct {
		Endpoint, Auth        string
		IndexUID              string `json:"index_uid"`
		ClientCertFile        string `json:"client_cert_file"`
		ClientKeyFile         string `json:"client_key_file"`
		CAFile                string `json:"ca_file"`
		ServerName            string `json:"server_name"`
		PasswordFile          string `json:"key_file"`
		ConnectTimeoutSeconds int    `json:"connect_timeout_seconds"`
		RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("invalid private Meilisearch configuration")
	}
	secret := ""
	if probe.PasswordFile != "" {
		raw, err := os.ReadFile(probe.PasswordFile)
		if err != nil {
			t.Fatal("private Meilisearch credential unavailable")
		}
		secret = string(raw)
	}
	return connections.MeilisearchConfig{Endpoint: probe.Endpoint, Auth: probe.Auth, IndexUID: probe.IndexUID, CAFile: probe.CAFile, ServerName: probe.ServerName, ClientCertFile: probe.ClientCertFile, ClientKeyFile: probe.ClientKeyFile, ConnectTimeoutSeconds: probe.ConnectTimeoutSeconds, RequestTimeoutSeconds: probe.RequestTimeoutSeconds}, secret
}

func meilisearchNativeProfile(t *testing.T, cfg connections.MeilisearchConfig, secret string) connections.URIImport {
	t.Helper()
	u, _ := url.Parse(cfg.Endpoint)
	uri := url.URL{Scheme: "meilisearch", Host: u.Host, Path: u.Path}
	options := url.Values{"auth": {cfg.Auth}, "sslmode": {"disable"}}
	if u.Scheme == "https" {
		options.Set("sslmode", "verify-full")
	}
	if cfg.CAFile != "" {
		options.Set("ca_file", cfg.CAFile)
	}
	if cfg.ServerName != "" {
		options.Set("server_name", cfg.ServerName)
	}
	if cfg.Auth == "bearer" {
		uri.User = url.UserPassword("", secret)
	}
	for name, value := range map[string]string{"index_uid": cfg.IndexUID, "client_cert_file": cfg.ClientCertFile, "client_key_file": cfg.ClientKeyFile} {
		if value != "" {
			options.Set(name, value)
		}
	}
	normalized, _ := connections.NormalizeMeilisearchConfig(cfg)
	options.Set("connect_timeout", strconv.Itoa(normalized.ConnectTimeoutSeconds))
	options.Set("request_timeout", strconv.Itoa(normalized.RequestTimeoutSeconds))
	uri.RawQuery = options.Encode()
	parsed, err := connections.ParseMeilisearchURI(uri.String())
	if err != nil || parsed.Password != secret || parsed.Profile.Username != "" || parsed.Profile.Database != "" {
		t.Fatal("native private URI import changed principal or secret")
	}
	config, err := connections.MeilisearchProfileConfig(parsed.Profile)
	normalized, normalizeErr := connections.NormalizeMeilisearchConfig(cfg)
	if err != nil || normalizeErr != nil || config != normalized {
		t.Fatal("native URI changed original endpoint/TLS context", err)
	}
	if _, err := connections.ParseURI(uri.String()); err == nil {
		t.Fatal("private Meilisearch URI published before provider")
	}
	return parsed
}

func TestMeilisearchNativeProfileURIReopenAndOriginalEditorCheck(t *testing.T) {
	primary, _ := meilisearchNativeProfileConfig(t, "NYADB_MEILISEARCH_CONFIG")
	names := []string{"NYADB_MEILISEARCH_CONFIG"}
	if primary.Auth == "bearer" {
		names = append(names, "NYADB_MEILISEARCH_SCOPED_CONFIG")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			cfg, password := meilisearchNativeProfileConfig(t, name)
			parsed := meilisearchNativeProfile(t, cfg, password)
			path := filepath.Join(t.TempDir(), "config.sqlite")
			db, err := localdb.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if db != nil {
					_ = db.Close()
				}
			}()
			service := connections.NewService(localdb.NewProfileStore(db))
			vault := &memoryCredentialVault{secrets: map[string]string{}}
			var closed atomic.Int32
			probe := func(ctx context.Context, p connections.Profile, secret string) error {
				config, err := connections.MeilisearchProfileConfig(p)
				if err != nil {
					return err
				}
				c, err := meilisearch.Open(ctx, config, secret)
				if err != nil {
					return err
				}
				defer c.Close()
				if config.IndexUID == "" && c.Identity().Version != "1.56.1" || config.IndexUID != "" && c.Identity() != (meilisearch.Identity{}) {
					return meilisearch.ErrProtocol
				}
				c.Close()
				if _, err := c.Do(ctx, meilisearch.Request{Method: "GET", Path: "/"}); !errors.Is(err, meilisearch.ErrRetired) {
					return errors.New("original native probe remained open")
				}
				closed.Add(1)
				return nil
			}
			s := New(Dependencies{Profiles: service, Vault: vault, TestConnection: probe})
			defer s.Close()
			s.OpenProfile(parsed.Profile)
			s.connectionSecret, s.connectionSavePassword = password, cfg.Auth == "bearer"
			s.Save()
			profiles, err := service.List(context.Background())
			if err != nil || len(profiles) != 1 || s.Error() != "" || s.Active() {
				t.Fatal("native private form save failed", s.Error())
			}
			p := profiles[0]
			copy, err := service.Duplicate(context.Background(), p.ID, "native duplicate")
			if err != nil || copy.MeilisearchOptions != p.MeilisearchOptions || copy.Username != p.Username {
				t.Fatal("native duplicate changed options")
			}
			if _, found, _ := vault.Get(copy.ID, ""); found {
				t.Fatal("duplicate copied credentials")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = localdb.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			service = connections.NewService(localdb.NewProfileStore(db))
			p, err = service.Get(context.Background(), p.ID)
			if err != nil || p.Username != "" || p.MeilisearchOptions != parsed.Profile.MeilisearchOptions {
				t.Fatal("native restart changed public settings")
			}
			s = New(Dependencies{Profiles: service, Vault: vault, TestConnection: probe})
			defer s.Close()
			s.OpenProfile(p)
			if s.connectionSecret != password || s.connectionSavePassword != (cfg.Auth == "bearer") {
				t.Fatal("native restart failed separate credential restoration")
			}
			tt := meilisearchEditorTester(s)
			if err := tt.Click("测试连接"); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(20 * time.Second)
			for s.connectionTesting && time.Now().Before(deadline) {
				tt.Frame()
				time.Sleep(5 * time.Millisecond)
			}
			if s.connectionTesting || s.Error() != "" || closed.Load() != 1 || !strings.Contains(s.connectionTestMessage, "已关闭测试会话") {
				t.Fatal("native check did not close original handle", s.Error())
			}
			public, _ := json.Marshal(p)
			if password != "" && strings.Contains(string(public), password) {
				t.Fatal("credential entered public profile")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			config, _ := connections.MeilisearchProfileConfig(p)
			c, err := meilisearch.Open(ctx, config, password)
			if err != nil {
				t.Fatal("independent saved-profile open failed", err)
			}
			defer c.Close()
			var fixture struct {
				IndexUID string `json:"fixture_index_uid"`
			}
			raw, readErr := os.ReadFile(os.Getenv(name))
			if readErr != nil || json.Unmarshal(raw, &fixture) != nil || fixture.IndexUID == "" {
				t.Fatal("seeded native index unavailable")
			}
			result, _, searchErr := c.Search(ctx, fixture.IndexUID, []byte(`{"q":"","limit":1}`))
			if searchErr != nil || len(result.Hits) != 1 || !strings.Contains(string(result.Hits[0]), "9007199254740993") {
				t.Fatal("saved original profile lost exact native search value", searchErr)
			}

		})
	}
}

func TestMeilisearchNativeProfileProxyContextAndCanceledOriginalCheck(t *testing.T) {
	cfg, password := meilisearchNativeProfileConfig(t, "NYADB_MEILISEARCH_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	upstream, err := meilisearch.Open(ctx, cfg, password)
	if err != nil {
		t.Fatal("original native upstream open failed", err)
	}
	defer upstream.Close()
	entered, acknowledged, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.RequestURI(), "/gateway/meilisearch")
		if path == r.URL.RequestURI() {
			w.WriteHeader(400)
			return
		}
		response, err := upstream.Do(r.Context(), meilisearch.Request{Method: r.Method, Path: path})
		if err != nil {
			w.WriteHeader(502)
			return
		}
		if path == "/version" {
			requests.Add(1)
			close(entered)
			<-r.Context().Done()
			close(acknowledged)
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "relay-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Endpoint, cfg.CAFile, cfg.ServerName = server.URL+"/gateway/meilisearch", ca, "example.com"
	cfg.ClientCertFile, cfg.ClientKeyFile = "", ""
	p := meilisearchNativeProfile(t, cfg, password).Profile
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		defer close(closed)
		config, err := connections.MeilisearchProfileConfig(p)
		if err != nil {
			return err
		}
		c, err := meilisearch.Open(ctx, config, secret)
		if c != nil {
			c.Close()
		}
		return err
	}})
	defer s.Close()
	s.OpenProfile(p)
	s.connectionSecret = password
	tt := meilisearchEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("native handshake did not reach original proxy context")
	}
	other := "none"
	if cfg.Auth == "none" {
		other = "bearer"
	}
	chooseMeilisearchAuth(t, tt, other)
	for _, done := range []<-chan struct{}{acknowledged, closed} {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("original native check did not acknowledge cancellation/close")
		}
	}
	tt.Frame()
	if s.connectionTesting || s.connectionTestResults != nil || s.Error() != "" || s.connectionSecret != "" || requests.Load() != 1 {
		t.Fatal("late original result entered changed authentication draft")
	}
	if _, err := upstream.Do(ctx, meilisearch.Request{Method: "GET", Path: "/"}); err != nil {
		t.Fatal("old check closed independent original client")
	}
}

func TestMeilisearchNativeEditorLocalizedFormEvidence(t *testing.T) {
	cfg, _ := meilisearchNativeProfileConfig(t, "NYADB_MEILISEARCH_CONFIG")
	u, _ := url.Parse(cfg.Endpoint)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{900, 700}, {720, 520}} {
			for _, auth := range []string{"none", "bearer"} {
				s := New(Dependencies{Vault: &memoryCredentialVault{secrets: map[string]string{}}})
				if err := s.OpenNew(connections.Meilisearch, ""); err != nil {
					t.Fatal(err)
				}
				s.connectionDraft.Name, s.connectionDraft.Host = "Meilisearch", u.Hostname()
				s.connectionPort = u.Port()
				s.engineNumbers[0], s.engineNumbers[1] = strconv.Itoa(cfg.ConnectTimeoutSeconds), strconv.Itoa(cfg.RequestTimeoutSeconds)
				if u.Scheme == "https" {
					s.connectionDraft.TLSMode = "verify-full"
					s.connectionDraft.MeilisearchOptions.CAFile = cfg.CAFile
					s.connectionDraft.MeilisearchOptions.ServerName = cfg.ServerName
					s.connectionDraft.MeilisearchOptions.ClientCertFile = cfg.ClientCertFile
					s.connectionDraft.MeilisearchOptions.ClientKeyFile = cfg.ClientKeyFile
				}
				tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, float32(size[0]), float32(size[1])) }, size[0], size[1])
				if auth == "bearer" {
					if err := tt.Click(locales.Text("Meilisearch 认证方式")); err != nil {
						t.Fatal(err)
					}
					if err := tt.Click(locales.Text("Bearer API key")); err != nil {
						t.Fatal(err)
					}
					// A non-sensitive example proves the visible field is masked;
					// actual fixture credentials are exercised in the reopen test.
					s.connectionSecret = "example-form-key"
					tt.Frame()
					if tt.HasText("example-form-key") {
						t.Fatal("credential preview was not masked")
					}
				}
				if s.connectionDraft.MeilisearchOptions.Auth != auth {
					t.Fatal("localized auth changed stable native value")
				}
				for _, name := range []string{"Meilisearch 服务路径", "Meilisearch 索引范围", "Meilisearch 认证方式", "保存连接", "取消连接向导"} {
					assertMeilisearchControlVisible(t, tt, name, size)
				}
				prefix := language + "-" + strconv.Itoa(size[0]) + "-" + auth
				meilisearchFormImage(t, tt, prefix+"-general")
				if err := tt.Click(locales.Text("高级")); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"Meilisearch 连接超时", "Meilisearch 请求超时", "保存连接", "取消连接向导"} {
					assertMeilisearchControlVisible(t, tt, name, size)
				}
				meilisearchFormImage(t, tt, prefix+"-advanced")
				if err := tt.Click("SSL"); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"Meilisearch TLS 模式", "Meilisearch CA 证书", "证书服务器名", "Meilisearch 客户端证书", "Meilisearch 客户端私钥", "保存连接", "取消连接向导"} {
					assertMeilisearchControlVisible(t, tt, name, size)
				}
				meilisearchFormImage(t, tt, prefix+"-ssl")
				s.Close()
			}
		}
	}
}

func assertMeilisearchControlVisible(t *testing.T, tt *ui.Tester, name string, size [2]int) {
	t.Helper()
	r, ok := tt.Find(locales.Text(name))
	if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
		t.Fatal("localized control clipped", name)
	}
}

func meilisearchFormImage(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	folder := os.Getenv("NYADB_MEILISEARCH_PROFILE_IMAGES")
	if folder == "" {
		return
	}
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(folder, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(file, tt.Image())
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal("form evidence encoding failed")
	}
}
