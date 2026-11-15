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
	base := connections.Profile{Kind: connections.H2, Name: "owned", Database: "owned", Username: "sa", H2Options: a}
	cases := []struct {
		name   string
		mutate func(*connections.Profile)
	}{
		{"url parameters", func(p *connections.Profile) { p.Database = "owned;INIT=RUNSCRIPT FROM 'remote'" }},
		{"network for memory", func(p *connections.Profile) { p.Host, p.Port = "localhost", 9092 }},
		{"readonly memory", func(p *connections.Profile) { p.H2Options.ReadOnly = true }},
		{"existing memory", func(p *connections.Profile) { p.H2Options.RequireExisting = true }},
		{"unfixed driver", func(p *connections.Profile) { p.H2Options.DriverVersion = "owned" }},
		{"relative driver store", func(p *connections.Profile) { p.H2Options.DriverDirectory = "relative" }},
		{"custom launch profile", func(p *connections.Profile) { p.H2Options.DriverProfile = "custom-executable" }},
		{"foreign tls", func(p *connections.Profile) { p.TLSMode = "disable" }},
		{"foreign engine", func(p *connections.Profile) { p.Kind = connections.MySQL }},
		{"excessive pool", func(p *connections.Profile) { p.H2Options.MaxConnections = 10000 }},
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
