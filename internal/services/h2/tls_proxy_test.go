package h2

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func testTLSCertificate(t *testing.T, ip net.IP, expired bool) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	if expired {
		expires = time.Now().Add(-time.Hour)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "owned H2 TLS fixture"}, NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: expires, IPAddresses: []net.IP{ip}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, path
}

func testTLSEcho(t *testing.T, cert tls.Certificate) (string, uint16) {
	t.Helper()
	listener, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	n, _ := strconv.ParseUint(port, 10, 16)
	return host, uint16(n)
}

func TestH2TLSVerifiesChainOriginalHostExpiryAndCapturedTrust(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		ip                         net.IP
		expired, trusted, accepted bool
	}{
		{"trusted original", net.ParseIP("127.0.0.1"), false, true, true},
		{"unknown chain", net.ParseIP("127.0.0.1"), false, false, false},
		{"wrong original host", net.ParseIP("127.0.0.2"), false, true, false},
		{"expired certificate", net.ParseIP("127.0.0.1"), true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert, ca := testTLSCertificate(t, tc.ip, tc.expired)
			host, port := testTLSEcho(t, cert)
			profile := connections.Profile{Host: host, Port: port, H2Options: connections.DefaultH2Options()}
			profile.H2Options.Mode = "ssl"
			if tc.trusted {
				profile.H2Options.TLSCAFile = ca
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			proxy, err := startTLSProxy(ctx, profile)
			if !tc.accepted {
				if !errors.Is(err, errTLSTrust) || proxy != nil {
					t.Fatal("invalid TLS trust admitted", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(proxy.Close)
			// Active runtime trust is an immutable certificate snapshot.
			if err := os.WriteFile(ca, []byte("changed invalid trust"), 0600); err != nil {
				t.Fatal(err)
			}
			local, port := proxy.endpoint()
			conn, err := net.Dial("tcp", net.JoinHostPort(local, strconv.Itoa(int(port))))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			_, err = conn.Write([]byte("original wire"))
			if err != nil {
				t.Fatal(err)
			}
			received := make([]byte, len("original wire"))
			if _, err = io.ReadFull(conn, received); err != nil || string(received) != "original wire" {
				t.Fatal("original stream changed", err)
			}
			proxy.Close()
			proxy.Close()
			if _, err = conn.Read(make([]byte, 1)); err == nil {
				t.Fatal("closed original proxy retained a stream")
			}
			if late, err := net.DialTimeout("tcp", net.JoinHostPort(local, strconv.Itoa(int(port))), time.Second); err == nil {
				late.Close()
				t.Fatal("closed proxy implicitly recovered")
			}
		})
	}
}
