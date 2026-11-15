package connections_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestH2ModesKeepExplicitDatabaseAndDriverIdentity(t *testing.T) {
	for _, mode := range []string{"file", "memory", "tcp", "ssl"} {
		t.Run(mode, func(t *testing.T) {
			a := connections.DefaultH2Options()
			a.Mode, a.RequireExisting = mode, mode != "memory"
			a.AutoServer = false
			a.AutoServer = mode == "file"
			a.DriverVersion, a.DriverSHA256 = "owned-1", strings.Repeat("a", 64)
			p := connections.Profile{Kind: connections.H2, Name: "owned", Database: "owned", Username: " sa ", H2Options: a}
			if mode == "file" {
				p.Database = filepath.Join(t.TempDir(), " database ")
			} else if mode == "tcp" || mode == "ssl" {
				p.Host, p.Port = "::1", 9092
			}
			got, err := connections.NormalizeProfile(p)
			if err != nil || got.Database != p.Database || got.Username != p.Username || got.H2Options != a {
				t.Fatal("original database/user/driver identity changed", err)
			}
		})
	}
}

func TestH2RejectsURLInjectionForeignSettingsAndIncompleteDriverPins(t *testing.T) {
	a := connections.DefaultH2Options()
	a.Mode, a.RequireExisting = "memory", false
	a.AutoServer = false
	base := connections.Profile{Kind: connections.H2, Name: "owned", Database: "owned", Username: "sa", H2Options: a}
	cases := []struct {
		name   string
		mutate func(*connections.Profile)
	}{
		{"url parameters", func(p *connections.Profile) { p.Database = "owned;INIT=RUNSCRIPT FROM 'remote'" }},
		{"network for memory", func(p *connections.Profile) { p.Host, p.Port = "localhost", 9092 }},
		{"readonly memory", func(p *connections.Profile) { p.H2Options.ReadOnly = true }},
		{"shared memory", func(p *connections.Profile) { p.H2Options.AutoServer = true }},
		{"existing memory", func(p *connections.Profile) { p.H2Options.RequireExisting = true }},
		{"unfixed driver", func(p *connections.Profile) { p.H2Options.DriverVersion = "owned" }},
		{"relative driver store", func(p *connections.Profile) { p.H2Options.DriverDirectory = "relative" }},
		{"custom launch profile", func(p *connections.Profile) { p.H2Options.DriverProfile = "custom-executable" }},
		{"foreign tls", func(p *connections.Profile) { p.TLSMode = "disable" }},
		{"TLS trust outside TLS mode", func(p *connections.Profile) { p.H2Options.TLSCAFile = filepath.Join(t.TempDir(), "ca.pem") }},
		{"foreign engine", func(p *connections.Profile) { p.Kind = connections.MySQL }},
		{"excessive pool", func(p *connections.Profile) { p.H2Options.MaxConnections = 10000 }},
		{"native pool upper bound", func(p *connections.Profile) { p.H2Options.MaxConnections = 33 }},
		{"SQL exceeds workload", func(p *connections.Profile) { p.H2Options.SQLMaxConnections = 7 }},
		{"negative reserve", func(p *connections.Profile) { p.H2Options.MetadataReserve = -1 }},
		{"no workload capacity", func(p *connections.Profile) { p.H2Options.MetadataReserve = 8 }},
		{"native wait minimum", func(p *connections.Profile) { p.H2Options.PoolWaitTimeoutMilliseconds = 249 }},
		{"native wait maximum", func(p *connections.Profile) { p.H2Options.PoolWaitTimeoutMilliseconds = 300001 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mutate(&p)
			if _, err := connections.NormalizeProfile(p); err == nil {
				t.Fatal("invalid original H2 settings admitted")
			}
		})
	}
}

func TestH2TLSCAPathPreservesExplicitRootsAndRejectsMalformedPaths(t *testing.T) {
	a := connections.DefaultH2Options()
	a.Mode, a.AutoServer = "ssl", false
	for _, path := range []string{"", filepath.Join(t.TempDir(), "original CA.pem")} {
		a.TLSCAFile = path
		got, err := connections.NormalizeH2Options(a)
		if err != nil || got != a {
			t.Fatal("original TLS roots changed", err)
		}
	}
	for _, path := range []string{"relative.pem", filepath.Join(t.TempDir(), "bad\nname.pem")} {
		a.TLSCAFile = path
		if _, err := connections.NormalizeH2Options(a); err == nil {
			t.Fatal("invalid TLS roots path admitted")
		}
	}
}

func TestH2RejectsSharedReadOnlyFiles(t *testing.T) {
	a := connections.DefaultH2Options()
	a.ReadOnly = true
	if _, err := connections.NormalizeH2Options(a); err == nil {
		t.Fatal("shared read-only file settings were admitted")
	}
	a.AutoServer = false
	if _, err := connections.NormalizeH2Options(a); err != nil {
		t.Fatal("exclusive read-only file rejected", err)
	}
}

func TestH2PoolAcceptsNativeBoundariesAndExplicitNoReserve(t *testing.T) {
	for _, limits := range [][3]int{{1, 0, 1}, {8, 2, 6}, {32, 2, 30}, {32, 0, 32}} {
		a := connections.DefaultH2Options()
		a.MaxConnections, a.MetadataReserve, a.SQLMaxConnections = limits[0], limits[1], limits[2]
		got, err := connections.NormalizeH2Options(a)
		if err != nil || got != a {
			t.Fatal("explicit native limits changed", err)
		}
	}
}
