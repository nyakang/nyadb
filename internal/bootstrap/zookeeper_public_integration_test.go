package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

type zooPublicVault struct {
	secrets map[string]string
	setErr  error
}

func (v *zooPublicVault) Get(id int64, key string) (string, bool, error) {
	s, ok := v.secrets[fmt.Sprintf("%d/%s", id, key)]
	return s, ok, nil
}
func (v *zooPublicVault) Set(id int64, key, s string) error {
	if v.setErr != nil {
		return v.setErr
	}
	v.secrets[fmt.Sprintf("%d/%s", id, key)] = s
	return nil
}
func (v *zooPublicVault) Delete(id int64, key string) error {
	delete(v.secrets, fmt.Sprintf("%d/%s", id, key))
	return nil
}

func TestZooKeeperNativePublicBootstrapEditorImportVaultTestAndLiveSession(t *testing.T) {
	f := zookeeperfixture.Read(t)
	directory := t.TempDir()
	store, err := driverstore.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Remove(context.Background(), "zookeeper", f.Version); err != nil {
			t.Error("public installed package remained leased", err)
		}
	})
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
	observe := f.Observe(directory, f.Version)
	packages := managedDriverPackages()
	resolve := func(dir string) string {
		if dir == "" {
			return directory
		}
		return dir
	}
	s := connectioneditor.New(connectioneditor.Dependencies{Profiles: profiles, Vault: vault,
		TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
			return m.Test(driverstore.WithRuntimeObserver(ctx, observe), p, secret)
		},
		DriverPackages: connectioneditor.DriverPackages{
			List: func(ctx context.Context, k connections.Kind, dir string) ([]connections.ManagedDriver, error) {
				return packages.List(ctx, k, resolve(dir))
			},
			Import: func(ctx context.Context, k connections.Kind, dir, file string) (connections.ManagedDriver, error) {
				return packages.Import(ctx, k, resolve(dir), file)
			},
		}})
	t.Cleanup(s.Close)
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	if err := s.OpenNew(connections.ZooKeeper, ""); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 920, 780) }, 920, 780)
	wait := func(pending func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for pending() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		tt.Frame()
		if pending() {
			t.Fatal("public editor job did not settle", s.Error())
		}
	}
	wait(s.DriverBusy)
	s.SetEngineFilePicker(func(kind connectioneditor.EngineFile, row int, version uint64) {
		if kind != connectioneditor.ZooKeeperDriverPackageFile {
			t.Error("foreign public package picker")
		}
		s.SelectEngineFile(kind, row, version, f.Archive)
	})
	if err := tt.Click(locales.Text("导入驱动包")); err != nil {
		t.Fatal(err)
	}
	wait(s.DriverBusy)
	pin := s.Draft().ZooKeeperOptions
	if pin.DriverVersion != f.Version || pin.DriverSHA256 != f.SHA256 || pin.DriverDirectory != directory {
		t.Fatal("bootstrap native package import lost original pin")
	}
	secret := "original-public-fixture-secret"
	s.BeginURIFromEditor()
	uri := &url.URL{Scheme: "zookeeper", Host: f.Endpoint, User: url.UserPassword("fixture-user", secret), RawQuery: "session_timeout=2000&connect_timeout=5000"}
	if err := s.ImportURI(uri.String()); err != nil {
		t.Fatal(err)
	}
	if s.Draft().ZooKeeperOptions.DriverSHA256 != pin.DriverSHA256 {
		t.Fatal("same-kind URI replaced explicit installed package")
	}
	tt.Frame()
	input := func(label, value string) {
		t.Helper()
		if err := tt.Click(locales.Text(label)); err != nil {
			t.Fatal(err)
		}
		tt.Command("selectAll")
		tt.Type(value)
		tt.Frame()
	}
	input("连接名称", "public ZooKeeper editor")
	if err := tt.Click(locales.Text("保存凭据")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(locales.Text("确定")); err != nil {
		t.Fatal(err)
	}
	list, err := profiles.List(context.Background())
	if err != nil || len(list) != 1 || s.Error() != "" || s.Active() {
		t.Fatal("public editor save failed", err, s.Error())
	}
	p := list[0]
	if raw, _ := json.Marshal(p); strings.Contains(string(raw), secret) {
		t.Fatal("temporary URI credential entered public profile")
	}
	if saved, found, err := vault.Get(p.ID, ""); err != nil || !found || saved != secret {
		t.Fatal("public save lost vault credential")
	}
	s.OpenProfile(p)
	wait(s.DriverBusy)
	session, err := m.ConnectSession(driverstore.WithRuntimeObserver(context.Background(), observe), p, secret)
	if err != nil {
		t.Fatal("original builtin session failed", err)
	}
	kv, err := m.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := kv.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lease.Close() })
	if err := lease.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	generation := session.Generation()
	before := f.Starts.Load()
	if err := tt.Click(locales.Text("测试连接")); err != nil {
		t.Fatal(err)
	}
	wait(s.Testing)
	if s.Error() != "" || m.Generation(p.ID) != generation || !m.IsConnected(p.ID) || f.Starts.Load() != before+1 {
		t.Fatal("isolated editor test changed original publication or process", s.Error())
	}
	plan, err := lease.Prepare(contract.KVRequest{Method: "kv_list_prefix", JSON: `{"prefix":"/","recursive":false,"limit":1}`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lease.Execute(context.Background(), plan)
	if err != nil || !result.Completed || result.ZNode == nil {
		t.Fatal("editor check invalidated original physical lease", err)
	}
	// Exercise save failure through the public form and preserve original config/vault.
	input("连接名称", "replacement must roll back")
	input("连接密码", "replacement-secret")
	vault.setErr = errors.New("fixture vault locked")
	s.Save()
	got, err := profiles.Get(context.Background(), p.ID)
	if err != nil || got.Name != p.Name || got.ZooKeeperOptions != p.ZooKeeperOptions || s.Error() == "" {
		t.Fatal("failed vault save changed original profile")
	}
	if saved, _, _ := vault.Get(p.ID, ""); saved != secret {
		t.Fatal("failed public save changed vault")
	}
	vault.setErr = nil
	s.OpenProfile(p)
	wait(s.DriverBusy)
	s.SetEngineFilePicker(func(kind connectioneditor.EngineFile, _ int, _ uint64) {
		switch kind {
		case connectioneditor.ZooKeeperDriverPackageFile, connectioneditor.ZooKeeperCAFile, connectioneditor.ZooKeeperCertificateFile, connectioneditor.ZooKeeperKeyFile:
		default:
			t.Error("foreign public file picker")
		}
	})
	// Keep credentials masked; small windows expose lower fields by scrolling
	// the body while tabs and the confirmation footer remain available.
	type controlEvidence struct {
		ui.Rect
		Screenshot     string
		ExpectedHeight float32
	}
	controls := map[string]map[string]controlEvidence{}
	fullHeights := map[string]float32{}
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{920, 780}, {720, 520}} {
			capture := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, float32(size[0]), float32(size[1])) }, size[0], size[1])
			for _, tab := range []struct{ caption, name string }{{"常规", "general"}, {"高级", "advanced"}, {"SSL", "ssl"}} {
				capture.Frame()
				if err := capture.Click(locales.Text(tab.caption)); err != nil {
					t.Fatal(err)
				}
				capture.Frame()
				name := fmt.Sprintf("%s-%dx%d-public-editor-%s", language, size[0], size[1], tab.name)
				labels := map[string][]string{"general": {"ZooKeeper Chroot", "ZooKeeper Namespace", "ZooKeeper 驱动包", "导入驱动包", "连接密码"}, "advanced": {"ZooKeeper 其他服务器", "ZooKeeper 会话超时（毫秒）", "ZooKeeper 连接超时（毫秒）", "ZooKeeper 文档连接上限"}, "ssl": {"ZooKeeper TLS 模式", "ZooKeeper CA 证书", "选择 ZooKeeper CA 证书", "ZooKeeper 客户端证书", "ZooKeeper 客户端私钥"}}[tab.name]
				controls[name] = map[string]controlEvidence{}
				f.Screenshot(t, capture, name)
				frameName := name
				for index, label := range labels {
					box, ok := capture.Find(locales.Text(label))
					reference := language + "/" + tab.name + "/" + label
					if size[0] == 920 && ok {
						fullHeights[reference] = box.H
					}
					expected := fullHeights[reference]
					if expected < 12 {
						t.Fatal("public wide control reference missing", reference, box)
					}
					if ok && (box.H < expected || box.Y+box.H > float32(size[1])-45) {
						capture.Scroll(float32(size[0])-30, float32(size[1])/2, 0, 200)
						capture.Frame()
						frameName = fmt.Sprintf("%s-scroll-%d", name, index)
						f.Screenshot(t, capture, frameName)
						box, ok = capture.Find(locales.Text(label))
					}
					if !ok || box.W <= 0 || box.H < expected || box.X < 0 || box.Y < 29 || box.X+box.W > float32(size[0])+1 || box.Y+box.H > float32(size[1])-45 || math.IsNaN(float64(box.X+box.Y+box.W+box.H)) {
						t.Fatal("public localized editor control missing or clipped", name, label, box)
					}
					if err := capture.Click(locales.Text(label)); err != nil {
						t.Fatal("visible public control was not actionable", name, label, err)
					}
					capture.Key(0, ui.KeyEscape)
					controls[name][label] = controlEvidence{Rect: box, Screenshot: frameName + ".png", ExpectedHeight: expected}
				}
				if capture.HasText(secret) {
					t.Fatal("original credential displayed")
				}
			}
		}
	}
	raw, err := json.MarshalIndent(controls, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.Directory, "public-editor-controls.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
