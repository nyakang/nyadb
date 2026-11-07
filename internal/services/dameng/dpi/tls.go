package dpi

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TLSOptions are explicit public files/settings, never passwords. A nil policy
// preserves the private loopback verification connector. An explicit policy
// defaults to verify-full and controls the actual remote TLS connection in Go.
type TLSOptions struct {
	Mode            string
	CAFile          string
	CertificateFile string
	KeyFile         string
	ServerName      string
}

type localTLS struct {
	directory string
	server    *tls.Config
}

// Native TLS protects the private local socket. Remote Go TLS independently
// verifies the original endpoint with the caller's trust and client identity.
func newLocalTLS(username string) (*localTLS, error) {
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "NyaDB private native session CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	makeCertificate := func(subject string, usage x509.ExtKeyUsage, ip bool) ([]byte, error) {
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return nil, err
		}
		certificate := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: subject}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		if ip {
			certificate.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		} else {
			certificate.DNSNames = []string{subject}
		}
		return x509.CreateCertificate(rand.Reader, certificate, ca, &key.PublicKey, caKey)
	}
	serverDER, err := makeCertificate("127.0.0.1", x509.ExtKeyUsageServerAuth, true)
	if err != nil {
		return nil, err
	}
	clientDER, err := makeCertificate(username, x509.ExtKeyUsageClientAuth, false)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp("", "nyadb-dpi-tls-")
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(directory)
		}
	}()
	for name, data := range map[string][]byte{
		"ca-cert.pem":     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		"client-cert.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}),
		"client-key.pem":  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	} {
		if err = os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
			return nil, err
		}
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	complete = true
	return &localTLS{directory: directory, server: &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{serverDER, caDER}, PrivateKey: key}}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}}, nil
}

func remoteTLS(host string, o TLSOptions) (*tls.Config, TLSOptions, error) {
	o.Mode = strings.ToLower(strings.TrimSpace(o.Mode))
	if o.Mode == "" {
		o.Mode = "verify-full"
	}
	switch o.Mode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return nil, o, errors.New("unsupported explicit DM8 TLS mode")
	}
	if strings.ContainsRune(o.ServerName, 0) || len(o.ServerName) > 255 || strings.ContainsAny(o.ServerName, "/@?#[]\\ \t\r\n") {
		return nil, o, errors.New("invalid DM8 TLS peer name")
	}
	if o.Mode == "disable" {
		if o.CAFile != "" || o.CertificateFile != "" || o.KeyFile != "" || o.ServerName != "" {
			return nil, o, errors.New("DM8 certificates and peer identity require TLS")
		}
		return nil, o, nil
	}
	if (o.CertificateFile == "") != (o.KeyFile == "") {
		return nil, o, errors.New("DM8 client certificate and key must be paired")
	}
	for _, path := range []string{o.CAFile, o.CertificateFile, o.KeyFile} {
		if path != "" && (!filepath.IsAbs(path) || strings.ContainsRune(path, 0)) {
			return nil, o, errors.New("DM8 TLS requires explicit absolute certificate paths")
		}
	}
	name := o.ServerName
	if name == "" {
		name = host
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: name}
	if o.CAFile != "" {
		data, err := os.ReadFile(o.CAFile)
		if err != nil || len(data) > 1<<20 {
			return nil, o, errors.New("DM8 trusted CA file is unavailable")
		}
		config.RootCAs = x509.NewCertPool()
		if !config.RootCAs.AppendCertsFromPEM(data) {
			return nil, o, errors.New("DM8 trusted CA file contains no certificates")
		}
	}
	if o.CertificateFile != "" {
		certificate, err := tls.LoadX509KeyPair(o.CertificateFile, o.KeyFile)
		if err != nil {
			return nil, o, errors.New("DM8 client certificate or private key is invalid")
		}
		config.Certificates = []tls.Certificate{certificate}
		// An explicitly selected identity must not be silently omitted when
		// the server advertises a different issuer list.
		config.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &certificate, nil
		}
	}
	if o.Mode == "verify-ca" || o.Mode == "require" {
		config.InsecureSkipVerify = true // The explicit policy below verifies chains.
		if o.Mode == "verify-ca" || o.CAFile != "" {
			config.VerifyConnection = func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return errors.New("DM8 TLS server certificate is missing")
				}
				intermediates := x509.NewCertPool()
				for _, certificate := range state.PeerCertificates[1:] {
					intermediates.AddCert(certificate)
				}
				_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: config.RootCAs, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
				return err
			}
		}
	}
	return config, o, nil
}
