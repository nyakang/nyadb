package dpi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func testLocalTLS(t *testing.T) (*localTLS, *tls.Config) {
	t.Helper()
	material, err := newLocalTLS("fixture-user")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(material.directory) })
	client, _, err := remoteTLS("127.0.0.1", TLSOptions{
		CAFile:          filepath.Join(material.directory, "ca-cert.pem"),
		CertificateFile: filepath.Join(material.directory, "client-cert.pem"),
		KeyFile:         filepath.Join(material.directory, "client-key.pem"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return material, client
}

func TestTLSRejectsInvalidPoliciesBeforeLoadingNativeClient(t *testing.T) {
	for _, policy := range []TLSOptions{
		{Mode: "prefer"}, {Mode: "allow"}, {Mode: "disable", ServerName: "peer"},
		{Mode: "disable", CAFile: "/ca.pem"}, {CertificateFile: "/cert.pem"},
		{KeyFile: "/key.pem"}, {CAFile: "relative.pem"},
		{CAFile: "/abs\x00.pem"}, {ServerName: "peer/password"},
		{ServerName: "peer\x00"}, {CAFile: "/missing/ca.pem"},
	} {
		_, err := NewConnector(Options{Host: "remote.example", Username: "user", TLS: &policy}, "")
		if err == nil || err.Error() == "DM8 requires an explicit absolute native client path" {
			t.Fatalf("invalid TLS policy reached the native client: %+v, %v", policy, err)
		}
	}
	file := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(file, []byte("no certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := remoteTLS("peer", TLSOptions{CAFile: file}); err == nil {
		t.Fatal("invalid CA file accepted")
	}
}

func greeting(kind uint16, mode uint32, body []byte) []byte {
	packet := make([]byte, 64+len(body))
	binary.LittleEndian.PutUint16(packet[4:6], kind)
	binary.LittleEndian.PutUint32(packet[6:10], uint32(len(body)))
	binary.LittleEndian.PutUint32(packet[20:24], mode)
	copy(packet[64:], body)
	return packet
}

func TestTLSNegotiationCancellationJoinsPartialOriginalGreeting(t *testing.T) {
	server, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	secure, _, err := remoteTLS("127.0.0.1", TLSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := openConfiguredTransport(ctx, server.Addr().String(), time.Second, true, secure, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	join := bridge.watch(ctx)
	defer join()
	upstream, err := server.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	native, err := net.DialTimeout("tcp", bridge.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	if _, err = native.Write(greeting(200, 0, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err = readGreeting(upstream); err != nil {
		t.Fatal(err)
	}
	// The original peer stalls midway through its header, before TLS starts.
	if _, err = upstream.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-bridge.done:
	case <-time.After(time.Second):
		t.Fatal("canceled original negotiation was not joined")
	}
	if err = native.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err = native.Read(b[:]); err == nil {
		t.Fatal("partial unverified peer reply reached native authentication")
	}
}

func TestGreetingRejectsUnknownAndOversizedHeaders(t *testing.T) {
	for _, packet := range [][]byte{greeting(199, 0, nil), greeting(200, 0, nil)} {
		if binary.LittleEndian.Uint16(packet[4:6]) == 200 {
			binary.LittleEndian.PutUint32(packet[6:10], (64<<10)+1)
		}
		reader, writer := net.Pipe()
		joined := make(chan struct{})
		go func() { defer close(joined); writer.Write(packet); writer.Close() }()
		_, err := readGreeting(reader)
		reader.Close()
		<-joined
		if err == nil {
			t.Fatal("unsupported unbounded greeting accepted")
		}
	}
}

func TestTLSBridgeWithholdsAuthenticationUntilOriginalPeerIsVerified(t *testing.T) {
	original, remoteClient := testLocalTLS(t)
	private, nativeClient := testLocalTLS(t)
	for _, tc := range []struct {
		name, mode                                   string
		wrongName, wrongCA, noClient, plain, success bool
		serverMode                                   uint32
		wrongClient, noRequest                       bool
	}{
		{name: "default-full", success: true},
		{name: "wrong-name", wrongName: true},
		{name: "wrong-ca", wrongCA: true},
		{name: "missing-client-identity", noClient: true},
		{name: "untrusted-client-identity", wrongClient: true},
		{name: "server-does-not-request-identity", noRequest: true},
		{name: "verify-ca-name-optional", mode: "verify-ca", wrongName: true, success: true},
		{name: "verify-ca-still-checks-chain", mode: "verify-ca", wrongCA: true},
		{name: "require-explicit-name-optional", mode: "require", wrongName: true, success: true},
		{name: "require-explicit-ca-checked", mode: "require", wrongCA: true},
		{name: "disable-refuses-encrypted-server", mode: "disable"},
		{name: "full-refuses-plain-server", plain: true},
		{name: "disable-plain-explicit", mode: "disable", plain: true, success: true},
		{name: "certificate-only-mode-refused", serverMode: 2},
		{name: "unverified-mode-refused", serverMode: 4},
		{name: "unaccepted-one-way-mode-refused", serverMode: 5},
		{name: "unsupported-tlcp-mode-refused", serverMode: 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := TLSOptions{Mode: tc.mode, CAFile: filepath.Join(original.directory, "ca-cert.pem"), CertificateFile: filepath.Join(original.directory, "client-cert.pem"), KeyFile: filepath.Join(original.directory, "client-key.pem")}
			if tc.wrongName {
				policy.ServerName = "other-peer.invalid"
			}
			if tc.wrongCA {
				policy.CAFile = filepath.Join(private.directory, "ca-cert.pem")
			}
			if tc.noClient {
				policy.CertificateFile, policy.KeyFile = "", ""
			}
			if tc.wrongClient {
				policy.CertificateFile = filepath.Join(private.directory, "client-cert.pem")
				policy.KeyFile = filepath.Join(private.directory, "client-key.pem")
			}
			if tc.mode == "disable" {
				policy = TLSOptions{Mode: "disable"}
			}
			secure, _, err := remoteTLS("127.0.0.1", policy)
			if err != nil {
				t.Fatal(err)
			}
			server, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var authenticated atomic.Bool
			served := make(chan struct{})
			request := greeting(200, 0, []byte("native-greeting"))
			mode := uint32(1)
			if tc.serverMode != 0 {
				mode = tc.serverMode
			}
			if tc.plain {
				mode = 0
			}
			response := greeting(228, mode, []byte("original-greeting"))
			const authentication = "authentication-marker"
			go func() {
				defer close(served)
				connection, err := server.Accept()
				if err != nil {
					return
				}
				defer connection.Close()
				connection.SetDeadline(time.Now().Add(5 * time.Second))
				actual, err := readGreeting(connection)
				if err != nil || !bytes.Equal(actual, request) {
					return
				}
				if _, err = connection.Write(response); err != nil {
					return
				}
				if !tc.plain {
					serverConfig := original.server.Clone()
					if tc.noRequest {
						serverConfig.ClientAuth = tls.NoClientCert
					}
					encrypted := tls.Server(connection, serverConfig)
					if err = encrypted.HandshakeContext(ctx); err != nil {
						return
					}
					connection = encrypted
				}
				marker := make([]byte, len(authentication))
				if _, err = io.ReadFull(connection, marker); err == nil && string(marker) == authentication {
					authenticated.Store(true)
				}
			}()
			bridge, err := openConfiguredTransport(ctx, server.Addr().String(), time.Second, true, secure, private.server)
			if err != nil {
				t.Fatal(err)
			}
			join := bridge.watch(ctx)
			defer join()
			defer bridge.Close()
			native, err := net.DialTimeout("tcp", bridge.listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer native.Close()
			native.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err = native.Write(request); err != nil {
				t.Fatal(err)
			}
			actual, err := readGreeting(native)
			if tc.success {
				if err != nil || !bytes.Equal(actual, response) {
					t.Fatalf("original greeting: %v", err)
				}
				if !tc.plain {
					encrypted := tls.Client(native, nativeClient)
					if err = encrypted.HandshakeContext(ctx); err != nil {
						t.Fatal(err)
					}
					native = encrypted
				}
				if _, err = native.Write([]byte(authentication)); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("native client received permission to authenticate to a rejected peer")
			}
			<-served
			if authenticated.Load() != tc.success {
				t.Fatal("original peer authentication boundary was violated")
			}
			if !tc.success && bridge.startupFailure() == nil {
				t.Fatal("TLS rejection lost its original reason")
			}
		})
	}
	// Independent local material must not admit another session's identity.
	certificate, parseErr := x509.ParseCertificate(remoteClient.Certificates[0].Certificate[0])
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	_, err := certificate.Verify(x509.VerifyOptions{Roots: private.server.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if err == nil {
		t.Fatal("private sessions share client trust")
	}
}
