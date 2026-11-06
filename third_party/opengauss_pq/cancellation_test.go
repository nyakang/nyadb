package pq

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type observedSocket struct {
	net.Conn
	closed atomic.Bool
	writes atomic.Int32
}

func (c *observedSocket) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}
func (c *observedSocket) Write(b []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(b)
}

func readPacket(c net.Conn) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(c, header); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header)
	if n < 4 || n > 65536 {
		return nil, fmt.Errorf("invalid packet length %d", n)
	}
	packet := make([]byte, n)
	copy(packet, header)
	_, err := io.ReadFull(c, packet[4:])
	return packet, err
}

// Exercise a real verified TLS connection, including SSLRequest and CancelRequest.
// Keeping the backend socket open proves that finish waits for delivery to settle.
func TestTLSCancelUsesSeparateSocketAndWaitsForEOF(t *testing.T) {
	for _, watcher := range []string{"direct", "connection", "statement"} {
		t.Run(watcher, func(t *testing.T) {
			certServer := httptest.NewTLSServer(nil)
			serverTLS := certServer.TLS.Clone()
			roots := x509.NewCertPool()
			roots.AddCert(certServer.Certificate())
			certServer.Close()
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			original, peer := net.Pipe()
			defer peer.Close()
			observed := &observedSocket{Conn: original}
			defer observed.Close()
			received := make(chan error, 1)
			ack := make(chan struct{})
			defer close(ack)
			go func() {
				c, err := ln.Accept()
				if err != nil {
					received <- err
					return
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(3 * time.Second))
				packet, err := readPacket(c)
				if err != nil || len(packet) != 8 || binary.BigEndian.Uint32(packet[4:]) != 80877103 {
					received <- fmt.Errorf("SSLRequest: %x, %v", packet, err)
					return
				}
				if _, err = c.Write([]byte{'S'}); err != nil {
					received <- err
					return
				}
				tlsSocket := tls.Server(c, serverTLS)
				packet, err = readPacket(tlsSocket)
				if err != nil || len(packet) != 16 || binary.BigEndian.Uint32(packet[4:]) != 80877102 || binary.BigEndian.Uint32(packet[8:]) != 12345 || binary.BigEndian.Uint32(packet[12:]) != 67890 {
					received <- fmt.Errorf("CancelRequest: %x, %v", packet, err)
					return
				}
				received <- nil
				<-ack
			}()
			cn := &conn{c: observed, scratch: make([]byte, 8192), processID: 12345, secretKey: 67890,
				config: &Config{minReadBufferSize: 8192, DialFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
				}}, fallbackConfig: &FallbackConfig{Host: "127.0.0.1", Port: 5432, TLSConfig: &tls.Config{RootCAs: roots, ServerName: "example.com"}},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			var finish func()
			if watcher == "direct" {
				go func() { finished <- cn.cancel(ctx) }()
			} else {
				finish = cn.watchCancel(ctx)
				if watcher == "statement" {
					finish() // Stop the first watcher before installing the statement watcher.
					finish = (&stmt{cn: cn}).watchCancel(ctx)
				}
				cancel()
			}
			select {
			case err := <-received:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("cancel packet not received")
			}
			if finish != nil {
				// The packet proves that cancellation claimed this request.
				go func() { finish(); finished <- nil }()
			}
			if cn.c != observed || observed.closed.Load() || observed.writes.Load() != 0 {
				t.Fatal("TLS cancellation touched original query socket")
			}
			select {
			case err := <-finished:
				t.Fatalf("returned before server acknowledgement: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			ack <- struct{}{}
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("cancel did not settle")
			}
			if watcher != "direct" && !observed.closed.Load() {
				t.Fatal("cancelled original socket was not retired")
			}
		})
	}
}

func TestFailedCancelDeliveryUnblocksOriginalRead(t *testing.T) {
	original, peer := net.Pipe()
	defer peer.Close()
	cn := &conn{c: original, fallbackConfig: &FallbackConfig{Host: "127.0.0.1"}, config: &Config{
		DialFunc: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("cancel dial failed") },
	}}
	ctx, cancel := context.WithCancel(context.Background())
	finish := cn.watchCancel(ctx)
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, original); finish(); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("failed cancellation left the original read blocked")
	}
}

func TestStartupCancellationClosesCapturedSocket(t *testing.T) {
	for _, phase := range []string{"ssl-response", "tls-handshake", "authentication"} {
		for _, bounded := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/timeout=%v", phase, bounded), func(t *testing.T) {
				mode := "require"
				if phase == "authentication" {
					mode = "disable"
				}
				connector, err := NewConnector("host=127.0.0.1 port=5432 user=fixture dbname=fixture sslmode=" + mode)
				if err != nil {
					t.Fatal(err)
				}
				client, server := net.Pipe()
				defer server.Close()
				observed := &observedSocket{Conn: client}
				connector.config.DialFunc = func(context.Context, string, string) (net.Conn, error) { return observed, nil }
				entered := make(chan struct{})
				go func() {
					_, err := readPacket(server)
					if err == nil && phase == "tls-handshake" {
						_, err = server.Write([]byte{'S'})
					}
					close(entered)
					io.Copy(io.Discard, server)
				}()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if bounded {
					connector.config.ConnectTimeout = 80 * time.Millisecond
				}
				done := make(chan error, 1)
				go func() { _, err := connector.Connect(ctx); done <- err }()
				<-entered
				want := context.DeadlineExceeded
				if !bounded {
					cancel()
					want = context.Canceled
				}
				select {
				case err = <-done:
					if !errors.Is(err, want) || !observed.closed.Load() {
						t.Fatalf("startup: error=%v closed=%v", err, observed.closed.Load())
					}
				case <-time.After(time.Second):
					t.Fatal("startup ignored cancellation")
				}
			})
		}
	}
}
