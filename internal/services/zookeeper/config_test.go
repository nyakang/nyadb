package zookeeper

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCapturedNamespaceRoutesAndTrustOwnOriginalConfiguration(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	pair := server.TLS.Certificates[0]
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]})
	key, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	private := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
	directory := t.TempDir()
	ca, client, clientKey := filepath.Join(directory, "ca.pem"), filepath.Join(directory, "client.pem"), filepath.Join(directory, "key.pem")
	for path, raw := range map[string][]byte{ca: cert, client: cert, clientKey: private} {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := Config{ConnectString: "zookeeper://original:2181/chroot", Namespace: "nested/", Prefix: "suggestion/", CACertPath: ca, ClientCertPath: client, ClientKeyPath: clientKey, EndpointRoutes: map[string]string{"original:2181": "127.0.0.1:2182"}}
	captured, err := captureConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(captured.directory)
	if captured.root != "/chroot/nested" || captured.config.Prefix != "/suggestion" || !captured.config.SSL || captured.config.EndpointRoutes["original:2181"] != "127.0.0.1:2182" {
		t.Fatal("captured logical root or endpoint changed")
	}
	// Replacing source files or mutating caller-owned route maps after opening
	// must not redirect the next original document's connection or trust.
	c.EndpointRoutes["original:2181"] = "127.0.0.1:9999"
	for _, path := range []string{ca, client, clientKey} {
		if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for path, want := range map[string][]byte{captured.config.CACertPath: cert, captured.config.ClientCertPath: cert, captured.config.ClientKeyPath: private} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("captured trust followed original path replacement")
		}
		info, err := os.Stat(path)
		if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal("captured trust file permissions changed")
		}
	}
	if captured.config.EndpointRoutes["original:2181"] != "127.0.0.1:2182" {
		t.Fatal("captured routes alias caller map")
	}
}

func TestNativeConfigurationRejectsAmbiguousRoutesAndUnsupportedIdentity(t *testing.T) {
	for _, c := range []Config{
		{ConnectString: "original:2181,other:2181", EndpointRoutes: map[string]string{"original:2181": "127.0.0.1:2182"}},
		{ConnectString: "original:2181", EndpointRoutes: map[string]string{"original:2181": "192.0.2.1:2182"}},
		{ConnectString: "original:2181", Namespace: "/a/../b"},
		{ConnectString: "original:2181", AuthScheme: "sasl"},
		{ConnectString: "original:2181", ClientCertPath: "unpaired"},
		{ConnectString: "user:password@original:2181"},
		{ConnectString: "original:2181,original:2181"},
	} {
		if _, _, err := normalizeConfig(c); !errors.Is(err, ErrInvalid) {
			t.Fatal("ambiguous or unsupported original configuration admitted", err)
		}
	}
}
