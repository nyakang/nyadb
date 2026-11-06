package oracle

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"github.com/nyakang/nyadb/internal/services/connections"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOracleTCPSValidatesConfiguredServerIdentity(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Oracle fixture CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "ca.pem")
	if err = os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"oracle.fixture"}, NotBefore: root.NotBefore, NotAfter: root.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err = x509.CreateCertificate(rand.Reader, leaf, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.Oracle)
	p.Host = "127.0.0.1"
	p.TLSMode = "verify-full"
	p.OracleOptions.CAFile = file
	p.OracleOptions.ServerName = "oracle.fixture"
	c, err := connector(p, "temporary-fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	if err = c.tls.VerifyConnection(state); err != nil {
		t.Fatal("valid override server name rejected", err)
	}
	p.OracleOptions.ServerName = "wrong.fixture"
	c, err = connector(p, "temporary-fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.tls.VerifyConnection(state); err == nil {
		t.Fatal("wrong server identity accepted")
	}
	p.OracleOptions.ServerName = "oracle.fixture"
	p.OracleOptions.CAFile = ""
	c, err = connector(p, "temporary-fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.tls.VerifyConnection(state); err == nil {
		t.Fatal("untrusted issuer accepted")
	}
}
