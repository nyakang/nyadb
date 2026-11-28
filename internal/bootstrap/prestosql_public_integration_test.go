package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/nyakang/nyadb/internal/testsupport/prestosqlfixture"
	"github.com/nyakang/nyadb/internal/ui/connectioneditor"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestPrestoSQLNativePublicEditorImportVaultCheckAndRemoval(t *testing.T) {
	f := prestosqlfixture.Read(t)
	directory := filepath.Join(f.Directory, t.Name(), "store")
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
	s := connectioneditor.New(connectioneditor.Dependencies{Profiles: profiles, Vault: vault, TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		return m.Test(driverstore.WithRuntimeObserver(ctx, observe), p, secret)
	}, DriverPackages: connectioneditor.DriverPackages{
		List: func(ctx context.Context, k connections.Kind, dir string) ([]connections.ManagedDriver, error) {
			return packages.List(ctx, k, resolve(dir))
		},
		Import: func(ctx context.Context, k connections.Kind, dir, file string) (connections.ManagedDriver, error) {
			return packages.Import(ctx, k, resolve(dir), file)
		}, Remove: packages.Remove,
	}})
	t.Cleanup(s.Close)
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	if err := s.OpenNew(connections.PrestoSQL, ""); err != nil {
		t.Fatal(err)
	}
	width, height := float32(920), float32(780)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, width, height) }, 920, 780)
	wait := func(pending func() bool) {
		t.Helper()
		deadline := time.Now().Add(120 * time.Second)
		for pending() && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(3 * time.Millisecond)
		}
		tt.Frame()
		if pending() {
			t.Fatal("editor job did not settle", s.Error())
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
	wait(s.DriverBusy)
	s.SetEngineFilePicker(func(kind connectioneditor.EngineFile, row int, version uint64) {
		if kind != connectioneditor.PrestoSQLDriverPackageFile {
			t.Error("foreign picker")
			return
		}
		s.SelectEngineFile(kind, row, version, f.Archive)
	})
	click("导入驱动包")
	wait(s.DriverBusy)
	pin := s.Draft().PrestoSQLOptions
	if pin.DriverVersion != f.Version || pin.DriverSHA256 != f.SHA256 || pin.DriverDirectory != directory {
		t.Fatal("original import pin lost")
	}
	s.BeginURIFromEditor()
	if err := s.ImportURI(f.URI(t)); err != nil {
		t.Fatal(err)
	}
	wait(s.DriverBusy)
	if s.Draft().PrestoSQLOptions.DriverSHA256 != pin.DriverSHA256 {
		t.Fatal("URI replaced selected package")
	}
	click("连接名称")
	tt.Command("selectAll")
	tt.Type("public PrestoSQL editor")
	tt.Frame()
	click("保存凭据")
	click("确定")
	list, err := profiles.List(context.Background())
	if err != nil || len(list) != 1 || s.Active() {
		t.Fatal("public save", err, s.Error())
	}
	p := list[0]
	encoded, found, err := vault.Get(p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Secret(t) != "" && (!found || encoded != f.Secret(t)) {
		t.Fatal("vault changed original credential bundle")
	}
	raw, _ := json.Marshal(p)
	for _, secret := range []string{f.Credentials(t).Password, f.Credentials(t).TrustStorePassword} {
		if secret != "" && strings.Contains(string(raw), secret) {
			t.Fatal("secret entered profile")
		}
	}
	s.OpenProfile(p)
	wait(s.DriverBusy)
	session, err := m.ConnectSession(driverstore.WithRuntimeObserver(context.Background(), observe), p, f.Secret(t))
	if err != nil {
		t.Fatal("builtin open", err)
	}
	provider, err := contract.Require[contract.QueryProvider](session)
	if err != nil {
		t.Fatal(err)
	}
	q, err := provider.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Release)
	if err := q.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	generation := session.Generation()
	click("测试连接")
	wait(s.Testing)
	if !m.IsConnected(p.ID) || m.Generation(p.ID) != generation || s.Error() != "" {
		t.Fatal("isolated connection check altered live session or failed")
	}
	click("移除驱动包")
	f.Screenshot(t, tt, "editor-driver-remove-confirm")
	click("确认移除驱动包")
	wait(s.DriverBusy)
	if !tt.HasText(locales.Text("驱动包仍在使用，请先关闭相关连接。")) || s.Draft().PrestoSQLOptions.DriverSHA256 != pin.DriverSHA256 {
		t.Fatal("live package removal was not refused")
	}
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		tt.SetLocale(language)
		for _, tab := range []string{"常规", "高级", "SSL"} {
			click(tab)
			for _, size := range [][2]int{{920, 780}, {720, 620}} {
				width, height = float32(size[0]), float32(size[1])
				tt.SetSize(size[0], size[1])
				tt.Frame()
				view := tt
				for _, label := range []string{"保存连接", "测试连接", "取消连接向导"} {
					r, ok := view.Find(locales.Text(label))
					if !ok || r.W <= 0 || r.Y+r.H > float32(size[1]+1) || r.X+r.W > float32(size[0]+1) {
						t.Fatal("public control outside viewport", language, tab, label)
					}
				}
				f.Screenshot(t, view, fmt.Sprintf("%s-%dx%d-editor-%s", language, size[0], size[1], tab))
			}
		}
	}
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	tt.SetLocale("zh-Hans")
	width, height = 920, 780
	tt.SetSize(920, 780)
	click("常规")
	q.Release()
	if err := m.Disconnect(p.ID); err != nil {
		t.Fatal(err)
	}
	click("移除驱动包")
	click("确认移除驱动包")
	wait(s.DriverBusy)
	if s.Draft().PrestoSQLOptions.DriverSHA256 != "" || !tt.HasText(locales.Text("驱动包已移除。")) {
		t.Fatal("closed package removal failed")
	}
	original, err := profiles.Get(context.Background(), p.ID)
	if err != nil || original.PrestoSQLOptions != p.PrestoSQLOptions {
		t.Fatal("removal silently changed saved profile")
	}
	if _, err := m.ConnectSession(context.Background(), original, f.Secret(t)); err == nil {
		t.Fatal("removed package silently fell back")
	}
	f.Journal(t, "public-editor.jsonl", map[string]any{"test": t.Name(), "package_imported": true, "vault_bundle_preserved": true, "check_isolated": true, "live_removal_refused": true, "closed_removal_completed": true, "saved_pin_retained": true, "starts": f.Starts.Load()})
}
