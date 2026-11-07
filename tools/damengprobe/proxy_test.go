package main

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDropProxyForwardsCompleteWriteAndDropsReply(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	observed := make(chan string, 1)
	firstForwarded := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		server, err := upstream.Accept()
		if err != nil {
			return
		}
		defer server.Close()
		reader := bufio.NewReader(server)
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		io.WriteString(server, line)
		prefix := make([]byte, len("INSERT INTO NYADB_"))
		if _, err = io.ReadFull(reader, prefix); err != nil {
			return
		}
		close(firstForwarded)
		line, err = reader.ReadString('\n')
		if err == nil {
			observed <- string(prefix) + line
			io.WriteString(server, "COMMITTED\n")
		}
	}()
	p, err := newDropProxy(upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	client, err := net.Dial("tcp", p.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = io.WriteString(client, "READ\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	if line, err := reader.ReadString('\n'); err != nil || line != "READ\n" {
		t.Fatalf("ordinary reply=%q err=%v", line, err)
	}
	// A TCP split inside the marker must still forward the complete request
	// and suppress its acknowledgement.
	if _, err = io.WriteString(client, "INSERT INTO NYADB_"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstForwarded:
	case <-time.After(3 * time.Second):
		t.Fatal("first fragment was not forwarded")
	}
	if _, err = io.WriteString(client, "UNKNOWN_WRITE VALUES (1)\n"); err != nil {
		t.Fatal(err)
	}
	if line, err := reader.ReadString('\n'); err != io.EOF || line != "" {
		t.Fatalf("lost acknowledgement=%q err=%v", line, err)
	}
	select {
	case line := <-observed:
		if strings.TrimSpace(line) != "INSERT INTO NYADB_UNKNOWN_WRITE VALUES (1)" {
			t.Fatalf("upstream request=%q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not receive the complete write")
	}
	<-serverDone
}

func TestDropProxyCloseReleasesBlockedOriginalSockets(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	p, err := newDropProxy(upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	client, err := net.Dial("tcp", p.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	upstream.(*net.TCPListener).SetDeadline(time.Now().Add(3 * time.Second))
	server, err := upstream.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	done := make(chan struct{})
	go func() {
		p.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("proxy close left its original sockets blocked")
	}
	server.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err = server.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("original upstream socket remains alive: %v", err)
	}
}
