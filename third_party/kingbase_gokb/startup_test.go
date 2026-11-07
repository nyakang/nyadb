package gokb

import (
	"bufio"
	"context"
	"database/sql/driver"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func backendMessage(kind byte, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	b[0] = kind
	binary.BigEndian.PutUint32(b[1:], uint32(4+len(payload)))
	copy(b[5:], payload)
	return b
}

func TestSuccessfulStartupClearsItsDeadline(t *testing.T) {
	connector, err := NewConnectorConfig(Config{Host: "127.0.0.1", Port: 54321, User: "fixture", Database: "fixture", ConnectTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	connector.connectTimeout = 50 * time.Millisecond
	client, server := net.Pipe()
	defer server.Close()
	connector.dialer = explicitDialer{func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	serverDone := make(chan error, 1)
	go func() {
		if _, err := readPacket(server); err != nil {
			serverDone <- err
			return
		}
		messages := backendMessage('R', []byte{0, 0, 0, 0})
		messages = append(messages, backendMessage('Z', []byte{'I'})...)
		if _, err := server.Write(messages); err != nil {
			serverDone <- err
			return
		}
		// An execution after the startup deadline must still use this socket.
		header := make([]byte, 1)
		if _, err := io.ReadFull(server, header); err != nil {
			serverDone <- err
			return
		}
		if _, err := readPacket(server); err != nil {
			serverDone <- err
			return
		}
		messages = backendMessage('C', []byte("UPDATE 1\x00"))
		messages = append(messages, backendMessage('Z', []byte{'I'})...)
		_, err := server.Write(messages)
		serverDone <- err
		io.Copy(io.Discard, server)
	}()
	c, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(60 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = c.(*conn).ExecContext(ctx, "UPDATE fixture SET n=1", nil); err != nil {
		t.Fatal(err)
	}
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestBeginCancellationIsInstalledBeforeSendingBegin(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	cn := &conn{c: client, opts: values{"host": "127.0.0.1", "port": "54321"},
		dialer: explicitDialer{func(context.Context, string, string) (net.Conn, error) { return nil, io.EOF }},
	}
	// A BEGIN response that never arrives must still be cancellable.
	cn.buf = bufio.NewReader(client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := cn.BeginTx(ctx, driver.TxOptions{}); done <- err }()
	header := make([]byte, 1)
	if _, err := io.ReadFull(server, header); err != nil {
		t.Fatal(err)
	}
	if _, err := readPacket(server); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled BEGIN succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("BEGIN did not acknowledge cancellation")
	}
}
