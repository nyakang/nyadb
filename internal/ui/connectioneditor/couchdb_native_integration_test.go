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
	"github.com/nyakang/nyadb/internal/services/couchdb"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func couchdbNativeProfileConfig(t *testing.T, name string) (connections.CouchDBConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_COUCHDB_PROFILE_IT") != "1" {
		t.Skip("requires owned native CouchDB profile fixture")
	}
	raw, err := os.ReadFile(os.Getenv(name))
	if err != nil {
		t.Fatal("private CouchDB configuration unavailable")
	}
	var probe struct {
		Endpoint, Auth, Username string
		CAFile                   string `json:"ca_file"`
		ServerName               string `json:"server_name"`
		PasswordFile             string `json:"password_file"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("invalid private CouchDB configuration")
	}
	secret := ""
	if probe.PasswordFile != "" {
		raw, err := os.ReadFile(probe.PasswordFile)
		if err != nil {
			t.Fatal("private CouchDB credential unavailable")
		}
		secret = string(raw)
	}
	return connections.CouchDBConfig{Endpoint: probe.Endpoint, Auth: probe.Auth, Username: probe.Username, CAFile: probe.CAFile, ServerName: probe.ServerName}, secret
}

func couchdbNativeProfile(t *testing.T, cfg connections.CouchDBConfig, secret string) connections.URIImport {
	t.Helper()
	u, _ := url.Parse(cfg.Endpoint)
	uri := url.URL{Scheme: "couchdb", Host: u.Host, Path: u.Path}
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
	if cfg.Auth == "basic" {
		uri.User = url.UserPassword(cfg.Username, secret)
	}
	uri.RawQuery = options.Encode()
	parsed, err := connections.ParseCouchDBURI(uri.String())
	if err != nil || parsed.Password != secret || parsed.Profile.Username != cfg.Username || parsed.Profile.Database != "" {
		t.Fatal("native private URI import changed principal or secret")
	}
	config, err := connections.CouchDBProfileConfig(parsed.Profile)
	if err != nil || config != cfg {
		t.Fatal("native URI changed original endpoint/TLS context", err)
	}
	if _, err := connections.ParseURI(uri.String()); err != nil {
		t.Fatal("public URI rejected CouchDB")
	}
	return parsed
}

func TestCouchDBNativeProfileURIReopenAndOriginalEditorCheck(t *testing.T) {
	for _, name := range []string{"NYADB_COUCHDB_CONFIG", "NYADB_COUCHDB_PADDED_CONFIG"} {
		t.Run(name, func(t *testing.T) {
			cfg, password := couchdbNativeProfileConfig(t, name)
			parsed := couchdbNativeProfile(t, cfg, password)
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
				config, err := connections.CouchDBProfileConfig(p)
				if err != nil {
					return err
				}
				c, err := couchdb.Open(ctx, config, secret)
				if err != nil {
					return err
				}
				defer c.Close()
				if c.Identity().Version != "3.5.3" || config.Auth == "basic" && (c.Principal().Name == nil || *c.Principal().Name != p.Username) {
					return couchdb.ErrProtocol
				}
				c.Close()
				if _, err := c.Do(ctx, couchdb.Request{Method: "GET", Path: "/"}); !errors.Is(err, couchdb.ErrRetired) {
					return errors.New("original native probe remained open")
				}
				closed.Add(1)
				return nil
			}
			s := New(Dependencies{Profiles: service, Vault: vault, TestConnection: probe})
			defer s.Close()
			s.OpenProfile(parsed.Profile)
			s.connectionSecret, s.connectionSavePassword = password, cfg.Auth == "basic"
			s.Save()
			profiles, err := service.List(context.Background())
			if err != nil || len(profiles) != 1 || s.Error() != "" || s.Active() {
				t.Fatal("native private form save failed", s.Error())
			}
			p := profiles[0]
			copy, err := service.Duplicate(context.Background(), p.ID, "native duplicate")
			if err != nil || copy.CouchDBOptions != p.CouchDBOptions || copy.Username != p.Username {
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
			if err != nil || p.Username != cfg.Username || p.CouchDBOptions != parsed.Profile.CouchDBOptions {
				t.Fatal("native restart changed public settings")
			}
			s = New(Dependencies{Profiles: service, Vault: vault, TestConnection: probe})
			defer s.Close()
			s.OpenProfile(p)
			if s.connectionSecret != password || s.connectionSavePassword != (cfg.Auth == "basic") {
				t.Fatal("native restart failed separate credential restoration")
			}
			tt := couchdbEditorTester(s)
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
			config, _ := connections.CouchDBProfileConfig(p)
			c, err := couchdb.Open(ctx, config, password)
			if err != nil {
				t.Fatal("independent saved-profile open failed", err)
			}
			defer c.Close()
			id := "profile_" + strconv.FormatInt(time.Now().UnixNano(), 36)
			documentPath := "/nyadb_fixture/" + id
			if _, err := c.Do(ctx, couchdb.Request{Method: "PUT", Path: documentPath, Body: []byte(`{"_id":"` + id + `","exact":9007199254740993}`)}); err != nil {
				t.Fatal("native saved profile PUT failed", err)
			}
			r, err := c.Do(ctx, couchdb.Request{Method: "GET", Path: documentPath})
			if err != nil || !strings.Contains(string(r.Body), "9007199254740993") {
				t.Fatal("native saved profile read lost exact integer")
			}
		})
	}
}

func TestCouchDBNativeProfileProxyContextAndCanceledOriginalCheck(t *testing.T) {
	cfg, password := couchdbNativeProfileConfig(t, "NYADB_COUCHDB_CONFIG")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	upstream, err := couchdb.Open(ctx, cfg, password)
	if err != nil {
		t.Fatal("original native upstream open failed", err)
	}
	defer upstream.Close()
	entered, acknowledged, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.RequestURI(), "/gateway/couchdb")
		if path == r.URL.RequestURI() {
			w.WriteHeader(400)
			return
		}
		response, err := upstream.Do(r.Context(), couchdb.Request{Method: r.Method, Path: path})
		if err != nil {
			w.WriteHeader(502)
			return
		}
		if path == "/_session" {
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
	cfg.Endpoint, cfg.CAFile, cfg.ServerName = server.URL+"/gateway/couchdb", ca, "example.com"
	p := couchdbNativeProfile(t, cfg, password).Profile
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		defer close(closed)
		config, err := connections.CouchDBProfileConfig(p)
		if err != nil {
			return err
		}
		c, err := couchdb.Open(ctx, config, secret)
		if c != nil {
			c.Close()
		}
		return err
	}})
	defer s.Close()
	s.OpenProfile(p)
	s.connectionSecret = password
	tt := couchdbEditorTester(s)
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
		other = "basic"
	}
	chooseCouchDBAuth(t, tt, other)
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
	if _, err := upstream.Do(ctx, couchdb.Request{Method: "GET", Path: "/"}); err != nil {
		t.Fatal("old check closed independent original client")
	}
}

func TestCouchDBNativeEditorLocalizedFormEvidence(t *testing.T) {
	cfg, _ := couchdbNativeProfileConfig(t, "NYADB_COUCHDB_CONFIG")
	u, _ := url.Parse(cfg.Endpoint)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{900, 700}, {720, 520}} {
			for _, auth := range []string{"none", "basic"} {
				s := New(Dependencies{Vault: &memoryCredentialVault{secrets: map[string]string{}}})
				if err := s.OpenNew(connections.CouchDB, ""); err != nil {
					t.Fatal(err)
				}
				s.connectionDraft.Name, s.connectionDraft.Host = "Apache CouchDB", u.Hostname()
				s.connectionPort = u.Port()
				if u.Scheme == "https" {
					s.connectionDraft.TLSMode = "verify-full"
					s.connectionDraft.CouchDBOptions.CAFile = cfg.CAFile
					s.connectionDraft.CouchDBOptions.ServerName = cfg.ServerName
				}
				tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, float32(size[0]), float32(size[1])) }, size[0], size[1])
				if auth == "basic" {
					if err := tt.Click(locales.Text("CouchDB 认证方式")); err != nil {
						t.Fatal(err)
					}
					if err := tt.Click(locales.Text("用户名和密码")); err != nil {
						t.Fatal(err)
					}
					tt.Frame()
				}
				if s.connectionDraft.CouchDBOptions.Auth != auth {
					t.Fatal("localized auth changed stable native value")
				}
				for _, name := range []string{"CouchDB 服务路径", "CouchDB 认证方式", "保存连接", "取消连接向导"} {
					assertCouchDBControlVisible(t, tt, name, size)
				}
				prefix := language + "-" + strconv.Itoa(size[0]) + "-" + auth
				couchdbFormImage(t, tt, prefix+"-general")
				if err := tt.Click("SSL"); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"CouchDB TLS 模式", "CouchDB CA 证书", "证书服务器名", "保存连接", "取消连接向导"} {
					assertCouchDBControlVisible(t, tt, name, size)
				}
				couchdbFormImage(t, tt, prefix+"-ssl")
				s.Close()
			}
		}
	}
}

func assertCouchDBControlVisible(t *testing.T, tt *ui.Tester, name string, size [2]int) {
	t.Helper()
	r, ok := tt.Find(locales.Text(name))
	if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
		t.Fatal("localized control clipped", name)
	}
}

func couchdbFormImage(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	folder := os.Getenv("NYADB_COUCHDB_PROFILE_IMAGES")
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
