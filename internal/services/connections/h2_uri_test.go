package connections_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestH2JDBCURIKeepsNativeModesCredentialsAndExplicitFileOptions(t *testing.T) {
	base := filepath.Join(t.TempDir(), "chosen file")
	file := "jdbc:h2:file:" + filepath.ToSlash(base) + ".mv.db"
	for _, tc := range []struct {
		uri, mode, database, host string
		port                      uint16
		shared, existing, ro      bool
	}{
		{file + ";USER=sa;PASSWORD=original%3B%3D%2Bsecret;AUTO_SERVER=TRUE", "file", base, "", 0, true, true, false},
		{file + ";ACCESS_MODE_DATA=r;AUTO_SERVER=FALSE", "file", base, "", 0, false, true, true},
		{file + ";IFEXISTS=FALSE", "file", base, "", 0, true, false, false},
		{"jdbc:h2:mem:original;DB_CLOSE_DELAY=-1", "memory", "original", "", 0, false, false, false},
		{"jdbc:h2:tcp://[::1]:9192/owned%20db;USER=sa", "tcp", "owned db", "::1", 9192, false, true, false},
		{"jdbc:h2:ssl://localhost/owned", "ssl", "owned", "localhost", 9092, false, true, false},
	} {
		t.Run(tc.mode+fmt.Sprint(tc.ro, tc.existing), func(t *testing.T) {
			got, err := connections.ParseURI(tc.uri)
			p, a := got.Profile, got.Profile.H2Options
			if err != nil || p.Kind != connections.H2 || a.Mode != tc.mode || p.Database != tc.database || p.Host != tc.host || p.Port != tc.port || a.AutoServer != tc.shared || a.RequireExisting != tc.existing || a.ReadOnly != tc.ro || a.DriverVersion != "" || a.DriverSHA256 != "" {
				t.Fatal("native JDBC configuration changed or selected an unrequested package", err)
			}
			if strings.Contains(tc.uri, "PASSWORD=") {
				if got.Password != "original;=+secret" {
					t.Fatal("original temporary password changed")
				}
				raw, _ := json.Marshal(got)
				if strings.Contains(string(raw), "secret") || strings.Contains(fmt.Sprintf("%#v", got), "secret") {
					t.Fatal("H2 URI exposed a temporary credential")
				}
			}
		})
	}
}

func TestH2JDBCURIRejectsUnexecutedOptionsAndConflictingIdentity(t *testing.T) {
	base := "jdbc:h2:file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "original"))
	for _, uri := range []string{
		base + ";USER=sa;user=other", base + ";PASSWORD=x;password=y",
		base + ";AUTO_SERVER=TRUE;ACCESS_MODE_DATA=r", base + ";AUTO_SERVER=1",
		base + ";INIT=RUNSCRIPT FROM 'unsafe'", base + ";CIPHER=AES", base + ";SCHEMA=unverified",
		base + ";AUTO_SERVER_PORT=9093", base + ";PASSWORD=" + url.PathEscape("original\nsecret"),
		"jdbc:h2:mem:owned;AUTO_SERVER=TRUE", "jdbc:h2:mem:owned;IFEXISTS=TRUE",
		"jdbc:h2:tcp://localhost:0/db", "jdbc:h2:tcp://localhost:/db",
		"jdbc:h2:tcp://user:secret@localhost/db", "jdbc:h2:tcp://localhost/db?INIT=unsafe",
		"jdbc:h2:split:12:/unimplemented", "jdbc:h2:file:relative", base + "%3BINIT=unsafe",
	} {
		got, err := connections.ParseURI(uri)
		if err == nil || got.Profile.Kind != "" || got.Password != "" || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), uri) {
			t.Fatal("invalid JDBC URI admitted or exposed credentials")
		}
	}
}
