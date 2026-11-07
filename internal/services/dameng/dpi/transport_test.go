package dpi

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func transportFixture(t *testing.T) (*transport, net.Conn, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	owned, err := openTransport(context.Background(), listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owned.Close)
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	native, err := net.DialTimeout("tcp", owned.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { native.Close() })
	return owned, server, native
}

func TestTransportPreservesBytesAndClosesOnlyCapturedSession(t *testing.T) {
	first, firstServer, firstNative := transportFixture(t)
	_, secondServer, secondNative := transportFixture(t)
	forward := func(from, to net.Conn) {
		t.Helper()
		from.SetDeadline(time.Now().Add(2 * time.Second))
		to.SetDeadline(time.Now().Add(2 * time.Second))
		payload := bytes.Repeat([]byte{0, 255, 128, '\n'}, 10000)
		done := make(chan error, 1)
		go func() { _, err := from.Write(payload); done <- err }()
		actual := make([]byte, len(payload))
		if _, err := io.ReadFull(to, actual); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(payload, actual) {
			t.Fatal("transport changed original bytes")
		}
	}
	forward(firstNative, firstServer)
	forward(firstServer, firstNative)
	ctx, cancel := context.WithCancel(context.Background())
	join := first.watch(ctx)
	cancel()
	select {
	case <-first.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not join transport")
	}
	join()
	var buffer [1]byte
	if _, err := firstNative.Read(buffer[:]); err == nil {
		t.Fatal("original native socket remains open")
	}
	if _, err := firstServer.Read(buffer[:]); err == nil {
		t.Fatal("original upstream socket remains open")
	}
	forward(secondNative, secondServer)
	forward(secondServer, secondNative)
	first.Close()
}

func TestTransportClosesBeforeNativeAcceptAndJoinsConcurrentClose(t *testing.T) {
	server, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	owned, err := openTransport(context.Background(), server.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var joined sync.WaitGroup
	for range 12 {
		joined.Add(1)
		go func() { defer joined.Done(); owned.Close() }()
	}
	joined.Wait()
	upstream, err := server.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstream.SetReadDeadline(time.Now().Add(time.Second))
	var buffer [1]byte
	if _, err := upstream.Read(buffer[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("upstream not closed: %v", err)
	}
}

func TestCancellationWaitsForNativeAcknowledgementBeforeReleasingHandles(t *testing.T) {
	owned, _, native := transportFixture(t)
	var freed atomic.Int32
	a := &api{
		freeStmt: func(uintptr) int16 { freed.Add(1); return 0 },
		logout:   func(uintptr) int16 { freed.Add(1); return 0 },
		freeCon:  func(uintptr) int16 { freed.Add(1); return 0 },
		freeEnv:  func(uintptr) int16 { freed.Add(1); return 0 },
	}
	c := &conn{a: a, env: 1, h: 2, transport: owned, options: Options{OperationTimeoutSeconds: 5}, statements: make(map[uintptr]*statement)}
	s := &statement{c: c, h: 3}
	c.statements[3] = s
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	acknowledged := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		result <- c.runLocked(ctx, s, true, "execution", func() int16 {
			close(entered)
			var buffer [1]byte
			native.Read(buffer[:])
			close(acknowledged)
			<-release
			return -1
		})
	}()
	<-entered
	cancel()
	select {
	case <-acknowledged:
	case <-time.After(2 * time.Second):
		t.Fatal("native I/O did not stop")
	}
	if freed.Load() != 0 {
		t.Fatal("native handles freed during original call")
	}
	select {
	case <-result:
		t.Fatal("returned before native acknowledgement")
	default:
	}
	release <- struct{}{}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) {
			t.Fatalf("cancellation outcome: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("original call did not settle")
	}
	if freed.Load() != 4 || c.h != 0 || s.h != 0 {
		t.Fatal("original native ownership was not released")
	}
}
