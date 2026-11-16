package connectioneditor

import (
	"context"
	"errors"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"strings"
	"testing"
	"time"
)

func trinoUnitDriver(t *testing.T) connections.ManagedDriver {
	t.Helper()
	return connections.ManagedDriver{Key: "trino", Version: "jdbc-471", SHA256: strings.Repeat("a", 64), Directory: t.TempDir(), Profiles: []string{"trino"}}
}

func TestTrinoManagedDriverAndCapacityValidationPreserveOriginalPin(t *testing.T) {
	s := New(Dependencies{})
	t.Cleanup(s.Close)
	s.OpenNew(connections.Trino, "")
	s.connectionDraft.Name = "original"
	s.setTrinoMode("jdbc")
	if _, err := s.draftProfile(); err == nil {
		t.Fatal("missing package pin saved")
	}
	d := trinoUnitDriver(t)
	s.selectTrinoDriver(d)
	if _, err := s.draftProfile(); err != nil {
		t.Fatal(err)
	}
	original := s.connectionDraft.TrinoOptions
	wrong := d
	wrong.Key = "h2"
	wrong.Version = "wrong"
	s.selectTrinoDriver(wrong)
	if s.connectionDraft.TrinoOptions != original || s.trinoDriverChoice != d.Version {
		t.Fatal("foreign driver replaced original pin")
	}
	for _, bad := range []struct {
		index int
		value string
	}{{0, "0"}, {1, "-1"}, {2, "0"}, {3, "0"}, {4, "8"}, {5, "7"}, {5, "0"}, {0, "text"}} {
		old := s.trinoNumbers[bad.index]
		s.trinoNumbers[bad.index] = bad.value
		if _, err := s.draftProfile(); err == nil {
			t.Fatal("invalid explicit capacity accepted", bad)
		}
		s.trinoNumbers[bad.index] = old
	}
	s.setTrinoMode("http")
	s.setTrinoMode("jdbc")
	if s.connectionDraft.TrinoOptions != original {
		t.Fatal("mode switch discarded original pin")
	}
	s.BeginURIFromEditor()
	if err := s.ImportURI("trino://reader@localhost/memory/default?mode=http"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDraft.TrinoOptions.DriverSHA256 != d.SHA256 || s.connectionDraft.TrinoOptions.Mode != "jdbc" {
		t.Fatal("same editor URI replaced selected driver")
	}
}

func TestTrinoManagedImportRevokedByModeURIAndNewForm(t *testing.T) {
	for _, action := range []string{"mode", "uri", "new-form"} {
		t.Run(action, func(t *testing.T) {
			started := make(chan context.Context, 1)
			finish := make(chan struct{})
			d := trinoUnitDriver(t)
			s := New(Dependencies{DriverPackages: DriverPackages{Import: func(ctx context.Context, kind connections.Kind, dir, file string) (connections.ManagedDriver, error) {
				if kind != connections.Trino || dir != "" || file != "original.zip" {
					t.Error("immutable request changed")
				}
				started <- ctx
				<-finish
				return d, nil
			}}})
			t.Cleanup(s.Close)
			s.OpenNew(connections.Trino, "")
			s.setTrinoMode("jdbc")
			s.ImportDriverPackage("original.zip")
			ctx := <-started
			original := s.driverWork.results
			picker := s.EngineFileVersion()
			switch action {
			case "mode":
				s.setTrinoMode("http")
			case "uri":
				s.BeginURIFromEditor()
			case "new-form":
				s.Cancel()
				s.OpenNew(connections.H2, "")
			}
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("original import not revoked")
			}
			s.SelectEngineFile(TrinoDriverPackageFile, -1, picker, "late.zip")
			close(finish)
			select {
			case <-original:
			case <-time.After(time.Second):
				t.Fatal("original import did not acknowledge completion")
			}
			s.consumeDriverWork()
			if s.connectionDraft.TrinoOptions.DriverSHA256 != "" || s.connectionDraft.H2Options.DriverSHA256 != "" || s.DriverBusy() {
				t.Fatal("late import changed another form")
			}
		})
	}
}

func TestTrinoManagedSavedPinAndVaultRollbackRemainOriginal(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	t.Cleanup(s.Close)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	s.OpenNew(connections.Trino, "")
	s.setTrinoMode("jdbc")
	s.selectTrinoDriver(trinoUnitDriver(t))
	s.connectionDraft.Name = "original pin"
	s.connectionSecret = "original-secret"
	s.connectionSavePassword = true
	s.Save()
	list, err := service.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatal("save failed", err, s.Error())
	}
	p := list[0]
	s.OpenProfile(p)
	if s.connectionSecret != "original-secret" {
		t.Fatal("saved secret lost")
	}
	s.connectionDraft.Name = "replacement"
	s.connectionSecret = "wrong"
	s.connectionDraft.TrinoOptions.DriverVersion = "new-pin"
	vault.setErr = errors.New("vault locked")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.Name != p.Name || got.TrinoOptions != p.TrinoOptions || s.Error() == "" {
		t.Fatal("vault rollback changed original pin", err)
	}
	if vault.secrets[fmt.Sprintf("%d/", p.ID)] != "original-secret" {
		t.Fatal("rollback replaced original secret")
	}
}

func TestTrinoManagedAdvancedControlsAreReachableAndHTTPKeepsItsTabs(t *testing.T) {
	s := New(Dependencies{})
	t.Cleanup(s.Close)
	s.OpenNew(connections.Trino, "")
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 780) }, 720, 780)
	tt.Frame()
	if _, ok := tt.Find("高级"); ok {
		t.Fatal("legacy HTTP form unexpectedly gained JDBC settings")
	}
	if err := tt.Click("Trino 连接方式"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("受管 JDBC"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.connectionDraft.TrinoOptions.Mode != "jdbc" {
		t.Fatal("public mode selector did not change transport")
	}
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, label := range h2NumberLabels {
		box, ok := tt.Find(label)
		if !ok || box.W <= 0 || box.H <= 0 || box.X+box.W > 721 || box.Y+box.H > 781 {
			t.Fatal("advanced JDBC setting unavailable", label, box)
		}
	}
	s.setTrinoMode("http")
	tt.Frame()
	if s.connectionEditorTab != 0 {
		t.Fatal("transport switch retained obsolete tab ordinal")
	}
	if _, ok := tt.Find("高级"); ok {
		t.Fatal("JDBC settings remained in HTTP form")
	}
}
