package connections

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func localMySQLTLSFixture(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(17),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		DNSNames:              []string{"localhost"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "server-ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: cert}, path
}

func TestMySQLTLSVerifiedHandshakeAndHostnameRejection(t *testing.T) {
	cert, ca := localMySQLTLSFixture(t)
	server := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	check := func(name string, wantSuccess bool) {
		t.Helper()
		clientCfg, err := verifiedMySQLTLS(name, MySQLSSL{CACertificate: ca})
		if err != nil {
			t.Fatal(err)
		}
		if clientCfg.InsecureSkipVerify || clientCfg.ServerName != name || clientCfg.MinVersion < tls.VersionTLS12 {
			t.Fatal("TLS certificate/hostname verification disabled")
		}
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		done := make(chan error, 1)
		go func() { done <- tls.Server(a, server).Handshake() }()
		client := tls.Client(b, clientCfg)
		_ = client.SetDeadline(time.Now().Add(time.Second))
		err = client.Handshake()
		if (err == nil) != wantSuccess {
			t.Fatalf("hostname %s handshake err: %v", name, err)
		}
		client.Close()
		<-done
	}
	check("localhost", true)
	check("wronghost.test", false)
}

func TestMySQLTLSRejectsInvalidRootsAndClientKeyPair(t *testing.T) {
	_, ca := localMySQLTLSFixture(t)
	a := DefaultMySQLAdvanced()
	a.SSL.Enabled = true
	a.SSL.CACertificate = ca
	s := ActiveSettings{Host: "localhost", Port: 3306, MySQLAdvanced: a}
	cfg, err := DirectMySQLConfig(s, "")
	if err != nil || cfg.TLS == nil || cfg.TLS.InsecureSkipVerify || cfg.TLS.ServerName != "localhost" {
		t.Fatalf("verified TLS not installed in mysql.Config: %v", err)
	}
	s.TLSMode = "verify-full"
	s.MySQLAdvanced.SSL.Enabled = false
	cfg, err = DirectMySQLConfig(s, "")
	if err != nil || cfg.TLS == nil {
		t.Fatalf("verify-full not enforced: %v", err)
	}
	for _, mode := range []string{"verify-ca", "prefer"} {
		s.TLSMode = mode
		if _, err := DirectMySQLConfig(s, ""); err == nil {
			t.Fatalf("unverified/downgraded mode accepted: %s", mode)
		}
	}
	s.TLSMode = "require"
	s.MySQLAdvanced.SSL.ClientKey = filepath.Join(t.TempDir(), "not-present.pem")
	if _, err := DirectMySQLConfig(s, ""); err == nil {
		t.Fatal("unpaired client key accepted")
	}
	s.MySQLAdvanced.SSL.ClientKey = ""
	s.MySQLAdvanced.SSL.CACertificate = filepath.Join(t.TempDir(), "missing-ca.pem")
	if _, err := DirectMySQLConfig(s, ""); err == nil {
		t.Fatal("missing CA silently trusted")
	}
	s.MySQLAdvanced.SSL.CACertificate = ca
	cfg, err = DirectMySQLConfig(s, "")
	if err != nil || cfg.TLS == nil {
		t.Fatalf("require must enforce verification: %v", err)
	}
}
