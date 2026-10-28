package connections

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise actual go-sql-driver/mysql traffic against a disposable local
// protocol peer. The peer accepts only the authentication handshake and
// COM_PING; it is not a production MySQL server or a Navicat experiment.
func TestMySQLKeepAliveRealDriverEmitsPeriodicCOMPing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var pings atomic.Int64
	peerResult := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			peerResult <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(7 * time.Second))
		if err := writeMySQLPacket(conn, 0, mysqlFakeHandshake()); err != nil {
			peerResult <- err
			return
		}
		if _, err := readMySQLPacket(conn); err != nil {
			peerResult <- err
			return
		}
		if err := writeMySQLPacket(conn, 2, []byte{0, 0, 0, 2, 0, 0, 0}); err != nil {
			peerResult <- err
			return
		}
		for {
			packet, err := readMySQLPacket(conn)
			if err != nil {
				if errors.Is(err, io.EOF) {
					peerResult <- nil
				} else {
					peerResult <- err
				}
				return
			}
			if len(packet) == 0 {
				peerResult <- errors.New("empty driver command")
				return
			}
			switch packet[0] {
			case 0x0e: // COM_PING
				pings.Add(1)
				if err := writeMySQLPacket(conn, 1, []byte{0, 0, 0, 2, 0, 0, 0}); err != nil {
					peerResult <- err
					return
				}
			case 0x01: // COM_QUIT
				peerResult <- nil
				return
			default:
				peerResult <- fmt.Errorf("unexpected driver command 0x%x", packet[0])
				return
			}
		}
	}()

	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	adv := DefaultMySQLAdvanced()
	adv.KeepAlive = true
	adv.KeepAliveSeconds = 1
	m := NewMySQLSessionManager()
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const profile int64 = 2701
	if err := m.Connect(ctx, ActiveSettings{
		ProfileID: profile, Host: "127.0.0.1", Port: uint16(port),
		Username: "fixture", MySQLAdvanced: adv,
	}, ""); err != nil {
		t.Fatalf("real driver could not perform test handshake: %v", err)
	}
	// One COM_PING occurs during Connect, and the next after one interval.
	deadline := time.Now().Add(3 * time.Second)
	for pings.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := pings.Load(); got < 2 {
		t.Fatalf("no periodic COM_PING observed (count includes initial test Ping): %d", got)
	}
	if err := m.Disconnect(profile); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-peerResult:
		if err != nil {
			t.Fatalf("protocol peer error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("driver did not close the test peer after disconnect")
	}
}
