package transport_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/transport"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/websocket"
)

func echo(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return l.Addr().String()
}
func httpSettings(t *testing.T, s *httptest.Server) connections.ActiveSettings {
	t.Helper()
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
	path := filepath.Join(t.TempDir(), "ca.pem")
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	a := connections.DefaultMySQLAdvanced()
	a.HTTP.Enabled = true
	a.HTTP.URL = s.URL
	a.HTTP.TargetID = "fixture"
	a.HTTP.CACertificate = path
	return connections.ActiveSettings{MySQLAdvanced: a}
}
func TestGatewayTypedBinaryTLSConcurrentAndNoReplay(t *testing.T) {
	target := echo(t)
	var eventsMu sync.Mutex
	var events []transport.GatewayEvent
	g, e := transport.NewGateway(transport.GatewayConfig{Targets: map[string]string{"fixture": target}, Token: "fixture-token", Log: func(e transport.GatewayEvent) { eventsMu.Lock(); events = append(events, e); eventsMu.Unlock() }})
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	server := httptest.NewTLSServer(g)
	defer server.Close()
	s := httpSettings(t, server)
	dialer, e := transport.New(s, transport.Options{Secrets: transport.Secrets{HTTPToken: "fixture-token"}})
	if e != nil {
		t.Fatal(e)
	}
	defer dialer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			c, e := dialer.DialContext(ctx, "tcp", "ignored.example:3306")
			if e != nil {
				t.Error(e)
				return
			}
			defer c.Close()
			data := []byte(strings.Repeat("\x00\xffSQL\n", 14000))
			if _, e = c.Write(data); e != nil {
				t.Error(e)
				return
			}
			got := make([]byte, len(data))
			if _, e = io.ReadFull(c, got); e != nil {
				t.Error(e)
				return
			}
			if string(got) != string(data) {
				t.Error("binary stream changed")
			}
		})
	}
	wg.Wait()
	c, e := dialer.DialContext(ctx, "tcp", "ignored:1")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	g.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = c.Read(make([]byte, 1)); e == nil {
		t.Fatal("gateway shutdown did not revoke")
	}
	if _, e = dialer.DialContext(ctx, "tcp", "ignored:1"); !errors.Is(e, transport.ErrRevoked) {
		t.Fatalf("terminal transport reopened: %v", e)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	if len(events) == 0 {
		t.Fatal("missing state log")
	}
}
func TestGatewayAuthenticationTargetTLSAndTextRejection(t *testing.T) {
	g, e := transport.NewGateway(transport.GatewayConfig{Targets: map[string]string{"fixture": echo(t)}, Token: "secret"})
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	server := httptest.NewTLSServer(g)
	defer server.Close()
	for _, tt := range []struct {
		token, id string
		want      int
	}{{"wrong", "fixture", 401}, {"secret", "arbitrary:3306", 403}} {
		req, _ := http.NewRequest("GET", server.URL, nil)
		req.Header.Set("Authorization", "Bearer "+tt.token)
		req.Header.Set("X-NyaDB-Target-ID", tt.id)
		res, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != tt.want {
			t.Fatalf("status=%d", res.StatusCode)
		}
	}
	s := httpSettings(t, server)
	s.MySQLAdvanced.HTTP.CACertificate = ""
	d, e := transport.New(s, transport.Options{Secrets: transport.Secrets{HTTPToken: "secret"}})
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	if _, e = d.DialContext(context.Background(), "tcp", "unused:1"); e == nil {
		t.Fatal("untrusted outer TLS accepted")
	}
	cfg, _ := websocket.NewConfig("wss"+strings.TrimPrefix(server.URL, "https"), server.URL)
	cfg.Protocol = []string{transport.Protocol}
	cfg.Header = http.Header{"Authorization": []string{"Bearer secret"}, "X-NyaDB-Target-ID": []string{"fixture"}}
	cfg.TlsConfig = server.Client().Transport.(*http.Transport).TLSClientConfig
	ws, e := cfg.DialContext(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer ws.Close()
	if e = websocket.Message.Send(ws, "text SQL"); e != nil {
		t.Fatal(e)
	}
	ws.SetReadDeadline(time.Now().Add(time.Second))
	var b []byte
	if e = websocket.Message.Receive(ws, &b); e == nil {
		t.Fatal("text frame accepted")
	}
}
func TestTunnelCancellationHandshakeAcknowledges(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); accepted <- c }()
	s := connections.ActiveSettings{MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	s.MySQLAdvanced.HTTP.Enabled = true
	s.MySQLAdvanced.HTTP.URL = "https://" + l.Addr().String()
	s.MySQLAdvanced.HTTP.TargetID = "fixture"
	d, e := transport.New(s, transport.Options{Secrets: transport.Secrets{HTTPToken: "secret"}})
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := d.DialContext(ctx, "tcp", "unused:1"); done <- e }()
	c := <-accepted
	defer c.Close()
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("cancel=%v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled handshake stuck")
	}
}
func sshServer(t *testing.T, requireBoth ...bool) (string, ssh.Signer) {
	t.Helper()
	_, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := ssh.NewSignerFromKey(priv)
	if e != nil {
		t.Fatal(e)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(m ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if m.User() == "fixture" && string(p) == "ssh-secret" {
			return nil, nil
		}
		return nil, errors.New("authentication rejected")
	}, PublicKeyCallback: func(m ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	if len(requireBoth) > 0 && requireBoth[0] {
		public := cfg.PublicKeyCallback
		cfg.PublicKeyCallback = nil
		cfg.PasswordCallback = func(m ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if m.User() != "fixture" || string(p) != "ssh-secret" {
				return nil, errors.New("authentication rejected")
			}
			return nil, &ssh.PartialSuccessError{Next: ssh.ServerAuthCallbacks{PublicKeyCallback: public}}
		}
	}
	cfg.AddHostKey(signer)
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	var clients []net.Conn
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range clients {
			c.Close()
		}
	})
	go func() {
		for {
			raw, e := l.Accept()
			if e != nil {
				return
			}
			mu.Lock()
			clients = append(clients, raw)
			mu.Unlock()
			go func() {
				_, channels, requests, e := ssh.NewServerConn(raw, cfg)
				if e != nil {
					raw.Close()
					return
				}
				go ssh.DiscardRequests(requests)
				for ch := range channels {
					if ch.ChannelType() != "direct-tcpip" {
						ch.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					var target struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}
					if e := ssh.Unmarshal(ch.ExtraData(), &target); e != nil {
						ch.Reject(ssh.Prohibited, "invalid")
						continue
					}
					db, e := net.Dial("tcp", net.JoinHostPort(target.Host, fmt.Sprint(target.Port)))
					if e != nil {
						ch.Reject(ssh.ConnectionFailed, "unavailable")
						continue
					}
					c, r, e := ch.Accept()
					if e != nil {
						db.Close()
						continue
					}
					go ssh.DiscardRequests(r)
					go func() {
						defer c.Close()
						defer db.Close()
						done := make(chan struct{})
						go func() { io.Copy(db, c); db.Close(); close(done) }()
						io.Copy(c, db)
						c.Close()
						<-done
					}()
				}
			}()
		}
	}()
	return l.Addr().String(), signer
}
func TestSSHFirstFingerprintPasswordKeyAndMismatch(t *testing.T) {
	endpoint, serverKey := sshServer(t)
	host, portText, _ := net.SplitHostPort(endpoint)
	var port int
	fmt.Sscan(portText, &port)
	s := connections.ActiveSettings{MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	s.MySQLAdvanced.SSH = connections.MySQLSSH{Enabled: true, Host: host, Port: port, Username: "fixture", AuthMethod: connections.MySQLSSHPassword}
	hosts := &credentials.MemoryHostStore{}
	var confirmations atomic.Int32
	opts := transport.Options{Secrets: transport.Secrets{SSHPassword: "ssh-secret"}, Hosts: hosts, ConfirmHost: func(_ context.Context, h transport.HostIdentity) (bool, error) {
		confirmations.Add(1)
		if h.Fingerprint != ssh.FingerprintSHA256(serverKey.PublicKey()) {
			t.Error("wrong fingerprint")
		}
		return true, nil
	}}
	d, e := transport.New(s, opts)
	if e != nil {
		t.Fatal(e)
	}
	c, e := d.DialContext(context.Background(), "tcp", echo(t))
	if e != nil {
		t.Fatal(e)
	}
	c.Write([]byte("test"))
	b := make([]byte, 4)
	if _, e = io.ReadFull(c, b); e != nil || string(b) != "test" {
		t.Fatalf("SSH forwarding: %q %v", b, e)
	}
	c.Close()
	d.Close()
	if confirmations.Load() != 1 {
		t.Fatal("first host not confirmed exactly once")
	}
	d, e = transport.New(s, opts)
	if e != nil {
		t.Fatal(e)
	}
	c, e = d.DialContext(context.Background(), "tcp", echo(t))
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	d.Close()
	if confirmations.Load() != 1 {
		t.Fatal("known host prompted again")
	}
	mismatch := &credentials.MemoryHostStore{}
	mismatch.Remember(endpoint, "SHA256:changed")
	opts.Hosts = mismatch
	d, e = transport.New(s, opts)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	if _, e = d.DialContext(context.Background(), "tcp", echo(t)); !errors.Is(e, transport.ErrHostChanged) && !strings.Contains(e.Error(), transport.ErrHostChanged.Error()) {
		t.Fatalf("host change accepted: %v", e)
	}
	_, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := x509.MarshalPKCS8PrivateKey(private)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "id.pem")
	os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0600)
	s.MySQLAdvanced.SSH.AuthMethod = connections.MySQLSSHPublicKey
	s.MySQLAdvanced.SSH.PrivateKey = path
	opts.Hosts = hosts
	d, e = transport.New(s, opts)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	c, e = d.DialContext(context.Background(), "tcp", echo(t))
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
}

func TestGatewayHTTPProxyCONNECTCredentialsAndOuterTLS(t *testing.T) {
	g, e := transport.NewGateway(transport.GatewayConfig{Targets: map[string]string{"fixture": echo(t)}, Token: "secret"})
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	server := httptest.NewTLSServer(g)
	defer server.Close()
	var auth atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Header.Get("Proxy-Authorization") != "Basic cHJveHk6cHJveHktc2VjcmV0" {
			http.Error(w, "unauthorized", 407)
			return
		}
		auth.Store(true)
		dst, e := net.Dial("tcp", r.Host)
		if e != nil {
			http.Error(w, "unavailable", 502)
			return
		}
		raw, buffer, e := w.(http.Hijacker).Hijack()
		if e != nil {
			dst.Close()
			return
		}
		buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		buffer.Flush()
		go func() { io.Copy(dst, raw); dst.Close(); raw.Close() }()
		io.Copy(raw, dst)
		dst.Close()
		raw.Close()
	}))
	defer proxy.Close()
	s := httpSettings(t, server)
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(proxy.URL, "http://"))
	var port int
	fmt.Sscan(portText, &port)
	s.MySQLAdvanced.HTTP.ProxyEnabled = true
	s.MySQLAdvanced.HTTP.ProxyHost = host
	s.MySQLAdvanced.HTTP.ProxyPort = port
	s.MySQLAdvanced.HTTP.ProxyUsername = "proxy"
	d, e := transport.New(s, transport.Options{Secrets: transport.Secrets{HTTPToken: "secret", ProxyPassword: "proxy-secret"}})
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, e := d.DialContext(ctx, "tcp", "ignored:1")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.Write([]byte("proxied"))
	b := make([]byte, 7)
	if _, e = io.ReadFull(c, b); e != nil || string(b) != "proxied" || !auth.Load() {
		t.Fatalf("proxy failed: %q %v", b, e)
	}
}
func TestSSHEncryptedPrivateKeyCombinationAndReadTimeout(t *testing.T) {
	endpoint, _ := sshServer(t)
	host, portText, _ := net.SplitHostPort(endpoint)
	var port int
	fmt.Sscan(portText, &port)
	_, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	block, e := ssh.MarshalPrivateKeyWithPassphrase(private, "fixture", []byte("key-secret"))
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "id.pem")
	os.WriteFile(path, pem.EncodeToMemory(block), 0600)
	s := connections.ActiveSettings{MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	s.MySQLAdvanced.SSH = connections.MySQLSSH{Enabled: true, Host: host, Port: port, Username: "fixture", AuthMethod: connections.MySQLSSHPasswordAndKey, PrivateKey: path}
	opts := transport.Options{Secrets: transport.Secrets{SSHPassword: "ssh-secret", SSHPassphrase: "key-secret"}, Hosts: &credentials.MemoryHostStore{}, ConfirmHost: func(context.Context, transport.HostIdentity) (bool, error) { return true, nil }}
	d, e := transport.New(s, opts)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, e := d.DialContext(ctx, "tcp", echo(t))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if e = c.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Read(make([]byte, 1)); !errors.Is(e, os.ErrDeadlineExceeded) {
		t.Fatalf("SSH deadline=%v", e)
	}
	if _, e = d.DialContext(ctx, "tcp", echo(t)); !errors.Is(e, transport.ErrRevoked) {
		t.Fatalf("timed out channel replay permitted: %v", e)
	}
	opts.Secrets.SSHPassphrase = "incorrect"
	if _, e = transport.New(s, opts); e == nil {
		t.Fatal("invalid key passphrase accepted")
	}
}

func TestTransportCloseRevokesPendingHandshakeWithoutCallerCancellation(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); accepted <- c }()
	s := connections.ActiveSettings{MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	s.MySQLAdvanced.HTTP.Enabled = true
	s.MySQLAdvanced.HTTP.URL = "https://" + l.Addr().String()
	s.MySQLAdvanced.HTTP.TargetID = "fixture"
	d, e := transport.New(s, transport.Options{Secrets: transport.Secrets{HTTPToken: "secret"}})
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := d.DialContext(context.Background(), "tcp", "unused:1"); done <- e }()
	c := <-accepted
	defer c.Close()
	closed := make(chan struct{})
	go func() { d.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked on pending dial")
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("closed pending handshake returned live channel")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not revoke pending handshake")
	}
}

func TestGatewayCloseAcknowledgesBlockedWriter(t *testing.T) {
	ready := make(chan struct{})
	stop := make(chan struct{})
	server := httptest.NewTLSServer(websocket.Server{Handshake: func(c *websocket.Config, _ *http.Request) error {
		c.Protocol = []string{transport.Protocol}
		return nil
	}, Handler: func(ws *websocket.Conn) { close(ready); defer ws.Close(); <-stop }})
	defer server.Close()
	defer close(stop)
	s := httpSettings(t, server)
	d, e := transport.New(s, transport.Options{Secrets: transport.Secrets{HTTPToken: "secret"}})
	if e != nil {
		t.Fatal(e)
	}
	c, e := d.DialContext(context.Background(), "tcp", "unused:1")
	if e != nil {
		t.Fatal(e)
	}
	<-ready
	written := make(chan error, 1)
	go func() { _, e := c.Write(make([]byte, 16<<20)); written <- e }()
	time.Sleep(20 * time.Millisecond)
	closed := make(chan struct{})
	go func() { d.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked on WebSocket close-frame write")
	}
	select {
	case e := <-written:
		if e == nil {
			t.Fatal("stalled writer completed after revoke")
		}
	case <-time.After(time.Second):
		t.Fatal("writer executor did not acknowledge close")
	}
}

func TestSSHCombinedAuthenticationServerRequiresPasswordThenKey(t *testing.T) {
	endpoint, _ := sshServer(t, true)
	host, ptext, _ := net.SplitHostPort(endpoint)
	port, _ := strconv.Atoi(ptext)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err = os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	a := connections.DefaultMySQLAdvanced()
	a.SSH = connections.MySQLSSH{Enabled: true, Host: host, Port: port, Username: "fixture", AuthMethod: connections.MySQLSSHPasswordAndKey, PrivateKey: path}
	d, err := transport.New(connections.ActiveSettings{MySQLAdvanced: a}, transport.Options{Secrets: transport.Secrets{SSHPassword: "ssh-secret"}, Hosts: &credentials.MemoryHostStore{}, ConfirmHost: func(context.Context, transport.HostIdentity) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := d.DialContext(ctx, "tcp", echo(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("both"))
	data := make([]byte, 4)
	if _, err = io.ReadFull(c, data); err != nil || string(data) != "both" {
		t.Fatalf("combined authentication failed: %q %v", data, err)
	}
}

func TestGatewayMutualTLSClientCertificateRequired(t *testing.T) {
	_, caKey, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture client CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, e := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caKey.Public(), caKey)
	if e != nil {
		t.Fatal(e)
	}
	ca, e := x509.ParseCertificate(caDER)
	if e != nil {
		t.Fatal(e)
	}
	_, clientKey, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	clientTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "fixture client"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientDER, e := x509.CreateCertificate(rand.Reader, clientTemplate, ca, clientKey.Public(), caKey)
	if e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	gateway, e := transport.NewGateway(transport.GatewayConfig{Targets: map[string]string{"fixture": echo(t)}, Token: "secret"})
	if e != nil {
		t.Fatal(e)
	}
	defer gateway.Close()
	server := httptest.NewUnstartedServer(gateway)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	server.StartTLS()
	defer server.Close()
	s := httpSettings(t, server)
	d, e := transport.New(s, transport.Options{Secrets: transport.Secrets{HTTPToken: "secret"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = d.DialContext(context.Background(), "tcp", "ignored:1"); e == nil {
		t.Fatal("mTLS gateway accepted absent certificate")
	}
	d.Close()
	keyBytes, e := x509.MarshalPKCS8PrivateKey(clientKey)
	if e != nil {
		t.Fatal(e)
	}
	keyPath := filepath.Join(t.TempDir(), "client.key")
	certPath := filepath.Join(t.TempDir(), "client.crt")
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}), 0600)
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}), 0600)
	s.MySQLAdvanced.HTTP.CertificateAuth = true
	s.MySQLAdvanced.HTTP.ClientKey = keyPath
	s.MySQLAdvanced.HTTP.ClientCertificate = certPath
	d, e = transport.New(s, transport.Options{Secrets: transport.Secrets{HTTPToken: "secret"}})
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, e := d.DialContext(ctx, "tcp", "ignored:1")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.Write([]byte("mtls"))
	b := make([]byte, 4)
	if _, e = io.ReadFull(c, b); e != nil || string(b) != "mtls" {
		t.Fatalf("client certificate gateway failed: %q %v", b, e)
	}
}
