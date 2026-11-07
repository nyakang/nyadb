package dpi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
	"unsafe"
)

func TestNativeDPISlowGreetingKeepsOriginalLoginAlive(t *testing.T) {
	o, password := nativeFixture(t)
	if o.TLS == nil {
		t.Skip("requires an owned native TLS server")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := net.JoinHostPort(o.Host, strconv.Itoa(int(o.Port)))
	finished, stop := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		client, err := listener.Accept()
		if err != nil {
			return
		}
		defer client.Close()
		server, err := net.DialTimeout("tcp", endpoint, time.Second)
		if err != nil {
			return
		}
		defer server.Close()
		watchDone, watchJoined := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(watchJoined)
			select {
			case <-stop:
				client.Close()
				server.Close()
			case <-watchDone:
			}
		}()
		defer func() { close(watchDone); <-watchJoined }()
		request, err := readGreeting(client)
		if err != nil {
			return
		}
		select {
		case <-stop:
			return
		case <-time.After(400 * time.Millisecond):
		}
		if _, err = io.Copy(server, bytes.NewReader(request)); err != nil {
			return
		}
		copyDone := make(chan struct{})
		go func() { defer close(copyDone); io.Copy(server, client); server.Close() }()
		io.Copy(client, server)
		client.Close()
		server.Close()
		<-copyDone
	}()
	t.Cleanup(func() { close(stop); listener.Close(); <-finished })
	o.Port = uint16(listener.Addr().(*net.TCPAddr).Port)
	db := nativeDB(t, o, password)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("the original login aborted before the configured deadline:", err)
	}
}

// This records the native library's certificate behavior in an owned fixture.
// It does not claim hostname verification or enable remote workbench support.
func TestNativeDPITLSCertificateAudit(t *testing.T) {
	if os.Getenv("NYADB_DAMENG_DPI_NATIVE_TLS_IT") != "1" {
		t.Skip("requires an explicitly enabled owned TLS audit")
	}
	o, password := nativeFixture(t)
	o.TLS = nil // Audit the native library, not the verified Go TLS bridge.
	data, err := os.ReadFile(os.Getenv("NYADB_DAMENG_DPI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Directory string `json:"native_ssl_directory"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil || cfg.Directory == "" {
		t.Fatal("requires the owned TLS certificate directory")
	}
	for _, directory := range []string{cfg.Directory, filepath.Join(t.TempDir(), "missing")} {
		t.Run(map[bool]string{true: "trusted", false: "missing"}[directory == cfg.Directory], func(t *testing.T) {
			configured, err := NewConnector(o, password)
			if err != nil {
				t.Fatal(err)
			}
			owner := configured.(*connector)
			copied := *owner.a
			login := copied.login
			copied.login = func(h uintptr, host, user, secret string) int16 {
				text := append([]byte(directory), 0)
				var pin runtime.Pinner
				pin.Pin(&text[0])
				defer pin.Unpin()
				if rc := copied.setCon(h, 12401, uintptr(unsafe.Pointer(&text[0])), int32(len(directory))); !succeeded(rc) {
					return rc
				}
				return login(h, host, user, secret)
			}
			owner.a = &copied
			db := sql.OpenDB(owner)
			defer db.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = db.PingContext(ctx)
			if directory == cfg.Directory && err != nil {
				t.Fatal(err)
			}
			if directory != cfg.Directory && err == nil {
				t.Fatal("native TLS accepted absent trust material")
			}
			t.Log("original native TLS success:", err == nil)
		})
	}
}

func TestNativeDPIVerifiedRemoteTLS(t *testing.T) {
	o, password := nativeFixture(t)
	if o.TLS == nil {
		t.Skip("requires owned native TLS server")
	}
	if name := os.Getenv("NYADB_DAMENG_TLS_FIXTURE_NAME"); name != "" {
		o.TLS.ServerName = name
	}
	configured, err := NewConnector(o, password)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(configured)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err = db.QueryRowContext(ctx, "SELECT PARA_VALUE FROM V$DM_INI WHERE PARA_NAME='ENABLE_ENCRYPT'").Scan(&mode); err != nil || mode != "1" {
		t.Fatalf("actual native encrypted mode=%s err=%v", mode, err)
	}
	settings := *o.TLS
	untrusted, _ := testLocalTLS(t)
	for _, tc := range []struct {
		name   string
		policy TLSOptions
	}{
		{name: "wrong-peer", policy: TLSOptions{Mode: "verify-full", CAFile: settings.CAFile, CertificateFile: settings.CertificateFile, KeyFile: settings.KeyFile, ServerName: "not-the-actual-peer.invalid"}},
		{name: "untrusted-ca", policy: TLSOptions{CAFile: filepath.Join(untrusted.directory, "ca-cert.pem"), CertificateFile: settings.CertificateFile, KeyFile: settings.KeyFile}},
		{name: "missing-client-identity", policy: TLSOptions{CAFile: settings.CAFile}},
		{name: "disabled-encryption", policy: TLSOptions{Mode: "disable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := o
			opts.TLS = &tc.policy
			configured, err := NewConnector(opts, password)
			if err != nil {
				t.Fatal(err)
			}
			wrong := sql.OpenDB(configured)
			defer wrong.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err = wrong.PingContext(ctx); err == nil {
				t.Fatal("rejected remote TLS policy authenticated")
			}
		})
	}
	t.Log("correct native remote TLS passed; incorrect peer, CA, missing identity and disabled TLS refused")
}

// This immutable 2022 server requests client certificates but does not enforce
// their presence/issuer trust. Record the actual server behavior independently
// of the bridge's requirement to present an explicit identity. Server identity
// verification in Go remains mandatory for verify-full and verify-ca.
func TestNativeDPIServerClientCertificateTrustAudit(t *testing.T) {
	o, password := nativeFixture(t)
	if o.TLS == nil {
		t.Skip("requires the owned original TLS server")
	}
	untrusted, _ := testLocalTLS(t)
	for _, policy := range []TLSOptions{
		{CAFile: o.TLS.CAFile},
		{CAFile: o.TLS.CAFile, CertificateFile: filepath.Join(untrusted.directory, "client-cert.pem"), KeyFile: filepath.Join(untrusted.directory, "client-key.pem")},
	} {
		proxy := dropNativeReply(t, net.JoinHostPort(o.Host, strconv.Itoa(int(o.Port))), "unused-audit-marker", *o.TLS, policy)
		opts := o
		opts.Port = uint16(proxy.listener.Addr().(*net.TCPAddr).Port)
		db := nativeDB(t, opts, password)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := db.PingContext(ctx)
		cancel()
		db.Close()
		if err != nil {
			t.Fatal("owned server's client trust behavior changed; reassess the recorded TLS boundary:", err)
		}
		t.Log("original server authenticated the password with client certificate supplied:", policy.CertificateFile != "", "; client CA enforcement is not verified")
	}
}
