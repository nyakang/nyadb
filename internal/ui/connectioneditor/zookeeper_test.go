package connectioneditor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func zookeeperUnitDriver(t *testing.T) connections.ManagedDriver {
	t.Helper()
	return connections.ManagedDriver{Key: "zookeeper", Version: "native-1", SHA256: strings.Repeat("a", 64), Directory: t.TempDir(), Profiles: []string{"zookeeper-v1"}}
}

func zookeeperEditorTester(s *State) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 760, 760) }, 760, 760)
}

func TestZooKeeperEditorSaveVaultRollbackAndAuthSwitch(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.ZooKeeper, ""); err != nil {
		t.Fatal(err)
	}
	d := zookeeperUnitDriver(t)
	s.driverWork.packages = []connections.ManagedDriver{d}
	s.selectZooKeeperDriver(d)
	tt := zookeeperEditorTester(s.State)
	setEngineOptionInput(t, tt, "连接名称", "original ZooKeeper")
	setEngineOptionInput(t, tt, "ZooKeeper Chroot", "/app")
	setEngineOptionInput(t, tt, "ZooKeeper Namespace", "/ns")
	setEngineOptionInput(t, tt, "ZooKeeper 键前缀", "/logical")
	chooseConsul(t, tt, "ZooKeeper 认证方式", "Digest")
	setEngineOptionInput(t, tt, "用户名", "dbx@ops")
	setEngineOptionInput(t, tt, "连接密码", "original-secret")
	if err := tt.Click(locales.Text("保存凭据")); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("original-secret") {
		t.Fatal("credential was displayed")
	}
	if err := tt.Click(locales.Text("高级")); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "ZooKeeper 其他服务器", "zk-2:2281")
	setEngineOptionInput(t, tt, zookeeperNumberLabels[1], "5000")
	s.Save()
	list, err := service.List(context.Background())
	if err != nil || len(list) != 1 || s.Error() != "" {
		t.Fatal("public native profile save failed", err, s.Error())
	}
	p := list[0]
	if p.Database != "" || p.Username != "dbx@ops" || p.ZooKeeperOptions.Auth != "digest" || p.ZooKeeperOptions.Chroot != "/app" || p.ZooKeeperOptions.Namespace != "/ns" || p.ZooKeeperOptions.Prefix != "/logical" || p.ZooKeeperOptions.AdditionalServers != "zk-2:2281" || p.ZooKeeperOptions.ConnectionTimeoutMS != 5000 || p.ZooKeeperOptions.DriverVersion != d.Version || p.ZooKeeperOptions.DriverSHA256 != d.SHA256 || strings.Contains(fmt.Sprint(p), "original-secret") {
		t.Fatal("public save lost identity/pin or stored credential")
	}
	s.OpenProfile(p)
	if s.connectionSecret != "original-secret" || !s.connectionSavePassword {
		t.Fatal("reopened profile did not read original vault credential")
	}
	s.connectionDraft.Name, s.connectionSecret = "replacement", "replacement-secret"
	vault.setErr = errors.New("locked")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.Name != p.Name || got.ZooKeeperOptions != p.ZooKeeperOptions || s.Error() == "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-secret" {
		t.Fatal("failed vault save altered original public profile")
	}
	vault.setErr = nil
	s.OpenProfile(p)
	s.setZooKeeperAuth("none")
	if s.connectionSecret != "" || s.connectionDraft.Username != "" || s.connectionSavePassword {
		t.Fatal("auth switch retained hidden credentials")
	}
	s.Cancel()
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-secret" {
		t.Fatal("canceled auth switch changed the vault")
	}
	s.OpenProfile(p)
	s.setZooKeeperAuth("none")
	s.Save()
	if _, found, _ := vault.Get(p.ID, ""); found || s.Error() != "" {
		t.Fatal("explicit no-auth save retained original credential", s.Error())
	}
}

func TestZooKeeperEditorRejectsRawBoundsAndUnselectedOrForeignDrivers(t *testing.T) {
	s := New(Dependencies{})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.ZooKeeper, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "original"
	if _, err := s.draftProfile(); err == nil {
		t.Fatal("unselected native driver accepted")
	}
	d := zookeeperUnitDriver(t)
	s.selectZooKeeperDriver(d)
	original := s.connectionDraft.ZooKeeperOptions
	for _, change := range []func(*connections.ManagedDriver){
		func(d *connections.ManagedDriver) { d.Key = "h2" },
		func(d *connections.ManagedDriver) { d.Profiles = []string{"h2-v1"} },
		func(d *connections.ManagedDriver) { d.JDBCClass = "org.h2.Driver" },
		func(d *connections.ManagedDriver) { d.SHA256 = "invalid" },
	} {
		candidate := d
		change(&candidate)
		s.selectZooKeeperDriver(candidate)
		if s.connectionDraft.ZooKeeperOptions != original || s.driverWork.message == "" {
			t.Fatal("foreign driver replaced original pin")
		}
	}
	for i, invalid := range []string{"999", "300001", "not-an-integer", "33"} {
		before := s.zookeeperNumbers[i]
		s.zookeeperNumbers[i] = invalid
		if _, err := s.draftProfile(); err == nil || s.zookeeperNumbers[i] != invalid || s.connectionDraft.ZooKeeperOptions != original {
			t.Fatal("invalid raw input was accepted or overwritten", i)
		}
		s.zookeeperNumbers[i] = before
	}
	if p, err := s.draftProfile(); err != nil || p.ZooKeeperOptions != original {
		t.Fatal("restored valid raw inputs did not validate", err)
	}
}

func TestZooKeeperEditorCheckCapturesOriginalConfigurationAndCancels(t *testing.T) {
	started, canceled, release, finished := make(chan connections.Profile, 1), make(chan struct{}), make(chan struct{}), make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		defer close(finished)
		if secret != "original-secret" {
			return errors.New("wrong captured credential")
		}
		started <- p
		<-ctx.Done()
		close(canceled)
		<-release
		return errors.New("late original-secret")
	}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.ZooKeeper, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "original"
	s.selectZooKeeperDriver(zookeeperUnitDriver(t))
	s.setZooKeeperAuth("digest")
	s.connectionDraft.Username, s.connectionSecret = "ops", "original-secret"
	s.connectionDraft.ZooKeeperOptions.Chroot = "/app"
	tt := zookeeperEditorTester(s)
	if err := tt.Click(locales.Text("测试连接")); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-started:
		if p.ZooKeeperOptions.Chroot != "/app" || p.ZooKeeperOptions.Auth != "digest" || p.Username != "ops" {
			t.Fatal("check did not capture original configuration")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native check did not start", s.Error())
	}
	chooseConsul(t, tt, "ZooKeeper 认证方式", "无认证")
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("auth switch did not cancel captured check")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("original executor did not acknowledge cancellation")
	}
	tt.Frame()
	if s.connectionTesting || s.Error() != "" || s.connectionSecret != "" {
		t.Fatal("late native check changed the new draft")
	}
}

func TestZooKeeperEditorPackageCompletionAndTrustFilesKeepOriginalForm(t *testing.T) {
	started, release := make(chan context.Context, 1), make(chan struct{})
	d := zookeeperUnitDriver(t)
	s := New(Dependencies{DriverPackages: DriverPackages{Import: func(ctx context.Context, kind connections.Kind, directory, file string) (connections.ManagedDriver, error) {
		if kind != connections.ZooKeeper || directory != "" || file != "original.zip" {
			return connections.ManagedDriver{}, errors.New("wrong captured package request")
		}
		started <- ctx
		<-release
		return d, nil
	}}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.ZooKeeper, ""); err != nil {
		t.Fatal(err)
	}
	s.ImportDriverPackage("original.zip")
	var ctx context.Context
	select {
	case ctx = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("original native package import did not start")
	}
	original := s.driverWork.results
	s.Cancel()
	if err := s.OpenNew(connections.ZooKeeper, ""); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("original import context not revoked")
	}
	close(release)
	select {
	case <-original:
	case <-time.After(3 * time.Second):
		t.Fatal("captured package importer did not settle")
	}
	s.consumeDriverWork()
	if s.connectionDraft.ZooKeeperOptions.DriverVersion != "" || len(s.driverWork.packages) != 0 {
		t.Fatal("late native package import selected a different form")
	}
	s.setZooKeeperTLS("verify-full")
	version := s.EngineFileVersion()
	s.SelectEngineFile(ZooKeeperCertificateFile, -1, version, "/owned/client.crt")
	s.SelectEngineFile(ZooKeeperKeyFile, -1, version, "/owned/client.key")
	if s.connectionDraft.ZooKeeperOptions.ClientCertFile != "/owned/client.crt" || s.connectionDraft.ZooKeeperOptions.ClientKeyFile != "/owned/client.key" {
		t.Fatal("mTLS files incorrectly required digest credentials")
	}
	s.setZooKeeperTLS("disable")
	s.SelectEngineFile(ZooKeeperCertificateFile, -1, version, "/late/client.crt")
	if s.connectionDraft.ZooKeeperOptions.ClientCertFile != "/owned/client.crt" {
		t.Fatal("stale trust picker altered changed TLS draft")
	}
}

func TestZooKeeperEditorURIKeepsPinAndInvalidImportKeepsRawDraft(t *testing.T) {
	s := New(Dependencies{})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.ZooKeeper, ""); err != nil {
		t.Fatal(err)
	}
	d := zookeeperUnitDriver(t)
	s.selectZooKeeperDriver(d)
	s.connectionDraft.Name, s.connectionSecret = "original", "original-secret"
	s.zookeeperNumbers[0] = "original-invalid"
	if err := s.ImportURI("zookeeper://ops:private@localhost?unknown=private"); err == nil || s.connectionDraft.Name != "original" || s.connectionSecret != "original-secret" || s.zookeeperNumbers[0] != "original-invalid" {
		t.Fatal("invalid URI changed original form or credential")
	}
	s.BeginURIFromEditor()
	if err := s.ImportURI("zookeeper://zk-1:2181,zk-2:2281/app?namespace=/ns"); err != nil {
		t.Fatal(err)
	}
	a := s.connectionDraft.ZooKeeperOptions
	if s.connectionDraft.Name != "original" || a.DriverVersion != d.Version || a.DriverSHA256 != d.SHA256 || a.DriverDirectory != d.Directory || a.AdditionalServers != "zk-2:2281" || a.Chroot != "/app" || a.Namespace != "/ns" || s.connectionSecret != "" {
		t.Fatal("same-kind URI lost explicit pin or imported scope")
	}
}
