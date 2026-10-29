package mysql

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// A disposable loopback protocol peer exercises the real Go MySQL driver
// without requiring credentials, a running database or production access.
// It sends only a handshake/OK and accepts COM_PING; it never executes SQL.
func writeMySQLPacket(w io.Writer, seq byte, payload []byte) error {
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
	_, err := w.Write(append(header, payload...))
	return err
}

func readMySQLPacket(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if n > 1024*1024 {
		return nil, errors.New("unexpected oversized packet")
	}
	p := make([]byte, n)
	_, err := io.ReadFull(r, p)
	return p, err
}

func mysqlFakeHandshake() []byte {
	const caps uint32 = 0x00000001 | 0x00000004 | 0x00000200 |
		0x00002000 | 0x00008000 | 0x00080000
	p := []byte{10}
	p = append(p, []byte("8.0.36-test")...)
	p = append(p, 0)
	id := make([]byte, 4)
	binary.LittleEndian.PutUint32(id, 123)
	p = append(p, id...)
	p = append(p, []byte("12345678")...)
	p = append(p, 0)
	p = append(p, byte(caps&0xff), byte((caps>>8)&0xff))
	p = append(p, 45)   // utf8mb4_general_ci
	p = append(p, 2, 0) // autocommit
	p = append(p, byte((caps>>16)&0xff), byte((caps>>24)&0xff))
	p = append(p, 21) // auth-plugin data length
	p = append(p, make([]byte, 10)...)
	p = append(p, []byte("abcdefghijkl")...)
	p = append(p, 0)
	p = append(p, []byte("mysql_native_password")...)
	p = append(p, 0)
	return p
}

func TestDirectMySQLConnectionRealProtocolPingAndClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var wg sync.WaitGroup
	defer wg.Wait()
	done := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := writeMySQLPacket(conn, 0, mysqlFakeHandshake()); err != nil {
			done <- err
			return
		}
		if _, err := readMySQLPacket(conn); err != nil {
			done <- err
			return
		}
		if err := writeMySQLPacket(conn, 2, []byte{0, 0, 0, 2, 0, 0, 0}); err != nil {
			done <- err
			return
		}
		for {
			packet, err := readMySQLPacket(conn)
			if err != nil {
				done <- err
				return
			}
			if len(packet) == 0 {
				done <- errors.New("empty command")
				return
			}
			switch packet[0] {
			case 0x0e: // COM_PING
				if err := writeMySQLPacket(conn, 1, []byte{0, 0, 0, 2, 0, 0, 0}); err != nil {
					done <- err
					return
				}
				done <- nil
				return
			default:
				done <- errors.New("unexpected command (not Ping)")
				return
			}
		}
	}()
	host, pstr, _ := net.SplitHostPort(ln.Addr().String())
	port, err := strconv.Atoi(pstr)
	if err != nil {
		t.Fatal(err)
	}
	settings := connections.ActiveSettings{Host: host, Port: uint16(port), Username: "tester", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	db, err := OpenDirectMySQL(ctx, settings, "not-a-production-credential")
	if err != nil {
		t.Fatalf("real-driver loopback handshake failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("loopback protocol peer failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not observe COM_PING")
	}
}

func TestDirectMySQLConnectionCancelledDuringHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		_, _ = readMySQLPacket(conn) // peer deliberately never sends a greeting
	}()
	host, pstr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(pstr)
	s := connections.ActiveSettings{Host: host, Port: uint16(port), MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- TestDirectMySQLConnection(ctx, s, "") }()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("driver never dialed loopback test listener")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not interrupt driver handshake")
	}
}
