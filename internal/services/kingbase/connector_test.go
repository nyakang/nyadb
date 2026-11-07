package kingbase

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNativeKingbaseTLSUsesExplicitCertificatesAndVerification(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	file := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	c := connections.KingbaseConfig{Host: "owned.invalid", Database: "test", Username: "user", Options: connections.DefaultPostgreSQLOptions()}
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		c.TLSMode = mode
		c.Options.CAFile = file
		cfg, err := nativeTLS(c)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MinVersion != tls.VersionTLS12 || cfg.ServerName != c.Host || cfg.RootCAs == nil {
			t.Fatal("native TLS did not capture explicit identity and CA")
		}
		if mode == "verify-full" && cfg.InsecureSkipVerify {
			t.Fatal("verify-full disabled verification")
		}
		if mode != "verify-full" {
			if cfg.VerifyConnection == nil {
				t.Fatal("configured CA was ignored")
			}
			if err = cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{server.Certificate()}}); err != nil {
				t.Fatal("explicit CA was rejected", err)
			}
			if err = cfg.VerifyConnection(tls.ConnectionState{}); err == nil {
				t.Fatal("missing native certificate accepted")
			}
		}
	}
	for _, key := range []string{"KBHOSTADDR", "KBSERVICE", "KBSSLCRL", "KBPASSWORD", "KBSSLCERT", "KBSSLKEY", "KBPASSFILE"} {
		t.Setenv(key, "ambient-invalid-value")
	}
	c.TLSMode = "disable"
	c.Options.CAFile = ""
	if _, err := NewConnector(c, "", nil); err != nil {
		t.Fatal("explicit transport used ambient options", err)
	}
	c.TLSMode = "verify-full"
	c.Options.CAFile = filepath.Join(t.TempDir(), "missing.pem")
	if _, err := NewConnector(c, "private-password", nil); err == nil {
		t.Fatal("missing explicit CA accepted")
	}
}
