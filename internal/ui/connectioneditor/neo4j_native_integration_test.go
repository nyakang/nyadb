package connectioneditor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type neo4jProfileFixture struct {
	Config                       connections.Neo4jConfig
	Secret, Mode, Version, Nonce string
}

func neo4jProfileFixtureTest(t *testing.T) neo4jProfileFixture {
	t.Helper()
	if os.Getenv("NYADB_NEO4J_PROFILE_IT") != "1" {
		t.Skip("requires an owned native Neo4j profile fixture")
	}
	b, e := os.ReadFile(os.Getenv("NYADB_NEO4J_CONFIG_FILE"))
	if e != nil {
		t.Fatal("cannot read private fixture")
	}
	var f neo4jProfileFixture
	if json.Unmarshal(b, &f) != nil || len(f.Nonce) != 32 || f.Version != "5.26.21" {
		t.Fatal("invalid owned native fixture")
	}
	return f
}
func neo4jProfileURI(t *testing.T, f neo4jProfileFixture, routing bool) connections.URIImport {
	t.Helper()
	cfg, e := connections.NormalizeNeo4jConfig(f.Config)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(cfg.URI)
	u.Path = "/" + cfg.Database
	if routing {
		u.Scheme = strings.Replace(u.Scheme, "bolt", "neo4j", 1)
	}
	q := url.Values{"auth": {cfg.Auth}, "connect_timeout": {strconv.Itoa(cfg.ConnectTimeoutSeconds)}, "request_timeout": {strconv.Itoa(cfg.RequestTimeoutSeconds)}}
	for k, v := range map[string]string{"ca_file": cfg.CAFile, "client_cert_file": cfg.ClientCertFile, "client_key_file": cfg.ClientKeyFile} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if routing {
		q.Set("region", "owned")
	}
	if cfg.Auth == "basic" {
		u.User = url.UserPassword(cfg.Username, f.Secret)
	}
	u.RawQuery = q.Encode()
	parsed, e := connections.ParseURI(u.String())
	if e != nil {
		t.Fatal("pure native URI failed")
	}
	got, e := connections.Neo4jProfileConfig(parsed.Profile)
	if e != nil {
		t.Fatal(e)
	}
	if routing {
		native, _ := url.Parse(cfg.URI)
		native.Scheme = strings.Replace(native.Scheme, "bolt", "neo4j", 1)
		native.RawQuery = "region=owned"
		cfg.URI = native.String()
	}
	if got != cfg || parsed.Password != f.Secret || strings.Contains(fmtProfile(parsed.Profile), f.Secret) && f.Secret != "" {
		t.Fatal("profile URI changed native settings or captured secret")
	}
	return parsed
}
func fmtProfile(p connections.Profile) string { b, _ := json.Marshal(p); return string(b) }
func neo4jProfileOpen(t *testing.T, cfg connections.Neo4jConfig, secret string) *neo4j.Client {
	t.Helper()
	c, e := neo4j.Open(context.Background(), cfg, secret)
	if e != nil {
		t.Fatalf("native open failed (%T)", e)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func neo4jProfileReadOne(t *testing.T, c *neo4j.Client) {
	t.Helper()
	p, e := c.Prepare(neo4j.Request{Query: "RETURN 1 AS n", Mode: "read"})
	if e != nil {
		t.Fatal(e)
	}
	r, e := c.Execute(context.Background(), p)
	if e != nil || !r.Completed || r.Uncertain || len(r.Rows) != 1 || r.Rows[0][0].Text != "1" {
		t.Fatalf("native read failed (%T)", e)
	}
}

func TestNeo4jNativeProfileURIReopenAndOriginalEditorCheck(t *testing.T) {
	f := neo4jProfileFixtureTest(t)
	for _, routing := range []bool{false, true} {
		t.Run(strconv.FormatBool(routing), func(t *testing.T) {
			parsed := neo4jProfileURI(t, f, routing)
			path := filepath.Join(t.TempDir(), "profiles.sqlite")
			db, e := localdb.Open(path)
			if e != nil {
				t.Fatal(e)
			}
			service := connections.NewService(localdb.NewProfileStore(db))
			vault := &memoryCredentialVault{secrets: map[string]string{}}
			var closedMu sync.Mutex
			var checked []*neo4j.Client
			tester := func(ctx context.Context, p connections.Profile, secret string) error {
				cfg, e := connections.Neo4jProfileConfig(p)
				if e != nil {
					return e
				}
				c, e := neo4j.Open(ctx, cfg, secret)
				if e != nil {
					return e
				}
				plan, e := c.Prepare(neo4j.Request{Query: "RETURN 1", Mode: "read"})
				if e == nil {
					var r neo4j.Response
					r, e = c.Execute(ctx, plan)
					if e == nil && (!r.Completed || r.Summary.Database != cfg.Database || c.Identity().Agent != "Neo4j/"+f.Version) {
						e = neo4j.ErrProtocol
					}
				}
				closeErr := c.Close()
				closedMu.Lock()
				checked = append(checked, c)
				closedMu.Unlock()
				return errors.Join(e, closeErr)
			}
			s := New(Dependencies{Profiles: service, Vault: vault, TestConnection: tester})
			t.Cleanup(s.Close)
			s.OpenProfile(parsed.Profile)
			s.connectionSecret, s.connectionSavePassword = parsed.Password, f.Config.Auth == "basic"
			s.Save()
			if s.Error() != "" {
				t.Fatal("native editor save failed")
			}
			profiles, e := service.List(context.Background())
			if e != nil || len(profiles) != 1 {
				t.Fatal(e)
			}
			original := profiles[0]
			copy, e := service.Duplicate(context.Background(), original.ID, "native copy")
			if e != nil || copy.Neo4jOptions != original.Neo4jOptions || copy.Username != original.Username || copy.Database != original.Database {
				t.Fatal("duplicate changed native profile", e)
			}
			if e := db.Close(); e != nil {
				t.Fatal(e)
			}
			db, e = localdb.Open(path)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			service = connections.NewService(localdb.NewProfileStore(db))
			s.SetProfileService(service)
			reopened, e := service.Get(context.Background(), original.ID)
			if e != nil || reopened.Neo4jOptions != original.Neo4jOptions || reopened.TLSMode != original.TLSMode {
				t.Fatal("restart changed native profile", e)
			}
			s.OpenProfile(reopened)
			if s.connectionSecret != parsed.Password {
				t.Fatal("original credential did not reopen")
			}
			tt := neo4jEditorTester(s)
			if e := tt.Click("测试连接"); e != nil {
				t.Fatal(e)
			}
			deadline := time.Now().Add(8 * time.Second)
			for s.connectionTesting && time.Now().Before(deadline) {
				tt.Frame()
				time.Sleep(10 * time.Millisecond)
			}
			if s.connectionTesting || s.Error() != "" || s.connectionTestMessage == "" {
				t.Fatal("native editor check did not finish successfully")
			}
			closedMu.Lock()
			defer closedMu.Unlock()
			if len(checked) != 1 || checked[0].Alive() {
				t.Fatal("native check retained its original driver")
			}
			if _, e := checked[0].Prepare(neo4j.Request{Query: "RETURN 1"}); !errors.Is(e, neo4j.ErrRetired) {
				t.Fatal("closed check client remained usable")
			}
			cfg, e := connections.Neo4jProfileConfig(reopened)
			if e != nil {
				t.Fatal(e)
			}
			fresh := neo4jProfileOpen(t, cfg, parsed.Password)
			neo4jProfileReadOne(t, fresh)
		})
	}
}

func TestNeo4jNativeProfileOriginalCanceledHandshake(t *testing.T) {
	f := neo4jProfileFixtureTest(t)
	admin := neo4jProfileOpen(t, f.Config, f.Secret)
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	observed := make(chan error, 1)
	eof := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	go func() {
		client, e := ln.Accept()
		if e != nil {
			observed <- e
			return
		}
		defer client.Close()
		u, _ := url.Parse(f.Config.URI)
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		var server net.Conn
		if strings.Contains(u.Scheme, "+") {
			pem, err := os.ReadFile(f.Config.CAFile)
			if err != nil {
				observed <- err
				return
			}
			roots := x509.NewCertPool()
			roots.AppendCertsFromPEM(pem)
			cfg := &tls.Config{RootCAs: roots, ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}
			if f.Config.ClientCertFile != "" {
				cert, err := tls.LoadX509KeyPair(f.Config.ClientCertFile, f.Config.ClientKeyFile)
				if err != nil {
					observed <- err
					return
				}
				cfg.Certificates = []tls.Certificate{cert}
			}
			server, e = tls.DialWithDialer(dialer, "tcp", u.Host, cfg)
		} else {
			server, e = dialer.Dial("tcp", u.Host)
		}
		if e != nil {
			observed <- e
			return
		}
		defer server.Close()
		go func() { _, _ = io.Copy(server, client); close(eof) }()
		_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
		handshake := make([]byte, 4)
		_, e = io.ReadFull(server, handshake)
		observed <- e
		<-release
	}()
	finished := make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		defer close(finished)
		cfg, e := connections.Neo4jProfileConfig(p)
		if e != nil {
			return e
		}
		client, e := neo4j.Open(ctx, cfg, secret)
		if client != nil {
			_ = client.Close()
		}
		return e
	}})
	t.Cleanup(s.Close)
	parsed := neo4jProfileURI(t, f, false)
	s.OpenProfile(parsed.Profile)
	s.connectionDraft.Host = "127.0.0.1"
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	s.connectionPort = port
	s.connectionDraft.TLSMode = "disable"
	s.connectionDraft.Neo4jOptions.CAFile = ""
	s.connectionDraft.Neo4jOptions.ClientCertFile = ""
	s.connectionDraft.Neo4jOptions.ClientKeyFile = ""
	s.connectionSecret = f.Secret
	s.initializeInputs()
	s.connectionPort = port
	tt := neo4jEditorTester(s)
	if e := tt.Click("测试连接"); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-observed:
		if e != nil {
			t.Fatalf("owned upstream native handshake failed (%T)", e)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("native handshake not observed")
	}
	if e := s.OpenNew(connections.Neo4j, ""); e != nil {
		t.Fatal(e)
	}
	s.connectionDraft.Name = "replacement draft"
	select {
	case <-finished:
	case <-time.After(8 * time.Second):
		t.Fatal("canceled original handshake did not finish")
	}
	select {
	case <-eof:
	case <-time.After(8 * time.Second):
		t.Fatal("canceled original native socket remained open")
	}
	releaseOnce.Do(func() { close(release) })
	tt.Frame()
	if s.connectionTesting || s.connectionTestResults != nil || s.connectionError != "" || s.connectionSecret != "" || s.connectionDraft.Name != "replacement draft" {
		t.Fatal("canceled handshake result entered replacement draft")
	}
	neo4jProfileReadOne(t, admin)
}

func TestNeo4jNativeEditorLocalizedFormEvidence(t *testing.T) {
	f := neo4jProfileFixtureTest(t)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{900, 700}, {720, 520}} {
			for _, routing := range []bool{false, true} {
				for _, auth := range []string{"basic", "none"} {
					parsed := neo4jProfileURI(t, f, routing)
					s := New(Dependencies{Vault: &memoryCredentialVault{secrets: map[string]string{}}})
					s.OpenProfile(parsed.Profile)
					s.setNeo4jAuth(auth)
					if auth == "basic" {
						s.connectionSecret = "example-masked-password"
					}
					s.SetEngineFilePicker(func(EngineFile, int, uint64) {})
					tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, float32(size[0]), float32(size[1])) }, size[0], size[1])
					if tt.HasText("example-masked-password") || s.connectionDraft.Neo4jOptions.Auth != auth || s.connectionDraft.Neo4jOptions.Protocol != parsed.Profile.Neo4jOptions.Protocol {
						t.Fatal("localized form changed stable auth/protocol or exposed password")
					}
					prefix := language + "-" + strconv.Itoa(size[0]) + "-" + s.connectionDraft.Neo4jOptions.Protocol + "-" + auth
					for _, tab := range []string{"常规", "高级", "SSL"} {
						if tab != "常规" {
							if e := tt.Click(locales.Text(tab)); e != nil {
								t.Fatal(e)
							}
						}
						tt.Frame()
						fields := []string{"保存连接", "取消连接向导"}
						switch tab {
						case "常规":
							fields = append(fields, "Neo4j 连接方式", "Neo4j 初始数据库", "Neo4j 认证方式")
							if auth == "basic" {
								fields = append(fields, "用户名", "密码", "保存密码")
							}
						case "高级":
							fields = append(fields, "Neo4j 连接超时", "Neo4j 请求超时")
							if routing {
								fields = append(fields, "Neo4j 路由参数")
							}
						case "SSL":
							fields = append(fields, "Neo4j TLS 模式", "Neo4j CA 证书", "Neo4j 客户端证书", "Neo4j 客户端私钥", "选择 Neo4j CA 证书", "选择 Neo4j 客户端证书", "选择 Neo4j 客户端私钥")
						}
						for _, field := range fields {
							r, ok := tt.Find(locales.Text(field))
							if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
								t.Fatalf("localized native control clipped: %s / %s / %s", language, tab, field)
							}
						}
						if folder := os.Getenv("NYADB_NEO4J_PROFILE_IMAGES"); folder != "" {
							if e := os.MkdirAll(folder, 0700); e != nil {
								t.Fatal(e)
							}
							file, e := os.Create(filepath.Join(folder, prefix+"-"+tab+".png"))
							if e != nil {
								t.Fatal(e)
							}
							e = png.Encode(file, tt.Image())
							closeErr := file.Close()
							if e != nil || closeErr != nil {
								t.Fatal("cannot retain native form evidence")
							}
						}
					}
					s.Close()
					if !reflect.DeepEqual(connectionEditorTabNames(connections.Neo4j), []string{"常规", "高级", "SSL", "备注"}) {
						t.Fatal("graph form exposes unrelated relational/tunnel tabs")
					}
				}
			}
		}
	}
}
