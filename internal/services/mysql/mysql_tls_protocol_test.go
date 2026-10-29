package mysql

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// A disposable MySQL protocol peer upgrades to TLS only after CLIENT_SSL.
// This verifies that the actual database driver encrypts the authentication
// exchange rather than merely accepting a tls.Config.
func TestDirectMySQLRealDriverTLSUpgrade(t *testing.T) {
	serverCert, caFile := localMySQLTLSFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		greeting := mysqlFakeHandshake()
		offset := 1 + len("8.0.36-test") + 1 + 4 + 8 + 1
		greeting[offset+1] |= 0x08 // CLIENT_SSL = 0x00000800
		if err := writeMySQLPacket(conn, 0, greeting); err != nil {
			done <- err
			return
		}
		request, err := readMySQLPacket(conn)
		if err != nil {
			done <- err
			return
		}
		if len(request) != 32 || request[1]&0x08 == 0 {
			done <- fmt.Errorf("driver did not send TLS SSLRequest: %d", len(request))
			return
		}
		encrypted := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{serverCert}, MinVersion: tls.VersionTLS12})
		if err := encrypted.Handshake(); err != nil {
			done <- err
			return
		}
		auth, err := readMySQLPacket(encrypted)
		if err != nil {
			done <- err
			return
		}
		if len(auth) < 36 {
			done <- fmt.Errorf("missing encrypted authentication response")
			return
		}
		if err := writeMySQLPacket(encrypted, 3, []byte{0, 0, 0, 2, 0, 0, 0}); err != nil {
			done <- err
			return
		}
		cmd, err := readMySQLPacket(encrypted)
		if err != nil {
			done <- err
			return
		}
		if len(cmd) == 0 || cmd[0] != 0x0e {
			done <- fmt.Errorf("expected encrypted COM_PING")
			return
		}
		done <- writeMySQLPacket(encrypted, 1, []byte{0, 0, 0, 2, 0, 0, 0})
	}()
	_, portStr, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	adv := connections.DefaultMySQLAdvanced()
	adv.SSL.Enabled = true
	adv.SSL.CACertificate = caFile
	settings := connections.ActiveSettings{Host: "localhost", Port: uint16(port), Username: "tester", MySQLAdvanced: adv}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	err = TestDirectMySQLConnection(ctx, settings, "dummy-loopback-password")
	if err != nil {
		t.Fatalf("actual driver TLS upgrade failed: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("TLS server never completed Ping")
	}
}
