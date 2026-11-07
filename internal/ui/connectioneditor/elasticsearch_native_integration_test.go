package connectioneditor

import (
	"context"
	"encoding/json"
	"image/png"
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
	elasticsvc "github.com/nyakang/nyadb/internal/services/elasticsearch"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func elasticsearchNativeProfileConfig(t *testing.T) (connections.ElasticsearchConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_ELASTICSEARCH_IT") != "1" {
		t.Skip("requires owned disposable Elasticsearch fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_ELASTICSEARCH_CONFIG"))
	if err != nil {
		t.Fatal("private Elasticsearch probe cannot be read")
	}
	var probe struct {
		Endpoint, Username, Auth string
		CAFile                   string `json:"ca_file"`
		PasswordFile             string `json:"password_file"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("invalid private Elasticsearch probe")
	}
	secret, err := os.ReadFile(probe.PasswordFile)
	if err != nil {
		t.Fatal("private Elasticsearch secret cannot be read")
	}
	return connections.ElasticsearchConfig{Endpoint: probe.Endpoint, Auth: probe.Auth, Username: probe.Username, CAFile: probe.CAFile}, string(secret)
}

func TestElasticsearchNativeProfileURIReopenAndEditorConnectionChecks(t *testing.T) {
	cfg, password := elasticsearchNativeProfileConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := elasticsvc.Open(ctx, cfg, password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	type scenario struct{ auth, username, secret string }
	cases := []scenario{{cfg.Auth, cfg.Username, password}}
	if cfg.Auth == "basic" {
		reply, err := admin.Do(ctx, elasticsvc.Request{Method: "POST", Path: "/_security/api_key", Body: []byte(`{"name":"nyadb-profile-fixture","expiration":"5m","role_descriptors":{"reader":{"cluster":["monitor"],"indices":[]}}}`)})
		if err != nil {
			t.Fatal(err)
		}
		var key struct{ ID, Encoded string }
		if json.Unmarshal(reply.Body, &key) != nil || key.ID == "" || key.Encoded == "" {
			t.Fatal("native API key receipt missing")
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			body, _ := json.Marshal(map[string][]string{"ids": {key.ID}})
			if _, err := admin.Do(cleanup, elasticsvc.Request{Method: "DELETE", Path: "/_security/api_key", Body: body}); err != nil {
				t.Error("owned API key cleanup failed")
			}
		}()
		cases = append(cases, scenario{"api_key", "", key.Encoded})
	}
	endpoint, _ := url.Parse(cfg.Endpoint)
	for _, tc := range cases {
		t.Run(tc.auth, func(t *testing.T) {
			uri := url.URL{Scheme: "elasticsearch", Host: endpoint.Host}
			options := url.Values{"auth": {tc.auth}, "sslmode": {"verify-full"}}
			if endpoint.Scheme == "http" {
				options.Set("sslmode", "disable")
			}
			if cfg.CAFile != "" {
				options.Set("ca_file", cfg.CAFile)
				options.Set("server_name", "localhost")
			}
			if tc.auth != "none" {
				uri.User = url.UserPassword(tc.username, tc.secret)
			}
			uri.RawQuery = options.Encode()
			parsed, err := connections.ParseURI(uri.String())
			if err != nil || parsed.Password != tc.secret {
				t.Fatal("native URI import failed")
			}
			path := filepath.Join(t.TempDir(), "config.sqlite")
			db, err := localdb.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			service := connections.NewService(localdb.NewProfileStore(db))
			vault := &memoryCredentialVault{secrets: map[string]string{}}
			var closed atomic.Int32
			probe := func(ctx context.Context, p connections.Profile, secret string) error {
				config, err := connections.ElasticsearchProfileConfig(p)
				if err != nil {
					return err
				}
				client, err := elasticsvc.Open(ctx, config, secret)
				if err != nil {
					return err
				}
				if client.Identity().ClusterUUID != admin.Identity().ClusterUUID {
					_ = client.Close()
					return elasticsvc.ErrProtocol
				}
				err = client.Close()
				if err == nil {
					closed.Add(1)
				}
				return err
			}
			s := New(Dependencies{Profiles: service, Vault: vault, TestConnection: probe})
			s.OpenProfile(parsed.Profile)
			s.connectionSecret, s.connectionSavePassword = parsed.Password, tc.auth != "none"
			s.Save()
			if s.Error() != "" || s.Active() {
				t.Fatal("native editor profile save failed", s.Error())
			}
			profiles, err := service.List(ctx)
			if err != nil || len(profiles) != 1 {
				t.Fatal("saved native profile missing")
			}
			copy, err := service.Duplicate(ctx, profiles[0].ID, "native public copy")
			if err != nil || copy.ElasticsearchOptions != profiles[0].ElasticsearchOptions {
				t.Fatal("public profile copy failed")
			}
			if _, found, _ := vault.Get(copy.ID, ""); found {
				t.Fatal("profile duplicate copied a credential")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = localdb.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			service = connections.NewService(localdb.NewProfileStore(db))
			p, err := service.Get(ctx, profiles[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			s = New(Dependencies{Profiles: service, Vault: vault, TestConnection: probe})
			defer s.Close()
			s.OpenProfile(p)
			if s.connectionSecret != tc.secret || s.connectionSavePassword != (tc.auth != "none") {
				t.Fatal("restart failed to restore separate credential")
			}
			tt := elasticsearchEditorTester(s)
			if err := tt.Click("测试连接"); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(15 * time.Second)
			for s.connectionTesting && time.Now().Before(deadline) {
				tt.Frame()
				time.Sleep(5 * time.Millisecond)
			}
			if s.connectionTesting || s.Error() != "" || !strings.Contains(s.connectionTestMessage, "已关闭测试会话") || closed.Load() != 1 {
				t.Fatal("native editor check did not close original probe", s.Error())
			}
			public, _ := json.Marshal(p)
			if tc.secret != "" && strings.Contains(string(public), tc.secret) {
				t.Fatal("credential entered public profile")
			}
		})
	}
}

func TestElasticsearchNativeEditorLocalizedFormEvidence(t *testing.T) {
	_, _ = elasticsearchNativeProfileConfig(t)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{900, 700}, {720, 520}} {
			for _, auth := range []string{"basic", "api_key", "none"} {
				s := New(Dependencies{Vault: &memoryCredentialVault{secrets: map[string]string{}}})
				if err := s.OpenNew(connections.Elasticsearch, ""); err != nil {
					t.Fatal(err)
				}
				s.connectionDraft.Name = "Elasticsearch"
				tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, float32(size[0]), float32(size[1])) }, size[0], size[1])
				if auth != "basic" {
					if err := tt.Click(locales.Text("Elasticsearch 认证方式")); err != nil {
						t.Fatal(err)
					}
					if err := tt.Click(locales.Text(elasticsearchAuthCaption(auth))); err != nil {
						t.Fatal(err)
					}
					tt.Frame()
				}
				if s.connectionDraft.ElasticsearchOptions.Auth != auth {
					t.Fatal("localized selection lost stable authentication value")
				}
				prefix := language + "-" + strconv.Itoa(size[0]) + "-" + auth
				elasticsearchFormImage(t, tt, prefix+"-general")
				if err := tt.Click("SSL"); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"Elasticsearch TLS 模式", "Elasticsearch CA 证书", "证书服务器名", "保存连接", "取消连接向导"} {
					r, ok := tt.Find(locales.Text(name))
					if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
						t.Fatal("localized SSL control clipped", name)
					}
				}
				elasticsearchFormImage(t, tt, prefix+"-ssl")
				s.Close()
			}
		}
	}
}

func elasticsearchFormImage(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	folder := os.Getenv("NYADB_ELASTICSEARCH_PROFILE_IMAGES")
	if folder == "" {
		return
	}
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(folder, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(file, tt.Image())
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal("form evidence cannot be saved")
	}
}
