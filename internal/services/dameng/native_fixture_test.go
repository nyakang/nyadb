package dameng

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeFixture(t *testing.T) (connections.DamengConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_DAMENG_DPI_IT") != "1" {
		t.Skip("requires an explicitly enabled owned original DM8 fixture")
	}
	data, err := os.ReadFile(os.Getenv("NYADB_DAMENG_DPI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Endpoint     string `json:"endpoint"`
		PasswordFile string `json:"password_file"`
		SSL          string `json:"native_ssl_directory"`
	}
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(f.Endpoint)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires a fixture on its captured loopback port")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	password, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	a := connections.DefaultDamengOptions()
	a.LibraryFile = os.Getenv("NYADB_DAMENG_DPI_LIBRARY")
	a.MaxConnections, a.SQLMaxConnections = 2, 2
	mode := "disable"
	if f.SSL != "" {
		mode = "verify-full"
		a.CAFile = filepath.Join(f.SSL, "ca-cert.pem")
		a.CertificateFile = filepath.Join(f.SSL, "client-cert.pem")
		a.KeyFile = filepath.Join(f.SSL, "client-key.pem")
	}
	c, err := connections.NormalizeDamengConfig(connections.DamengConfig{Host: host, Port: uint16(n), Database: "DAMENG", Username: "SYSDBA", TLSMode: mode, Options: a})
	if err != nil {
		t.Fatal(err)
	}
	return c, string(password)
}

func nativeSuffix(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}
