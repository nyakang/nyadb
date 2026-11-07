package dpi

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// A single owned server receives the actual operation. Its first response after
// arming is discarded before reaching the original client; bytes are not logged.
type lostReply struct {
	listener  net.Listener
	armed     atomic.Bool
	requested atomic.Bool
	dropped   chan struct{}
	done      chan struct{}
	stop      chan struct{}
}

func dropNativeReply(t *testing.T, endpoint, marker string, settings ...TLSOptions) *lostReply {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &lostReply{listener: listener, dropped: make(chan struct{}), done: make(chan struct{}), stop: make(chan struct{})}
	var remoteConfig, serverConfig *tls.Config
	if len(settings) > 0 {
		host, _, err := net.SplitHostPort(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		remoteConfig, _, err = remoteTLS(host, settings[0])
		if err != nil {
			t.Fatal(err)
		}
		folder := filepath.Join(filepath.Dir(settings[0].CAFile), "..", "server_ssl")
		certificate, err := tls.LoadX509KeyPair(filepath.Join(folder, "server-cert.pem"), filepath.Join(folder, "server-key.pem"))
		if err != nil {
			t.Fatal(err)
		}
		serverConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: remoteConfig.RootCAs}
		// The trust audit varies only the identity sent to the original server.
		// The owned fault proxy continues to verify the application's identity.
		if len(settings) > 1 {
			remoteConfig, _, err = remoteTLS(host, settings[1])
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	go func() {
		defer close(p.done)
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
		watchDone := make(chan struct{})
		go func() {
			select {
			case <-p.stop:
				client.Close()
				server.Close()
			case <-watchDone:
			}
		}()
		defer close(watchDone)
		reader, writer := client, server
		if remoteConfig != nil {
			request, err := readGreeting(client)
			if err != nil {
				return
			}
			if _, err = io.Copy(server, bytes.NewReader(request)); err != nil {
				return
			}
			response, err := readGreeting(server)
			if err != nil {
				return
			}
			upstream := tls.Client(server, remoteConfig)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err = upstream.HandshakeContext(ctx); err != nil {
				return
			}
			if _, err = io.Copy(client, bytes.NewReader(response)); err != nil {
				return
			}
			downstream := tls.Server(client, serverConfig)
			if err = downstream.HandshakeContext(ctx); err != nil {
				return
			}
			reader, writer = downstream, upstream
		}
		forwardDone := make(chan struct{})
		go func() {
			defer close(forwardDone)
			buffer := make([]byte, 64<<10)
			var tail []byte
			for {
				n, err := reader.Read(buffer)
				if n > 0 {
					combined := append(tail, buffer[:n]...)
					if p.armed.Load() && bytes.Contains(combined, []byte(marker)) {
						p.requested.Store(true)
					}
					tail = append([]byte(nil), combined[max(0, len(combined)-len(marker)):]...)
					if _, err = io.Copy(writer, bytes.NewReader(buffer[:n])); err != nil {
						break
					}
				}
				if err != nil {
					break
				}
			}
			server.Close()
		}()
		buffer := make([]byte, 64<<10)
		for {
			n, err := writer.Read(buffer)
			if n > 0 {
				if p.requested.Load() {
					close(p.dropped)
					break
				}
				if _, err = reader.Write(buffer[:n]); err != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		client.Close()
		server.Close()
		<-forwardDone
	}()
	t.Cleanup(func() { close(p.stop); listener.Close(); <-p.done })
	return p
}

func TestNativeDPILostWriteAndCommitRepliesAreNeverReplayed(t *testing.T) {
	o, password := nativeFixture(t)
	observer := nativeDB(t, o, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "autocommit", true: "commit"}[commit], func(t *testing.T) {
			name := "NYADB_DPI_OUTCOME_" + suffix(t)
			if _, err := observer.ExecContext(ctx, "CREATE TABLE "+name+" (ID INT)"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { observer.ExecContext(context.Background(), "DROP TABLE "+name) })
			marker := "INSERT INTO " + name
			if commit {
				marker = "COMMIT"
			}
			var tlsSettings []TLSOptions
			if o.TLS != nil {
				tlsSettings = append(tlsSettings, *o.TLS)
			}
			proxy := dropNativeReply(t, net.JoinHostPort(o.Host, strconv.Itoa(int(o.Port))), marker, tlsSettings...)
			opts := o
			opts.Port = uint16(proxy.listener.Addr().(*net.TCPAddr).Port)
			db := nativeDB(t, opts, password)
			original, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer original.Close()
			if err = original.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
			if commit {
				tx, err := original.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = tx.ExecContext(ctx, "INSERT INTO "+name+" VALUES (1)"); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				proxy.armed.Store(true)
				err = tx.Commit()
				if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) {
					t.Fatalf("lost commit outcome: %v", err)
				}
			} else {
				proxy.armed.Store(true)
				_, err = original.ExecContext(ctx, "INSERT INTO "+name+" VALUES (1)")
				if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) {
					t.Fatalf("lost autocommit outcome: %v", err)
				}
			}
			select {
			case <-proxy.dropped:
			default:
				t.Fatal("did not discard an actual server reply")
			}
			if err = original.PingContext(ctx); err == nil {
				t.Fatal("uncertain physical session was reused")
			}
			var count int
			if err = observer.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+name).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("original single write left %d rows", count)
			}
		})
	}
}
