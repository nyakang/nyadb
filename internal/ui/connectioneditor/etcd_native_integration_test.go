package connectioneditor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/etcd"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func etcdProfileFixture(t *testing.T) connections.URIImport {
	t.Helper()
	if os.Getenv("NYADB_ETCD_PROFILE_IT") != "1" {
		t.Skip("requires owned native etcd profile fixture")
	}
	u, err := url.Parse(os.Getenv("NYADB_ETCD_ENDPOINT"))
	if err != nil || u.Host == "" {
		t.Fatal("incomplete native fixture")
	}
	u.Scheme = "etcd"
	if os.Getenv("NYADB_ETCD_CA") != "" {
		u.Scheme = "etcds"
	}
	q := url.Values{"prefix": {" /nyadb/Exact/ "}, "connect_timeout_seconds": {"5"}, "request_timeout_seconds": {"5"}}
	if os.Getenv("NYADB_ETCD_MODE") != "anonymous" {
		u.User = url.UserPassword(os.Getenv("NYADB_ETCD_USERNAME"), os.Getenv("NYADB_ETCD_SECRET"))
	}
	for key, env := range map[string]string{"ca_file": "NYADB_ETCD_CA", "client_cert_file": "NYADB_ETCD_CERT", "client_key_file": "NYADB_ETCD_KEY"} {
		if v := os.Getenv(env); v != "" {
			q.Set(key, v)
		}
	}
	u.RawQuery = q.Encode()
	parsed, err := connections.ParseEtcdURI(u.String())
	if err != nil {
		t.Fatal("pure native URI failed")
	}
	return parsed
}
func etcdProfileRead(t *testing.T, c *etcd.Client) {
	t.Helper()
	p, err := c.Prepare(&pb.RangeRequest{Key: []byte("/native-profile-read")})
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Execute(context.Background(), p)
	if err != nil || !r.Completed || r.WriteOutcomeUnknown || c.Identity().Version != os.Getenv("NYADB_ETCD_VERSION") {
		t.Fatal("original native read failed")
	}
}
func etcdProfileOpen(t *testing.T, p connections.Profile, secret string) *etcd.Client {
	t.Helper()
	c, err := etcd.OpenProfile(context.Background(), p, secret)
	if err != nil {
		t.Fatalf("native profile open failed (%T)", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func etcdProfileWait(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for s.connectionTesting && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	if s.connectionTesting || s.Error() != "" || s.connectionTestMessage == "" {
		t.Fatal("native editor check did not finish")
	}
}
func TestEtcdNativeProfileURIReopenAndOriginalEditorCheck(t *testing.T) {
	parsed := etcdProfileFixture(t)
	auths := []string{parsed.Profile.EtcdOptions.Auth}
	if os.Getenv("NYADB_ETCD_MODE") == "mtls" {
		auths = append(auths, "certificate")
	}
	for _, auth := range auths {
		t.Run(auth, func(t *testing.T) {
			p, secret := parsed.Profile, parsed.Password
			p.EtcdOptions.Auth = auth
			if auth == "certificate" {
				p.Username, secret = "", ""
			}
			path := filepath.Join(t.TempDir(), "profiles.sqlite")
			db, err := localdb.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			service := connections.NewService(localdb.NewProfileStore(db))
			vault := &memoryCredentialVault{secrets: map[string]string{}}
			checked := make(chan *etcd.Client, 1)
			s := New(Dependencies{Profiles: service, Vault: vault, TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
				c, err := etcd.OpenProfile(ctx, p, secret)
				if err != nil {
					return err
				}
				closeErr := c.Close()
				checked <- c
				return closeErr
			}})
			t.Cleanup(s.Close)
			s.OpenProfile(p)
			s.connectionSecret, s.connectionSavePassword = secret, auth == "basic"
			s.Save()
			if s.Error() != "" {
				t.Fatal("native save failed")
			}
			profiles, err := service.List(context.Background())
			if err != nil || len(profiles) != 1 {
				t.Fatal("profile missing")
			}
			original := profiles[0]
			copy, err := service.Duplicate(context.Background(), original.ID, "native copy")
			if err != nil || copy.ID == original.ID || copy.EtcdOptions != original.EtcdOptions || copy.Username != original.Username {
				t.Fatal("duplicate changed native identity")
			}
			raw, _ := json.Marshal(original)
			if secret != "" && strings.Contains(string(raw), secret) {
				t.Fatal("profile persisted secret")
			}
			if _, found, _ := vault.Get(copy.ID, ""); found {
				t.Fatal("duplicate inherited original vault identity")
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = localdb.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			service = connections.NewService(localdb.NewProfileStore(db))
			s.SetProfileService(service)
			reopened, err := service.Get(context.Background(), original.ID)
			if err != nil || reopened.EtcdOptions != original.EtcdOptions || reopened.Username != original.Username || reopened.TLSMode != original.TLSMode {
				t.Fatal("restart changed configuration")
			}
			s.OpenProfile(reopened)
			if s.connectionSecret != secret {
				t.Fatal("original credential did not reopen")
			}
			tt := etcdEditorTester(s)
			if err = tt.Click("测试连接"); err != nil {
				t.Fatal(err)
			}
			etcdProfileWait(t, s, tt)
			c := <-checked
			if c.Alive() {
				t.Fatal("test retained original channel")
			}
			if _, err = c.Prepare(&pb.RangeRequest{Key: []byte("/closed")}); !errors.Is(err, etcd.ErrRetired) {
				t.Fatal("closed check remained usable")
			}
			fresh := etcdProfileOpen(t, reopened, secret)
			etcdProfileRead(t, fresh)
			if auth == "basic" {
				bad, err := etcd.OpenProfile(context.Background(), reopened, secret+"incorrect")
				if err == nil {
					bad.Close()
					t.Fatal("wrong original password accepted")
				}
			}
		})
	}
}

func TestEtcdNativeProfileVaultFailurePreservesOriginalConnection(t *testing.T) {
	parsed := etcdProfileFixture(t)
	if parsed.Profile.EtcdOptions.Auth != "basic" {
		// Anonymous fixtures still exercise a basic draft failing vault persistence;
		// its original native anonymous connection must remain usable.
		parsed.Profile.EtcdOptions.Auth = "basic"
		parsed.Profile.Username = "unused"
		parsed.Password = "temporary-secret"
	}
	original := etcdProfileFixture(t)
	admin := etcdProfileOpen(t, original.Profile, original.Password)
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	s.OpenProfile(parsed.Profile)
	s.connectionSecret, s.connectionSavePassword = parsed.Password, true
	s.Save()
	list, err := service.List(context.Background())
	if err != nil || len(list) != 1 || s.Error() != "" {
		t.Fatal("initial native save failed")
	}
	p := list[0]
	s.OpenProfile(p)
	s.connectionDraft.EtcdOptions.Prefix = " /changed/ "
	s.connectionSecret = "replacement-secret"
	vault.setErr = errors.New("locked write")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.EtcdOptions != p.EtcdOptions || s.Error() == "" {
		t.Fatal("vault failure left changed profile")
	}
	secret, found, err := vault.Get(p.ID, "")
	if err != nil || !found || secret != parsed.Password {
		t.Fatal("vault failure changed original credential")
	}
	vault.setErr, vault.getErr = nil, errors.New("locked read")
	s.OpenProfile(p)
	s.setEtcdAuth("none")
	s.Save()
	got, err = service.Get(context.Background(), p.ID)
	if err != nil || got.EtcdOptions.Auth != "basic" {
		t.Fatal("locked vault accepted identity change")
	}
	etcdProfileRead(t, admin)
}

// Hold an actual upstream native Authenticate (or anonymous Status) reply. The
// downstream channel must acknowledge cancellation before a replacement check.
type etcdProfileHandshakeProxy struct {
	pb.UnimplementedAuthServer
	pb.UnimplementedMaintenanceServer
	auth               pb.AuthClient
	maintenance        pb.MaintenanceClient
	observed, canceled chan struct{}
	release            chan struct{}
	once               sync.Once
}

func (p *etcdProfileHandshakeProxy) hold(ctx context.Context) error {
	p.once.Do(func() { close(p.observed) })
	select {
	case <-ctx.Done():
		close(p.canceled)
		return ctx.Err()
	case <-p.release:
		return nil
	}
}
func (p *etcdProfileHandshakeProxy) AuthStatus(ctx context.Context, r *pb.AuthStatusRequest) (*pb.AuthStatusResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	return p.auth.AuthStatus(metadata.NewOutgoingContext(ctx, md.Copy()), r)
}
func (p *etcdProfileHandshakeProxy) UserGet(ctx context.Context, r *pb.AuthUserGetRequest) (*pb.AuthUserGetResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	return p.auth.UserGet(metadata.NewOutgoingContext(ctx, md.Copy()), r)
}
func (p *etcdProfileHandshakeProxy) RoleGet(ctx context.Context, r *pb.AuthRoleGetRequest) (*pb.AuthRoleGetResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	return p.auth.RoleGet(metadata.NewOutgoingContext(ctx, md.Copy()), r)
}
func (p *etcdProfileHandshakeProxy) Authenticate(ctx context.Context, r *pb.AuthenticateRequest) (*pb.AuthenticateResponse, error) {
	reply, err := p.auth.Authenticate(ctx, r)
	if err != nil {
		return nil, err
	}
	if err = p.hold(ctx); err != nil {
		return nil, err
	}
	return reply, nil
}
func (p *etcdProfileHandshakeProxy) Status(ctx context.Context, r *pb.StatusRequest) (*pb.StatusResponse, error) {
	reply, err := p.maintenance.Status(ctx, r)
	if err != nil {
		return nil, err
	}
	if err = p.hold(ctx); err != nil {
		return nil, err
	}
	return reply, nil
}
func TestEtcdNativeProfileOriginalCanceledHandshakeAndLateCheck(t *testing.T) {
	parsed := etcdProfileFixture(t)
	admin := etcdProfileOpen(t, parsed.Profile, parsed.Password)
	u, _ := url.Parse(os.Getenv("NYADB_ETCD_ENDPOINT"))
	var creds credentials.TransportCredentials = insecure.NewCredentials()
	if u.Scheme == "https" {
		pem, err := os.ReadFile(os.Getenv("NYADB_ETCD_CA"))
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			t.Fatal("CA")
		}
		tc := &tls.Config{RootCAs: roots, ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}
		if os.Getenv("NYADB_ETCD_CERT") != "" {
			pair, err := tls.LoadX509KeyPair(os.Getenv("NYADB_ETCD_CERT"), os.Getenv("NYADB_ETCD_KEY"))
			if err != nil {
				t.Fatal(err)
			}
			tc.Certificates = []tls.Certificate{pair}
		}
		creds = credentials.NewTLS(tc)
	}
	upstream, err := grpc.NewClient("passthrough:///"+u.Host, grpc.WithTransportCredentials(creds), grpc.WithDisableRetry(), grpc.WithNoProxy())
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	proxy := &etcdProfileHandshakeProxy{auth: pb.NewAuthClient(upstream), maintenance: pb.NewMaintenanceClient(upstream), observed: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	defer close(proxy.release)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	server := grpc.NewServer()
	pb.RegisterAuthServer(server, proxy)
	pb.RegisterMaintenanceServer(server, proxy)
	go server.Serve(ln)
	defer server.Stop()
	finished := make(chan struct{}, 1)
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		c, err := etcd.OpenProfile(ctx, p, secret)
		if c != nil {
			err = errors.Join(err, c.Close())
		}
		finished <- struct{}{}
		return err
	}})
	t.Cleanup(s.Close)
	blocked := parsed.Profile
	blocked.Host = "127.0.0.1"
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(port)
	blocked.Port = uint16(n)
	blocked.TLSMode = "disable"
	blocked.EtcdOptions.CAFile, blocked.EtcdOptions.ServerName, blocked.EtcdOptions.ClientCertFile, blocked.EtcdOptions.ClientKeyFile = "", "", "", ""
	s.OpenProfile(blocked)
	s.connectionSecret = parsed.Password
	tt := etcdEditorTester(s)
	if err = tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proxy.observed:
	case <-time.After(8 * time.Second):
		t.Fatal("actual upstream handshake not observed")
	}
	// Opening the replacement revokes only the captured original check.
	s.OpenProfile(parsed.Profile)
	s.connectionSecret = parsed.Password
	select {
	case <-finished:
	case <-time.After(8 * time.Second):
		t.Fatal("original executor did not finish")
	}
	select {
	case <-proxy.canceled:
	case <-time.After(8 * time.Second):
		t.Fatal("original native RPC remained open")
	}
	tt.Frame()
	if s.connectionTesting || s.connectionTestResults != nil || s.Error() != "" || s.connectionDraft.Host != parsed.Profile.Host {
		t.Fatal("late check entered replacement draft")
	}
	if err = tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	etcdProfileWait(t, s, tt)
	etcdProfileRead(t, admin)
}

func TestEtcdNativeEditorLocalizedFormEvidence(t *testing.T) {
	parsed := etcdProfileFixture(t)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			for _, auth := range []string{"none", "basic", "certificate"} {
				for _, encoding := range []string{"text", "base64"} {
					s := New(Dependencies{Vault: &memoryCredentialVault{secrets: map[string]string{}}})
					s.OpenProfile(parsed.Profile)
					s.setEtcdAuth(auth)
					s.connectionDraft.EtcdOptions.PrefixEncoding, s.connectionDraft.EtcdOptions.Prefix = encoding, " /Case/键 "
					if encoding == "base64" {
						s.connectionDraft.EtcdOptions.Prefix = "AP8gL0tleS8g"
					}
					if auth == "certificate" {
						s.connectionDraft.TLSMode = "verify-full"
						s.connectionDraft.EtcdOptions.CAFile = "/example/ca.crt"
						s.connectionDraft.EtcdOptions.ClientCertFile = "/example/client.crt"
						s.connectionDraft.EtcdOptions.ClientKeyFile = "/example/client.key"
					}
					if auth == "basic" {
						s.connectionDraft.Username = " example user "
						s.connectionSecret = "example-masked-password"
					}
					s.initializeInputs()
					s.SetEngineFilePicker(func(EngineFile, int, uint64) {})
					tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, float32(size[0]), float32(size[1])) }, size[0], size[1])
					if tt.HasText("example-masked-password") {
						t.Fatal("password exposed")
					}
					for _, tab := range []string{"常规", "高级", "SSL"} {
						if tab != "常规" {
							if err := tt.Click(locales.Text(tab)); err != nil {
								t.Fatal(err)
							}
						}
						tt.Frame()
						fields := []string{"保存连接", "取消连接向导"}
						switch tab {
						case "常规":
							fields = append(fields, "etcd 前缀编码", "etcd 键前缀", "etcd 认证方式")
							if auth == "basic" {
								fields = append(fields, "用户名", "密码", "保存密码")
							}
						case "高级":
							fields = append(fields, "etcd 连接超时", "etcd 请求超时")
						case "SSL":
							fields = append(fields, "etcd TLS 模式", "etcd CA 证书", "etcd 证书服务器名", "选择 etcd CA 证书")
							if auth != "none" {
								fields = append(fields, "etcd 客户端证书", "etcd 客户端私钥", "选择 etcd 客户端证书", "选择 etcd 客户端私钥")
							}
						}
						for _, field := range fields {
							r, ok := tt.Find(locales.Text(field))
							if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
								t.Fatalf("localized control clipped: %s / %s / %s", language, tab, field)
							}
						}
						if folder := os.Getenv("NYADB_ETCD_PROFILE_IMAGES"); folder != "" {
							if err := os.MkdirAll(folder, 0700); err != nil {
								t.Fatal(err)
							}
							file, err := os.Create(filepath.Join(folder, fmt.Sprintf("%s-%dx%d-%s-%s-%s.png", language, size[0], size[1], auth, encoding, tab)))
							if err != nil {
								t.Fatal(err)
							}
							err = png.Encode(file, tt.Image())
							closeErr := file.Close()
							if err != nil || closeErr != nil {
								t.Fatal("image evidence failed")
							}
						}
					}
					s.Close()
				}
			}
		}
	}
}

func TestEtcdNativeProfileParkedSuccessDoesNotEnterReplacementDraft(t *testing.T) {
	parsed := etcdProfileFixture(t)
	opened := make(chan *etcd.Client, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	finished := make(chan struct{}, 2)
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		c, err := etcd.OpenProfile(ctx, p, secret)
		if err == nil {
			opened <- c
			if p.Name == "parked original" {
				<-release
			}
			err = c.Close()
		}
		finished <- struct{}{}
		return err
	}})
	t.Cleanup(s.Close)
	p := parsed.Profile
	p.Name = "parked original"
	s.OpenProfile(p)
	s.connectionSecret = parsed.Password
	tt := etcdEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	var original *etcd.Client
	select {
	case original = <-opened:
	case <-time.After(8 * time.Second):
		t.Fatal("original native check did not open")
	}
	p.Name = "replacement native"
	s.OpenProfile(p)
	s.connectionSecret = parsed.Password
	tt.Frame()
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	etcdProfileWait(t, s, tt)
	replacement := <-opened
	message, generation := s.connectionTestMessage, s.connectionTestGeneration
	releaseOnce.Do(func() { close(release) })
	for range 2 {
		select {
		case <-finished:
		case <-time.After(8 * time.Second):
			t.Fatal("native check did not settle")
		}
	}
	tt.Frame()
	if original.Alive() || replacement.Alive() || s.connectionTestMessage != message || s.connectionTestGeneration != generation || s.connectionDraft.Name != "replacement native" || s.Error() != "" {
		t.Fatal("parked original changed replacement check")
	}
	fresh := etcdProfileOpen(t, parsed.Profile, parsed.Password)
	etcdProfileRead(t, fresh)
}
