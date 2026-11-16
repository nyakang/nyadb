package connectioneditor

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// DriverPackages contains injected installed-package operations. Import uses
// an explicitly chosen local package; returned snapshots contain no secrets.
type DriverPackages struct {
	List       func(context.Context, connections.Kind, string) ([]connections.ManagedDriver, error)
	Import     func(context.Context, connections.Kind, string, string) (connections.ManagedDriver, error)
	ImportJDBC func(context.Context, connections.Kind, string, connections.JDBCImport) (connections.ManagedDriver, error)
}

type driverWorkResult struct {
	generation                                  uint64
	kind                                        connections.Kind
	directory, profile                          string
	packages                                    []connections.ManagedDriver
	imported                                    *connections.ManagedDriver
	err                                         error
	jdbc                                        *connections.JDBCImport
	baseChoice, rawClass, pinVersion, pinSHA256 string
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
	if !s.active || s.uriOpen || s.connectionDraft.Kind != connections.H2 && s.connectionDraft.Kind != connections.ZooKeeper && (s.connectionDraft.Kind != connections.Trino || s.connectionDraft.TrinoOptions.Mode != "jdbc") {
		return
	}
	s.cancelDriverWork()
	service := s.driverPackages
	if file == "" && service.List == nil || file != "" && service.Import == nil {
		return
	}
	_, _, directory, profile := s.currentDriverSelection()
	kind := s.connectionDraft.Kind
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	results := make(chan driverWorkResult, 1)
	s.driverWork.cancel, s.driverWork.results, s.driverWork.message = cancel, results, ""
	generation := s.driverWork.generation
	go func() {
		defer cancel()
		out := driverWorkResult{generation: generation, kind: kind, directory: directory, profile: profile}
		if file == "" {
			out.packages, out.err = service.List(ctx, kind, directory)
			out.packages = cloneDrivers(out.packages)
		} else {
			d, err := service.Import(ctx, kind, directory, file)
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

func (s *State) ImportJDBCDriver() {
	if !s.active || s.uriOpen || s.connectionDraft.Kind != connections.H2 || s.connectionDraft.H2Options.DriverProfile != "h2-custom" || s.driverPackages.ImportJDBC == nil {
		return
	}
	a := s.connectionDraft.H2Options
	base := s.h2CustomBase
	if base == "" {
		base = a.DriverVersion
	}
	var original connections.ManagedDriver
	for _, d := range s.driverWork.packages {
		if d.Version == base {
			original = cloneDrivers([]connections.ManagedDriver{d})[0]
			break
		}
	}
	if original.Key != "h2" || len(s.h2CustomPaths) == 0 {
		s.driverWork.message = "请选择基础驱动包和 JDBC JAR。"
		return
	}
	s.cancelDriverWork()
	request := connections.JDBCImport{BaseVersion: original.Version, BaseSHA256: original.SHA256, Profile: "h2-custom", Class: strings.TrimSpace(s.h2CustomClass), Paths: append([]string(nil), s.h2CustomPaths...)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	results := make(chan driverWorkResult, 1)
	s.driverWork.cancel, s.driverWork.results, s.driverWork.message = cancel, results, ""
	out := driverWorkResult{generation: s.driverWork.generation, kind: s.connectionDraft.Kind, directory: a.DriverDirectory, profile: a.DriverProfile, jdbc: &request, baseChoice: s.h2CustomBase, rawClass: s.h2CustomClass, pinVersion: a.DriverVersion, pinSHA256: a.DriverSHA256}
	service := s.driverPackages.ImportJDBC
	go func() {
		defer cancel()
		owned := request
		owned.Paths = append([]string(nil), request.Paths...)
		d, err := service(ctx, out.kind, original.Directory, owned)
		copy := cloneDrivers([]connections.ManagedDriver{d})[0]
		out.imported, out.err = &copy, err
		if out.err == nil {
			out.err = ctx.Err()
		}
		results <- out
	}()
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
		selectedVersion, selectedSHA, selectedDirectory, selectedProfile := s.currentDriverSelection()
		if !s.active || out.generation != s.driverWork.generation || out.kind != s.connectionDraft.Kind || out.directory != selectedDirectory {
			return
		}
		if out.jdbc != nil && (out.profile != s.connectionDraft.H2Options.DriverProfile || out.baseChoice != s.h2CustomBase || out.rawClass != s.h2CustomClass || out.pinVersion != s.connectionDraft.H2Options.DriverVersion || out.pinSHA256 != s.connectionDraft.H2Options.DriverSHA256 || !slices.Equal(out.jdbc.Paths, s.h2CustomPaths)) {
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
			if out.profile == selectedProfile {
				if out.kind == connections.Trino {
					s.selectTrinoDriver(d)
				} else if out.kind == connections.ZooKeeper {
					s.selectZooKeeperDriver(d)
				} else {
					if out.profile == "h2-custom" && !d.Supports("h2-custom") && out.jdbc == nil {
						s.h2CustomBase = d.Version
					} else {
						s.selectH2Driver(d)
					}
				}
			}
		} else {
			s.driverWork.packages = out.packages
		}
		selectedVersion, selectedSHA, _, selectedProfile = s.currentDriverSelection()
		if selectedVersion != "" {
			found := false
			for _, d := range s.driverWork.packages {
				if d.Version == selectedVersion && d.SHA256 == selectedSHA {
					found = true
					if !d.Supports(selectedProfile) {
						s.driverWork.message = "驱动包不包含当前 H2 版本，请选择匹配的版本。"
						if out.kind == connections.Trino {
							s.driverWork.message = "驱动包不包含 Trino 驱动。"
						}
						if out.kind == connections.ZooKeeper {
							s.driverWork.message = "驱动包不包含 ZooKeeper 原生驱动。"
						}
					}
					if selectedProfile == "h2-custom" && d.JDBCClass != "" && len(s.h2CustomPaths) == 0 && s.h2CustomClass == "org.h2.Driver" {
						s.h2CustomClass = d.JDBCClass
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

func (s *State) currentDriverSelection() (version, digest, directory, profile string) {
	if s.connectionDraft.Kind == connections.ZooKeeper {
		a := s.connectionDraft.ZooKeeperOptions
		return a.DriverVersion, a.DriverSHA256, a.DriverDirectory, "zookeeper-v1"
	}
	if s.connectionDraft.Kind == connections.Trino {
		a := s.connectionDraft.TrinoOptions
		return a.DriverVersion, a.DriverSHA256, a.DriverDirectory, "trino"
	}
	a := s.connectionDraft.H2Options
	return a.DriverVersion, a.DriverSHA256, a.DriverDirectory, a.DriverProfile
}
