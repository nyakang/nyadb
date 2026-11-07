package dpi

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeDPICanceledLoginJoinsOriginalCallAndReleasesHandles(t *testing.T) {
	o, password := nativeFixture(t)
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
	connector := configured.(*connector)
	a := *connector.a
	var freed atomic.Int32
	nativeFreeCon, nativeFreeEnv := a.freeCon, a.freeEnv
	a.freeCon = func(h uintptr) int16 { freed.Add(1); return nativeFreeCon(h) }
	a.freeEnv = func(h uintptr) int16 { freed.Add(1); return nativeFreeEnv(h) }
	nativeLogin := a.login
	returned := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	a.login = func(h uintptr, host, user, secret string) int16 {
		rc := nativeLogin(h, host, user, secret)
		close(returned)
		<-release
		return rc
	}
	connector.a = &a
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		value, err := connector.Connect(ctx)
		if value != nil {
			value.Close()
		}
		result <- err
	}()
	server.(*net.TCPListener).SetDeadline(time.Now().Add(3 * time.Second))
	upstream, err := server.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstream.SetReadDeadline(time.Now().Add(3 * time.Second))
	var b [1]byte
	if _, err = io.ReadFull(upstream, b[:]); err != nil {
		t.Fatal("native handshake did not start:", err)
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("native login did not acknowledge canceled I/O")
	}
	if freed.Load() != 0 {
		t.Fatal("startup native handles freed during original login")
	}
	select {
	case <-result:
		t.Fatal("startup returned before native acknowledgement")
	default:
	}
	release <- struct{}{}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled startup: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled startup did not settle")
	}
	if freed.Load() != 2 {
		t.Fatal("canceled startup leaked original native handles")
	}
}
