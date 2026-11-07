package connectioneditor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"image/png"
	"io"
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
	solrsvc "github.com/nyakang/nyadb/internal/services/solr"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func solrNativeProfileConfig(t *testing.T) (connections.SolrConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_SOLR_IT") != "1" {
		t.Skip("requires owned native Solr fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_SOLR_CONFIG"))
	if err != nil {
		t.Fatal("private Solr probe unavailable")
	}
	var probe struct {
		Endpoint, Username, Auth string
		CAFile                   string `json:"ca_file"`
		PasswordFile             string `json:"password_file"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("invalid private Solr probe")
	}
	secret, err := os.ReadFile(probe.PasswordFile)
	if err != nil {
		t.Fatal("private Solr credential unavailable")
	}
	return connections.SolrConfig{Endpoint: probe.Endpoint, Auth: probe.Auth, Username: probe.Username, CAFile: probe.CAFile}, string(secret)
}

func TestSolrNativeProfileURIReopenAndOriginalEditorCheck(t *testing.T) {
	cfg, password := solrNativeProfileConfig(t)
	t.Run("configured_principal", func(t *testing.T) { solrNativeProfileReopen(t, cfg, password) })
	if cfg.Auth == "basic" {
		t.Run("raw_native_padding_and_local_rejection", func(t *testing.T) {
			ca, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				t.Fatal("private CA unavailable")
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(ca) {
				t.Fatal("private CA invalid")
			}
			transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
			request, err := http.NewRequest("GET", cfg.Endpoint+"/admin/info/system?wt=json", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.SetBasicAuth(" "+cfg.Username+" ", " "+password+" ")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal("native padding probe failed")
			}
			_ = response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatal("original BasicAuthPlugin trim behavior changed")
			}
			bad := cfg
			bad.Username = " " + cfg.Username + " "
			if _, err := connections.NormalizeSolrConfig(bad); !errors.Is(err, connections.ErrInvalid) {
				t.Fatal("native-trimmed principal accepted locally")
			}
			if err := connections.ValidateSolrCredential(cfg, " "+password+" ", true); !errors.Is(err, connections.ErrInvalid) {
				t.Fatal("native-trimmed password accepted locally")
			}
		})
		cfg.Username = "\u00a0native-nbsp\u00a0"
		t.Run("exact_unicode_principal", func(t *testing.T) { solrNativeProfileReopen(t, cfg, password) })
	}
}

func solrNativeProfileReopen(t *testing.T, cfg connections.SolrConfig, password string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	endpoint, _ := url.Parse(cfg.Endpoint)
	uri := url.URL{Scheme: "solr", Host: endpoint.Host, Path: endpoint.Path}
	options := url.Values{"auth": {cfg.Auth}, "sslmode": {"verify-full"}}
	if endpoint.Scheme == "http" {
		options.Set("sslmode", "disable")
	}
	if cfg.CAFile != "" {
		options.Set("ca_file", cfg.CAFile)
		options.Set("server_name", "localhost")
	}
	if cfg.Auth == "basic" {
		uri.User = url.UserPassword(cfg.Username, password)
	}
	uri.RawQuery = options.Encode()
	parsed, err := connections.ParseURI(uri.String())
	if err != nil || parsed.Password != password || parsed.Profile.Database != "" {
		t.Fatal("native pure URI import failed")
	}
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
		config, err := connections.SolrProfileConfig(p)
		if err != nil {
			return err
		}
		client, err := solrsvc.Open(ctx, config, secret)
		if err != nil {
			return err
		}
		defer client.Close()
		if client.Identity().Mode != "std" || client.Identity().Lucene.SolrVersion != "9.9.0" {
			return solrsvc.ErrProtocol
		}
		client.Close()
		if _, err := client.Do(ctx, solrsvc.Request{Method: "GET", Path: "/admin/info/system?wt=json"}); !errors.Is(err, solrsvc.ErrRetired) {
			return errors.New("original probe handle remained open")
		}
		closed.Add(1)
		return nil
	}
	s := New(Dependencies{Profiles: service, Vault: vault, TestConnection: probe})
	defer s.Close()
	// Exercise the private form with the pure imported profile; the public URI
	// dialog remains gated until provider/metadata/workbench registration.
	s.OpenProfile(parsed.Profile)
	s.connectionSecret, s.connectionSavePassword = parsed.Password, cfg.Auth == "basic"
	s.Save()
	if s.Error() != "" || s.Active() {
		t.Fatal("native private editor save failed", s.Error())
	}
	profiles, err := service.List(ctx)
	if err != nil || len(profiles) != 1 {
		t.Fatal("saved profile missing")
	}
	copy, err := service.Duplicate(ctx, profiles[0].ID, "private native copy")
	if err != nil || copy.SolrOptions != profiles[0].SolrOptions {
		t.Fatal("native copy changed options")
	}
	if _, found, _ := vault.Get(copy.ID, ""); found {
		t.Fatal("copy duplicated credential")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service = connections.NewService(localdb.NewProfileStore(db))
	p, err := service.Get(ctx, profiles[0].ID)
	if err != nil || p.SolrOptions.ContextPath != endpoint.Path || p.Username != cfg.Username {
		t.Fatal("restart changed native service context", err)
	}
	s = New(Dependencies{Profiles: service, Vault: vault, TestConnection: probe})
	defer s.Close()
	s.OpenProfile(p)
	if s.connectionSecret != password || s.connectionSavePassword != (cfg.Auth == "basic") {
		t.Fatal("restart failed to restore separate credential")
	}
	tt := solrEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for s.connectionTesting && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if s.connectionTesting || s.Error() != "" || !strings.Contains(s.connectionTestMessage, "已关闭测试会话") || closed.Load() != 1 {
		t.Fatal("native editor check did not close original handle", s.Error())
	}
	public, _ := json.Marshal(p)
	if password != "" && strings.Contains(string(public), password) {
		t.Fatal("credential entered public profile")
	}
	config, err := connections.SolrProfileConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	other, err := solrsvc.Open(ctx, config, password)
	if err != nil {
		t.Fatal("independent saved-profile open failed", err)
	}
	defer other.Close()
	id := "profile_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := other.Do(ctx, solrsvc.Request{Method: "POST", Path: "/nyadb_fixture/update?commit=true&wt=json", Body: []byte(`[{"id":"` + id + `","exact_l":9007199254740993}]`)}); err != nil {
		t.Fatal(err)
	}
	response, err := other.Do(ctx, solrsvc.Request{Method: "GET", Path: "/nyadb_fixture/get?id=" + id + "&wt=json"})
	if err != nil || !strings.Contains(string(response.Body), "9007199254740993") {
		t.Fatal("independent original native result lost precision", err)
	}
}

func TestSolrNativeProfileProxyContextAndCanceledOriginalCheck(t *testing.T) {
	cfg, password := solrNativeProfileConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := solrsvc.Open(ctx, cfg, password)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	entered, acknowledged, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var dispatches atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/gateway/solr/") {
			w.WriteHeader(400)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			w.WriteHeader(400)
			return
		}
		response, err := admin.Do(r.Context(), solrsvc.Request{Method: r.Method, Path: strings.TrimPrefix(r.URL.RequestURI(), "/gateway/solr"), Body: body})
		if err != nil {
			w.WriteHeader(502)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/admin/cores") {
			dispatches.Add(1)
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
	endpoint, _ := url.Parse(server.URL)
	parsed, err := connections.ParseURI("solr://" + endpoint.Host + "/gateway/solr?ca_file=" + url.QueryEscape(ca) + "&server_name=example.com")
	if err != nil {
		t.Fatal("private proxy profile invalid")
	}
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, _ string) error {
		config, err := connections.SolrProfileConfig(p)
		if err != nil {
			return err
		}
		client, err := solrsvc.Open(ctx, config, "")
		if err != nil {
			return err
		}
		defer func() { client.Close(); close(closed) }()
		_, err = client.Do(ctx, solrsvc.Request{Method: "GET", Path: "/admin/cores?action=STATUS&wt=json"})
		return err
	}})
	defer s.Close()
	s.OpenProfile(parsed.Profile)
	tt := solrEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("native STATUS did not reach original core context")
	}
	chooseSolrAuth(t, tt, "basic")
	for _, done := range []<-chan struct{}{acknowledged, closed} {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("native original worker cancellation/close did not settle")
		}
	}
	tt.Frame()
	if s.connectionTesting || s.connectionTestResults != nil || s.Error() != "" || dispatches.Load() != 1 {
		t.Fatal("late result changed authentication draft or request replayed")
	}
	if _, err := admin.Do(ctx, solrsvc.Request{Method: "GET", Path: "/admin/info/system?wt=json"}); err != nil {
		t.Fatal("independent native client was closed", err)
	}
}

func TestSolrNativeEditorLocalizedFormEvidence(t *testing.T) {
	_, _ = solrNativeProfileConfig(t)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{900, 700}, {720, 520}} {
			for _, auth := range []string{"none", "basic"} {
				s := New(Dependencies{Vault: &memoryCredentialVault{secrets: map[string]string{}}})
				if err := s.OpenNew(connections.Solr, ""); err != nil {
					t.Fatal(err)
				}
				s.connectionDraft.Name = "Apache Solr"
				tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, float32(size[0]), float32(size[1])) }, size[0], size[1])
				if auth == "basic" {
					if err := tt.Click(locales.Text("Solr 认证方式")); err != nil {
						t.Fatal(err)
					}
					if err := tt.Click(locales.Text("用户名和密码")); err != nil {
						t.Fatal(err)
					}
					tt.Frame()
				}
				if s.connectionDraft.SolrOptions.Auth != auth {
					t.Fatal("localized auth selection lost stable value")
				}
				for _, name := range []string{"Solr 服务路径", "Solr 认证方式", "保存连接", "取消连接向导"} {
					assertSolrControlVisible(t, tt, name, size)
				}
				prefix := language + "-" + strconv.Itoa(size[0]) + "-" + auth
				solrFormImage(t, tt, prefix+"-general")
				if err := tt.Click("SSL"); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"Solr TLS 模式", "Solr CA 证书", "证书服务器名", "保存连接", "取消连接向导"} {
					assertSolrControlVisible(t, tt, name, size)
				}
				solrFormImage(t, tt, prefix+"-ssl")
				s.Close()
			}
		}
	}
}

func assertSolrControlVisible(t *testing.T, tt *ui.Tester, name string, size [2]int) {
	t.Helper()
	r, ok := tt.Find(locales.Text(name))
	if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
		t.Fatal("localized form control clipped", name)
	}
}

func solrFormImage(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	folder := os.Getenv("NYADB_SOLR_PROFILE_IMAGES")
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
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal("form evidence write failed")
	}
}
