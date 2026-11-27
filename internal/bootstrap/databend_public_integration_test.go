package bootstrap

import (
	"bytes"
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
	"github.com/nyakang/nyadb/internal/testsupport/databendfixture"
	"github.com/nyakang/nyadb/internal/ui/connectioneditor"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestDatabendNativePublicBootstrapEditorImportVaultTestAndLiveSession(t *testing.T) {
	f := databendfixture.Read(t)
	directory := filepath.Join(f.Directory, t.Name(), "store")
	store, err := driverstore.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Remove(context.Background(), "databend", f.Version); err != nil {
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
	observe := f.Observe(directory)
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
	if err := s.OpenNew(connections.Databend, ""); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 920, 780) }, 920, 780)
	wait := func(pending func() bool) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
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
		if kind != connectioneditor.DatabendDriverPackageFile {
			t.Error("foreign public package picker")
		}
		s.SelectEngineFile(kind, row, version, f.Archive)
	})
	if err := tt.Click(locales.Text("导入驱动包")); err != nil {
		t.Fatal(err)
	}
	wait(s.DriverBusy)
	pin := s.Draft().DatabendOptions
	if pin.DriverVersion != f.Version || pin.DriverSHA256 != f.SHA256 || pin.DriverDirectory != directory {
		t.Fatal("bootstrap native package import lost original pin")
	}
	secret := f.Secret(t)
	s.BeginURIFromEditor()
	endpoint, _ := url.Parse(f.Endpoint)
	uri := &url.URL{Scheme: "databend", Host: endpoint.Host, Path: "/" + f.Database, User: url.UserPassword(f.Username, secret), RawQuery: "ssl=false"}
	if err := s.ImportURI(uri.String()); err != nil {
		t.Fatal(err)
	}
	if s.Draft().DatabendOptions.DriverSHA256 != pin.DriverSHA256 {
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
	input("连接名称", "public Databend editor")
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
	lease, err := session.(contract.QueryProvider).ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
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
	result, err := lease.Execute(context.Background(), f.Database, "SELECT id,exact,payload,absent FROM managed_values")
	if err != nil || len(result.Sets) != 1 || len(result.Sets[0].Rows) != 1 {
		t.Fatal("editor check invalidated original physical lease", err)
	}
	row := result.Sets[0].Rows[0]
	if row[0].Text != "18446744073709551615" || row[1].Text != "9007199254740993.12345678901234567890" || !bytes.Equal(row[2].Bytes, []byte{0, 255, 128}) || row[3].Kind != contract.Null {
		t.Fatal("public builtin lost typed values")
	}
	f.Journal(t, "public-bootstrap.jsonl", map[string]any{"test": t.Name(), "public_provider_registered": true, "driver_version": pin.DriverVersion, "driver_sha256": pin.DriverSHA256, "generation": generation, "test_preserved_session": true, "query": result})
	// Cancellation before launch must not create or publish a process.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	before = f.Starts.Load()
	if err := m.Test(driverstore.WithRuntimeObserver(canceled, observe), p, secret); err == nil || !errors.Is(canceled.Err(), context.Canceled) || strings.Contains(err.Error(), secret) || f.Starts.Load() != before || m.Generation(p.ID) != generation {
		t.Fatal("canceled public check started a process or replaced session", err)
	}
	// Exercise save failure through the public form and preserve original config/vault.
	input("连接名称", "replacement must roll back")
	input("连接密码", "replacement-secret")
	vault.setErr = errors.New("fixture vault locked")
	s.Save()
	got, err := profiles.Get(context.Background(), p.ID)
	if err != nil || got.Name != p.Name || got.DatabendOptions != p.DatabendOptions || s.Error() == "" {
		t.Fatal("failed vault save changed original profile")
	}
	if saved, _, _ := vault.Get(p.ID, ""); saved != secret {
		t.Fatal("failed public save changed vault")
	}
	vault.setErr = nil
	s.OpenProfile(p)
	wait(s.DriverBusy)
	tlsDraft := p
	tlsDraft.TLSMode = "verify-full"
	s.OpenProfile(tlsDraft)
	wait(s.DriverBusy)
	s.SetEngineFilePicker(func(kind connectioneditor.EngineFile, _ int, _ uint64) {
		switch kind {
		case connectioneditor.DatabendDriverPackageFile, connectioneditor.DatabendCAFile, connectioneditor.DatabendCertificateFile, connectioneditor.DatabendKeyFile:
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
				labels := map[string][]string{"general": {"初始数据库", "Databend 认证方式", "连接密码", "Databend 驱动包", "导入驱动包"}, "advanced": {"Databend 连接超时（秒）", "Databend 请求超时（秒）", "Databend 查询超时（秒）", "Databend 连接上限", "Databend 元数据保留连接", "Databend SQL 连接上限", "Databend 连接等待（毫秒）"}, "ssl": {"Databend TLS 模式", "Databend TLS 服务器名称", "Databend CA 证书", "选择 Databend CA 证书", "Databend 客户端证书", "Databend 客户端私钥"}}[tab.name]
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
