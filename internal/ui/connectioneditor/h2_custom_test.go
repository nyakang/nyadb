package connectioneditor

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestH2CustomImportCapturesOriginalInputsAndRejectsChangedCompletion(t *testing.T) {
	for _, change := range []string{"unchanged", "class", "path", "base", "pin", "reopened"} {
		t.Run(change, func(t *testing.T) {
			base := h2UnitDriver(t)
			imported := base
			imported.Version, imported.SHA256, imported.JDBCClass = "custom-original", strings.Repeat("b", 64), "org.h2.Driver"
			imported.Profiles = append(append([]string(nil), base.Profiles...), "h2-custom")
			started, finish := make(chan connections.JDBCImport, 1), make(chan struct{})
			s := New(Dependencies{DriverPackages: DriverPackages{ImportJDBC: func(ctx context.Context, kind connections.Kind, directory string, request connections.JDBCImport) (connections.ManagedDriver, error) {
				if kind != connections.H2 || directory != base.Directory {
					t.Error("original product/store changed")
				}
				owned := request
				owned.Paths = append([]string(nil), request.Paths...)
				started <- owned
				request.Paths[0] = "service-mutated-request"
				<-finish
				return imported, nil
			}}})
			t.Cleanup(s.Close)
			if err := s.OpenNew(connections.H2, ""); err != nil {
				t.Fatal(err)
			}
			s.selectH2Driver(base)
			s.connectionDraft.H2Options.DriverProfile = "h2-custom"
			s.driverWork.packages = []connections.ManagedDriver{base}
			s.h2CustomBase, s.h2CustomClass = base.Version, "org.h2.Driver"
			s.h2CustomPaths = []string{filepath.Join(t.TempDir(), "original.jar")}
			originalPaths := append([]string(nil), s.h2CustomPaths...)
			s.ImportJDBCDriver()
			request := <-started
			if request.BaseVersion != base.Version || request.BaseSHA256 != base.SHA256 || request.Profile != "h2-custom" || request.Class != s.h2CustomClass || !slices.Equal(request.Paths, originalPaths) {
				t.Fatal("worker lost original import snapshot")
			}
			originalResult := s.driverWork.results
			switch change {
			case "class":
				s.h2CustomClass = "different.Driver"
			case "path":
				s.h2CustomPaths[0] = "new-path.jar"
			case "base":
				s.h2CustomBase = "different-base"
			case "pin":
				s.connectionDraft.H2Options.DriverSHA256 = strings.Repeat("c", 64)
			case "reopened":
				s.Cancel()
				s.OpenNew(connections.H2, "")
			}
			close(finish)
			if change == "reopened" {
				select {
				case <-originalResult:
				case <-time.After(time.Second):
					t.Fatal("revoked original result did not settle")
				}
			} else {
				deadline := time.Now().Add(time.Second)
				for s.driverWork.results != nil && time.Now().Before(deadline) {
					s.consumeDriverWork()
					time.Sleep(time.Millisecond)
				}
				if s.driverWork.results != nil {
					t.Fatal("original UI completion did not settle")
				}
			}
			if change == "unchanged" {
				if s.connectionDraft.H2Options.DriverVersion != imported.Version || s.connectionDraft.H2Options.DriverSHA256 != imported.SHA256 || !slices.Equal(s.h2CustomPaths, originalPaths) {
					t.Fatal("unchanged original import lost its complete pin or caller mutation reached UI")
				}
			} else if s.connectionDraft.H2Options.DriverVersion == imported.Version {
				t.Fatal("late import replaced changed or reopened original form")
			}
		})
	}
}

func TestH2CustomJARPickerAndURIRetainOriginalFormWithoutOldCompletion(t *testing.T) {
	s := New(Dependencies{})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.H2, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.H2Options.DriverProfile = "h2-custom"
	s.h2CustomBase, s.h2CustomClass = "original-base", "original.Driver"
	path := filepath.Join(t.TempDir(), "original.jar")
	s.SelectEngineFile(H2CustomJARFile, -1, s.EngineFileVersion(), path)
	old := s.EngineFileVersion()
	s.BeginURIFromEditor()
	if err := s.ImportURI("jdbc:h2:mem:replacement"); err != nil {
		t.Fatal(err)
	}
	if s.connectionDraft.H2Options.DriverProfile != "h2-custom" || s.h2CustomBase != "original-base" || s.h2CustomClass != "original.Driver" || !slices.Equal(s.h2CustomPaths, []string{path}) {
		t.Fatal("URI replacement discarded original driver choice or raw import inputs")
	}
	s.SelectEngineFile(H2CustomJARFile, -1, old, "late.jar")
	if len(s.h2CustomPaths) != 1 {
		t.Fatal("old picker changed returned original form")
	}
}
