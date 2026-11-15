package connectioneditor

import (
	"context"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// DriverPackages contains injected installed-package operations. Import uses
// an explicitly chosen local package; returned snapshots contain no secrets.
type DriverPackages struct {
	List   func(context.Context, connections.Kind, string) ([]connections.ManagedDriver, error)
	Import func(context.Context, connections.Kind, string, string) (connections.ManagedDriver, error)
}

type driverWorkResult struct {
	generation         uint64
	kind               connections.Kind
	directory, profile string
	packages           []connections.ManagedDriver
	imported           *connections.ManagedDriver
	err                error
}

type driverWork struct {
	generation uint64
	cancel     context.CancelFunc
	results    <-chan driverWorkResult
	packages   []connections.ManagedDriver
	message    string
}

func cloneDrivers(values []connections.ManagedDriver) []connections.ManagedDriver {
	out := append([]connections.ManagedDriver(nil), values...)
	for i := range out {
		out[i].Profiles = append([]string(nil), out[i].Profiles...)
	}
	return out
}

func (s *State) SetDriverPackages(service DriverPackages) {
	s.cancelDriverWork()
	s.driverPackages = service
	s.RefreshDriverPackages()
}

func (s *State) cancelDriverWork() {
	s.driverWork.generation++
	if s.driverWork.cancel != nil {
		s.driverWork.cancel()
	}
	s.driverWork.cancel = nil
	s.driverWork.results = nil
}

func (s *State) startDriverWork(file string) {
	if !s.active || s.uriOpen || s.connectionDraft.Kind != connections.H2 {
		return
	}
	s.cancelDriverWork()
	service := s.driverPackages
	if file == "" && service.List == nil || file != "" && service.Import == nil {
		return
	}
	a, kind := s.connectionDraft.H2Options, s.connectionDraft.Kind
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	results := make(chan driverWorkResult, 1)
	s.driverWork.cancel, s.driverWork.results, s.driverWork.message = cancel, results, ""
	generation := s.driverWork.generation
	go func() {
		defer cancel()
		out := driverWorkResult{generation: generation, kind: kind, directory: a.DriverDirectory, profile: a.DriverProfile}
		if file == "" {
			out.packages, out.err = service.List(ctx, kind, a.DriverDirectory)
			out.packages = cloneDrivers(out.packages)
		} else {
			d, err := service.Import(ctx, kind, a.DriverDirectory, file)
			out.err = err
			copy := cloneDrivers([]connections.ManagedDriver{d})[0]
			out.imported = &copy
		}
		if out.err == nil {
			out.err = ctx.Err()
		}
		results <- out
	}()
}

func (s *State) RefreshDriverPackages() { s.startDriverWork("") }
func (s *State) ImportDriverPackage(file string) {
	if file != "" {
		s.startDriverWork(file)
	}
}

// consumeDriverWork has one UI-thread consumer. Revoked original requests can
// finish into their private buffered channel without selecting a new form.
func (s *State) consumeDriverWork() {
	if s.driverWork.results == nil {
		return
	}
	select {
	case out := <-s.driverWork.results:
		s.driverWork.results, s.driverWork.cancel = nil, nil
		if !s.active || out.generation != s.driverWork.generation || out.kind != s.connectionDraft.Kind || out.directory != s.connectionDraft.H2Options.DriverDirectory {
			return
		}
		if out.err != nil {
			s.driverWork.message = "无法读取或导入驱动包，请检查文件、包类型和安装目录。"
			return
		}
		if out.imported != nil {
			d := *out.imported
			found := false
			for i, old := range s.driverWork.packages {
				if old.Version == d.Version && old.Directory == d.Directory {
					s.driverWork.packages[i], found = d, true
				}
			}
			if !found {
				s.driverWork.packages = append(s.driverWork.packages, d)
			}
			if out.profile == s.connectionDraft.H2Options.DriverProfile {
				s.selectH2Driver(d)
			}
		} else {
			s.driverWork.packages = out.packages
		}
		selected := s.connectionDraft.H2Options
		if selected.DriverVersion != "" {
			found := false
			for _, d := range s.driverWork.packages {
				if d.Version == selected.DriverVersion && d.SHA256 == selected.DriverSHA256 {
					found = true
					if !d.Supports(selected.DriverProfile) {
						s.driverWork.message = "驱动包不包含当前 H2 版本，请选择匹配的版本。"
					}
				}
			}
			if !found {
				s.driverWork.message = "所选驱动包未安装，请导入原包或选择其他版本。"
			}
		}
	default:
	}
}

func (s *State) selectH2Driver(d connections.ManagedDriver) {
	if d.Key != "h2" || !d.Supports(s.connectionDraft.H2Options.DriverProfile) {
		s.h2DriverChoice = s.connectionDraft.H2Options.DriverVersion
		s.driverWork.message = "驱动包不包含当前 H2 版本，请选择匹配的版本。"
		return
	}
	a := s.connectionDraft.H2Options
	a.DriverVersion, a.DriverSHA256, a.DriverDirectory = d.Version, d.SHA256, d.Directory
	if _, err := connections.NormalizeH2Options(a); err != nil {
		s.h2DriverChoice = s.connectionDraft.H2Options.DriverVersion
		s.driverWork.message = "无法读取或导入驱动包，请检查文件、包类型和安装目录。"
		return
	}
	s.cancelMySQLConnectionTest()
	s.connectionDraft.H2Options = a
	s.h2DriverChoice = d.Version
	s.driverWork.message = ""
}
