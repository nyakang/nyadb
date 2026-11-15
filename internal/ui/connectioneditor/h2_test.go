package connectioneditor

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func h2UnitDriver(t *testing.T) connections.ManagedDriver {
	t.Helper()
	return connections.ManagedDriver{Key: "h2", Version: "owned", SHA256: strings.Repeat("a", 64), Directory: t.TempDir(), Profiles: []string{"h2-v1", "h2-v2", "h2-v3"}}
}

func TestH2EditorSavesOriginalFilePinAndVaultAndPreservesOnRollback(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	d := h2UnitDriver(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.H2, ""); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 760, 650) }, 760, 650)
	s.selectH2Driver(d)
	setEngineOptionInput(t, tt, "连接名称", "H2 original")
	file := filepath.Join(t.TempDir(), "chosen.mv.db")
	s.SelectEngineFile(H2MainFile, -1, s.EngineFileVersion(), file)
	s.connectionSecret, s.connectionSavePassword = "vault-original", true
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "最大连接数", "4")
	setEngineOptionInput(t, tt, "元数据预留连接数", "1")
	setEngineOptionInput(t, tt, "SQL 会话上限", "3")
	setEngineOptionInput(t, tt, "连接池等待（毫秒）", "1500")
	s.Save()
	list, err := service.List(context.Background())
	if err != nil || len(list) != 1 || s.Error() != "" {
		t.Fatal("H2 save failed", err, s.Error())
	}
	p := list[0]
	if p.Database != strings.TrimSuffix(file, ".mv.db") || p.H2Options.DriverVersion != d.Version || p.H2Options.DriverSHA256 != d.SHA256 || p.H2Options.DriverDirectory != d.Directory || p.H2Options.MaxConnections != 4 || p.H2Options.MetadataReserve != 1 || p.H2Options.SQLMaxConnections != 3 || p.H2Options.PoolWaitTimeoutMilliseconds != 1500 {
		t.Fatal("H2 original settings changed")
	}
	s.OpenProfile(p)
	if s.connectionSecret != "vault-original" || !s.connectionSavePassword {
		t.Fatal("H2 vault password not restored")
	}
	version := s.EngineFileVersion()
	s.connectionSecret = "discarded"
	s.Cancel()
	s.OpenProfile(p)
	s.SelectEngineFile(H2MainFile, -1, version, filepath.Join(t.TempDir(), "wrong.mv.db"))
	if s.connectionDraft.Database != p.Database || s.connectionSecret != "vault-original" {
		t.Fatal("old H2 picker or canceled secret changed the reopened profile")
	}
	s.connectionDraft.Name = "must rollback"
	s.connectionSecret = "replacement"
	vault.setErr = errors.New("fixture vault unavailable")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.Name != p.Name || got.H2Options != p.H2Options {
		t.Fatal("failed H2 credential save replaced original configuration", err)
	}
	password, _, _ := vault.Get(p.ID, "")
	if password != "vault-original" {
		t.Fatal("failed H2 credential save changed original secret")
	}
}

func TestH2DriverImportCompletionStaysWithOriginalEditorAndProfile(t *testing.T) {
	started := make(chan context.Context, 1)
	finish := make(chan struct{})
	d := h2UnitDriver(t)
	s := New(Dependencies{DriverPackages: DriverPackages{Import: func(ctx context.Context, kind connections.Kind, dir, file string) (connections.ManagedDriver, error) {
		if kind != connections.H2 || dir != "" || file != "original.zip" {
			t.Error("driver import did not capture original request")
		}
		started <- ctx
		<-finish
		return d, nil
	}}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.H2, ""); err != nil {
		t.Fatal(err)
	}
	s.ImportDriverPackage("original.zip")
	ctx := <-started
	old := s.driverWork.results
	s.Cancel()
	s.OpenNew(connections.H2, "")
	s.connectionDraft.Name = "new original"
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("driver import context was not revoked")
	}
	close(finish)
	select {
	case <-old:
	case <-time.After(time.Second):
		t.Fatal("original import did not settle")
	}
	s.consumeDriverWork()
	if s.connectionDraft.Name != "new original" || s.connectionDraft.H2Options.DriverVersion != "" || s.driverWork.results != nil {
		t.Fatal("late driver import selected a different form")
	}
	if len(s.driverWork.packages) != 0 {
		t.Fatal("closed driver inventory retained original form snapshots")
	}
}

func TestH2ModeAndProfileValidationPreservesPinsAndRejectsUnsupportedCapacity(t *testing.T) {
	s := New(Dependencies{})
	s.OpenNew(connections.H2, "")
	s.connectionDraft.Name = "H2 memory"
	d := h2UnitDriver(t)
	s.selectH2Driver(d)
	s.setH2Mode("memory")
	p, err := s.draftProfile()
	if err != nil || p.Host != "" || p.Port != 0 || p.Database != "nyadb" || p.H2Options.RequireExisting {
		t.Fatal("H2 memory inherited network/file semantics", err)
	}
	s.h2Numbers[5] = "7"
	if _, err := s.draftProfile(); err == nil {
		t.Fatal("H2 SQL limit exceeded workload capacity")
	}
	s.h2Numbers[5] = "6"
	wrong := d
	wrong.Version = "wrong"
	wrong.Profiles = []string{"h2-v1"}
	s.selectH2Driver(wrong)
	if s.connectionDraft.H2Options.DriverVersion != d.Version || s.h2DriverChoice != d.Version {
		t.Fatal("unsupported driver selection changed original pin")
	}
}

func TestH2TypedFilePathUsesJDBCBaseWithoutChangingRawDraft(t *testing.T) {
	for _, suffix := range []string{".mv.db", ".H2.DB"} {
		t.Run(suffix, func(t *testing.T) {
			s := New(Dependencies{})
			t.Cleanup(s.Close)
			if err := s.OpenNew(connections.H2, ""); err != nil {
				t.Fatal(err)
			}
			s.connectionDraft.Name = "typed file"
			s.selectH2Driver(h2UnitDriver(t))
			base := filepath.Join(t.TempDir(), "typed")
			raw := "  " + base + suffix + "  "
			s.connectionDraft.Database = raw
			p, err := s.draftProfile()
			if err != nil || p.Database != base || s.connectionDraft.Database != raw {
				t.Fatal("typed H2 file lost original input or retained the physical suffix", err)
			}
		})
	}
}

func TestH2InventoryRefreshPreservesMissingOriginalPinAndNeverSelectsLatest(t *testing.T) {
	d := h2UnitDriver(t)
	s := New(Dependencies{DriverPackages: DriverPackages{List: func(ctx context.Context, kind connections.Kind, directory string) ([]connections.ManagedDriver, error) {
		return []connections.ManagedDriver{d}, nil
	}}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.H2, ""); err != nil {
		t.Fatal(err)
	}
	settle := func() {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for s.driverWork.results != nil && time.Now().Before(deadline) {
			s.consumeDriverWork()
			time.Sleep(time.Millisecond)
		}
		if s.driverWork.results != nil {
			t.Fatal("original inventory did not settle")
		}
	}
	settle()
	if len(s.driverWork.packages) != 1 || s.connectionDraft.H2Options.DriverVersion != "" {
		t.Fatal("listing selected a driver without an explicit choice")
	}
	s.selectH2Driver(d)
	s.connectionDraft.H2Options.DriverVersion = "original-missing"
	original := s.connectionDraft.H2Options
	s.RefreshDriverPackages()
	settle()
	if s.connectionDraft.H2Options != original || s.driverWork.message == "" {
		t.Fatal("refresh silently replaced or hid the missing original pin")
	}
}

func TestH2URIBackRefreshesInventoryWithoutSelectingRevokedImport(t *testing.T) {
	d := h2UnitDriver(t)
	started, finish := make(chan context.Context, 1), make(chan struct{})
	s := New(Dependencies{DriverPackages: DriverPackages{
		List: func(context.Context, connections.Kind, string) ([]connections.ManagedDriver, error) {
			return []connections.ManagedDriver{d}, nil
		},
		Import: func(ctx context.Context, _ connections.Kind, _, _ string) (connections.ManagedDriver, error) {
			started <- ctx
			<-finish
			return d, nil
		},
	}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.H2, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionSecret = "original-temporary-secret"
	s.h2Numbers[0] = "original-invalid-input"
	s.ImportDriverPackage("original.zip")
	ctx := <-started
	original := s.driverWork.results
	s.BeginURIFromEditor()
	if ctx.Err() != context.Canceled || s.driverWork.results != nil {
		t.Fatal("parking the editor did not revoke the original import")
	}
	close(finish)
	select {
	case <-original:
	case <-time.After(time.Second):
		t.Fatal("original import did not acknowledge cancellation")
	}
	s.RefreshDriverPackages()
	if s.driverWork.results != nil {
		t.Fatal("parked URI restarted driver work")
	}
	s.ReturnFromURI()
	deadline := time.Now().Add(time.Second)
	for s.driverWork.results != nil && time.Now().Before(deadline) {
		s.consumeDriverWork()
		time.Sleep(time.Millisecond)
	}
	if s.driverWork.results != nil || len(s.driverWork.packages) != 1 || s.connectionDraft.H2Options.DriverVersion != "" || s.connectionSecret != "original-temporary-secret" || s.h2Numbers[0] != "original-invalid-input" {
		t.Fatal("URI return lost original inputs or selected the revoked import")
	}
}
