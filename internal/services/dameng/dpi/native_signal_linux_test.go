//go:build linux

package dpi

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNativeDPILoginWithRuntimeSignalStillWaitsForCancellation(t *testing.T) {
	o, password := nativeFixture(t)
	o.TLS = nil
	server, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	o.Port = uint16(server.Addr().(*net.TCPAddr).Port)
	configured, err := NewConnector(o, password)
	if err != nil {
		t.Fatal(err)
	}
	owner := configured.(*connector)
	a := *owner.a
	login := a.login
	thread := make(chan int, 1)
	a.login = func(h uintptr, host, user, secret string) int16 {
		thread <- unix.Gettid()
		return login(h, host, user, secret)
	}
	owner.a = &a
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		connection, err := owner.Connect(ctx)
		if connection != nil {
			connection.Close()
		}
		result <- err
	}()
	server.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second))
	upstream, err := server.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstream.SetReadDeadline(time.Now().Add(5 * time.Second))
	var byte [1]byte
	if _, err = io.ReadFull(upstream, byte[:]); err != nil {
		t.Fatal("native login did not enter original I/O:", err)
	}
	tid := <-thread
	if err = unix.Tgkill(os.Getpid(), tid, unix.SIGURG); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		t.Fatalf("runtime signal interrupted original native login: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("explicit cancellation did not join original native login")
	}
}
