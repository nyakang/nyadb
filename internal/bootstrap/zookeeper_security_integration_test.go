package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/testsupport/zookeeperfixture"
	"github.com/nyakang/nyadb/internal/ui/connectioneditor"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// Forward original client bytes to the real server and withhold the real
// server's handshake reply. This changes delivery, never protocol contents.
func zooSecurityHandshakeProxy(t *testing.T, endpoint string) (string, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var sockets []net.Conn
	closed := false
	register := func(socket net.Conn) bool {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			socket.Close()
			return false
		}
		sockets = append(sockets, socket)
		return true
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			if !register(client) {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer client.Close()
				server, err := net.DialTimeout("tcp", endpoint, time.Second)
				if err != nil {
					return
				}
				defer server.Close()
				if !register(server) {
					return
				}
				workers.Add(1)
				go func() { defer workers.Done(); io.Copy(server, client) }()
				buffer := make([]byte, 4096)
				for {
					n, err := server.Read(buffer)
					if n > 0 {
						once.Do(func() { close(blocked) })
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		closed = true
		for _, socket := range sockets {
			socket.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return listener.Addr().String(), blocked
}

func TestZooKeeperNativePublicEditorSecurityFilesVaultTestAndCancellation(t *testing.T) {
	f := zookeeperfixture.ReadSecurity(t)
	_, driver := f.Install(t)
	db, err := localdb.Open(filepath.Join(t.TempDir(), "config.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	profiles := connections.NewService(localdb.NewProfileStore(db))
	vault := &zooPublicVault{secrets: map[string]string{}}
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	t.Cleanup(func() { m.Close() })
	ctx := driverstore.WithRuntimeObserver(context.Background(), f.Observe(driver.Directory, driver.Version))
	s := connectioneditor.New(connectioneditor.Dependencies{Profiles: profiles, Vault: vault, DriverPackages: managedDriverPackages(), TestConnection: func(request context.Context, p connections.Profile, secret string) error {
		return m.Test(driverstore.WithRuntimeObserver(request, f.Observe(driver.Directory, driver.Version)), p, secret)
	}})
	t.Cleanup(s.Close)
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 920, 780) }, 920, 780)
	wait := func(pending func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for pending() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(time.Millisecond)
		}
		tt.Frame()
		if pending() {
			t.Fatal("original security editor job did not settle", s.Error())
		}
	}
	click := func(label string) {
		t.Helper()
		tt.Frame()
		if err := tt.Click(locales.Text(label)); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	input := func(label, value string) {
		t.Helper()
		click(label)
		tt.Command("selectAll")
		tt.Type(value)
		tt.Frame()
	}
	for index, mode := range []string{"tls", "mtls", "sasl"} {
		endpoint := []string{f.Security.TLSEndpoint, f.Security.MTLSEndpoint, f.Security.SASLEndpoint}[index]
		p := f.EndpointProfile(t, driver, 0, endpoint)
		p.Name = "public ZooKeeper " + mode
		secret := ""
		if mode == "sasl" {
			p.Username = f.Security.Username
			p.ZooKeeperOptions.Auth = "sasl_digest"
			secret = f.Security.SASLSecret
		} else {
			p.TLSMode = "verify-full"
		}
		p, err = profiles.Create(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		original := p
		if mode != "sasl" {
			original.ZooKeeperOptions.CAFile = f.Security.CAFile
		}
		if mode == "mtls" {
			original.ZooKeeperOptions.ClientCertFile = f.Security.ClientCertFile
			original.ZooKeeperOptions.ClientKeyFile = f.Security.ClientKeyFile
		}
		published, err := m.ConnectSession(ctx, original, secret)
		if err != nil {
			t.Fatal("original secure editor reference publication", mode, err)
		}
		provider, err := m.KVRequests(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := provider.ReserveKV(1)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { lease.Close() })
		if err := lease.Preconnect(ctx); err != nil {
			t.Fatal(err)
		}
		s.OpenProfile(p)
		wait(s.DriverBusy)
		if mode == "sasl" {
			click("常规")
			input("连接密码", secret)
			click("保存凭据")
		}
		selected := map[connectioneditor.EngineFile]int{}
		s.SetEngineFilePicker(func(kind connectioneditor.EngineFile, row int, version uint64) {
			selected[kind]++
			var path string
			switch kind {
			case connectioneditor.ZooKeeperCAFile:
				path = f.Security.CAFile
			case connectioneditor.ZooKeeperCertificateFile:
				path = f.Security.ClientCertFile
			case connectioneditor.ZooKeeperKeyFile:
				path = f.Security.ClientKeyFile
			default:
				t.Error("foreign secure editor picker", kind)
				return
			}
			s.SelectEngineFile(kind, row, version, path)
		})
		if mode != "sasl" {
			click("SSL")
			click("选择 ZooKeeper CA 证书")
		}
		if mode == "mtls" {
			click("选择 ZooKeeper 客户端证书")
			click("选择 ZooKeeper 客户端私钥")
		}
		before := f.Starts.Load()
		click("测试连接")
		wait(s.Testing)
		if s.Error() != "" || m.Generation(p.ID) != published.Generation() || !m.IsConnected(p.ID) || f.Starts.Load() != before+1 {
			t.Fatal("secure editor test changed original publication", mode, s.Error())
		}
		if mode != "sasl" && selected[connectioneditor.ZooKeeperCAFile] != 1 || mode == "mtls" && (selected[connectioneditor.ZooKeeperCertificateFile] != 1 || selected[connectioneditor.ZooKeeperKeyFile] != 1) {
			t.Fatal("public certificate paths bypassed native picker callbacks")
		}
		click("确定")
		saved, err := profiles.Get(context.Background(), p.ID)
		if err != nil || s.Active() || saved.ZooKeeperOptions != original.ZooKeeperOptions {
			t.Fatal("secure public save changed captured options", mode, err, s.Error())
		}
		if raw, _ := json.Marshal(saved); secret != "" && strings.Contains(string(raw), secret) {
			t.Fatal("public secure profile retained a secret")
		}
		if mode == "sasl" {
			savedSecret, found, err := vault.Get(p.ID, "")
			if err != nil || !found || savedSecret != secret {
				t.Fatal("public SASL vault save failed")
			}
		}
		s.OpenProfile(saved)
		wait(s.DriverBusy)
		if mode == "sasl" {
			input("连接密码", "deliberately-wrong-sasl")
			click("测试连接")
			wait(s.Testing)
			if s.Error() == "" || m.Generation(p.ID) != published.Generation() {
				t.Fatal("failed SASL editor test published a replacement")
			}
			input("连接密码", secret)
			click("测试连接")
			wait(s.Testing)
			if s.Error() != "" {
				t.Fatal("original SASL credential did not recover editor testing", s.Error())
			}
		}
		if mode == "tls" {
			proxy, blocked := zooSecurityHandshakeProxy(t, endpoint)
			_, port, _ := net.SplitHostPort(proxy)
			click("常规")
			input("端口", port)
			click("测试连接")
			select {
			case <-blocked:
			case <-time.After(10 * time.Second):
				t.Fatal("real original TLS handshake reply was not captured")
			}
			click("取消测试")
			wait(s.Testing)
			input("端口", strconv.Itoa(int(saved.Port)))
			click("测试连接")
			wait(s.Testing)
			if s.Error() != "" || m.Generation(p.ID) != published.Generation() {
				t.Fatal("cancelled original handshake changed later editor test or publication", s.Error())
			}
		}
		plan, err := lease.Prepare(contract.KVRequest{Method: "kv_get", JSON: `{"key":"/nyadb_secure"}`})
		if err != nil {
			t.Fatal(err)
		}
		result, err := lease.Execute(ctx, plan)
		if err != nil || !result.Completed || result.ZNode == nil || string(result.ZNode.Reply.Node.Value) != "fixture" {
			t.Fatal("isolated secure editor checks invalidated original lease", mode, err)
		}
		for _, language := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return language })
			for _, size := range [][2]int{{920, 780}, {720, 520}} {
				view := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, float32(size[0]), float32(size[1])) }, size[0], size[1])
				view.Frame()
				if mode == "sasl" {
					if err := view.Click(locales.Text("常规")); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := view.Click(locales.Text("SSL")); err != nil {
						t.Fatal(err)
					}
				}
				view.Frame()
				if secret != "" && view.HasText(secret) {
					t.Fatal("secure editor displayed an unmasked secret")
				}
				f.Screenshot(t, view, fmt.Sprintf("%s-%dx%d-security-editor-%s", language, size[0], size[1], mode))
			}
		}
		i18n.SetLocaleFunc(func() string { return "zh-Hans" })
		s.Cancel()
		tt.Frame()
	}
}
