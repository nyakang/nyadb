package connectioneditor

import (
	"context"
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
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulNativePublicProfileURIStorageVaultEditorChecksAndOriginalTLS(t *testing.T) {
	if os.Getenv("NYADB_CONSUL_IT") != "1" {
		t.Skip("owned native Consul fixture required")
	}
	endpoint, err := url.Parse(os.Getenv("NYADB_CONSUL_ENDPOINT"))
	if err != nil || endpoint.Hostname() == "" {
		t.Fatal("missing owned endpoint")
	}
	query := url.Values{"dc": {"dc1"}, "prefix": {"native/editor//"}, "consistency": {"consistent"}, "auth": {os.Getenv("NYADB_CONSUL_AUTH")}}
	for key, env := range map[string]string{"ca_file": "NYADB_CONSUL_CA", "client_cert_file": "NYADB_CONSUL_CERT", "client_key_file": "NYADB_CONSUL_KEY"} {
		if value := os.Getenv(env); value != "" {
			query.Set(key, value)
		}
	}
	uri := &url.URL{Scheme: "consul", Host: endpoint.Host, RawQuery: query.Encode()}
	if endpoint.Scheme == "https" {
		uri.Scheme = "consuls"
	}
	secret := os.Getenv("NYADB_CONSUL_TOKEN")
	if secret != "" {
		uri.User = url.UserPassword("", secret)
	}
	path := filepath.Join(t.TempDir(), "config.sqlite")
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	profiles := connections.NewService(localdb.NewProfileStore(db))
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	var closed atomic.Int32
	s := New(Dependencies{Profiles: profiles, Vault: vault, TestConnection: func(ctx context.Context, p connections.Profile, token string) error {
		session, err := consul.OpenSession(ctx, p, token)
		if err != nil {
			return err
		}
		err = session.Close()
		if err == nil {
			closed.Add(1)
		}
		return err
	}})
	defer s.Close()
	if err = s.ImportURI(uri.String()); err != nil {
		t.Fatal("public URI import failed")
	}
	s.connectionSavePassword = secret != ""
	s.Save()
	if s.Error() != "" || s.Active() {
		t.Fatal("public editor save failed", s.Error())
	}
	list, err := profiles.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatal(err)
	}
	original := list[0]
	if original.Kind != connections.Consul || original.Username != "" || original.Database != "" || original.ConsulOptions.Prefix != "native/editor//" {
		t.Fatal("original public scope changed")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	profiles = connections.NewService(localdb.NewProfileStore(db))
	restored, err := profiles.Get(context.Background(), original.ID)
	if err != nil || restored.ConsulOptions != original.ConsulOptions {
		t.Fatal("restart changed Consul settings", err)
	}
	s.OpenProfile(restored)
	if s.connectionSecret != secret {
		t.Fatal("original vault credential changed")
	}
	for _, lang := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return lang })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, float32(size[0]), float32(size[1])) }, size[0], size[1])
			tt.Frame()
			if err = tt.Click(locales.Text("测试连接")); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(20 * time.Second)
			for s.connectionTesting && time.Now().Before(deadline) {
				tt.Frame()
				time.Sleep(time.Millisecond)
			}
			tt.Frame()
			if s.connectionTesting || s.Error() != "" || s.connectionTestMessage != "" && !strings.Contains(s.connectionTestMessage, strings.Split(locales.Text("%s 连接测试成功（已关闭测试会话）"), "%s")[1]) {
				t.Fatal("real public editor check failed", s.Error(), s.connectionTestMessage)
			}
			if secret != "" && tt.HasText(secret) {
				t.Fatal("temporary token exposed")
			}
			if dir := os.Getenv("NYADB_CONSUL_PUBLIC_IMAGES"); dir != "" {
				if err = os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				f, err := os.Create(filepath.Join(dir, lang+"-"+strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1])+"-editor-native.png"))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(f, tt.Image())
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatal("original editor screenshot failed")
				}
			}
		}
	}
	i18n.SetLocaleFunc(nil)
	t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
	if closed.Load() != 4 {
		t.Fatal("test sessions were not closed", closed.Load())
	}
}
