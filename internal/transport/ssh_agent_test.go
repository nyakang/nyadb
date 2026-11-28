package transport_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/transport"
	"golang.org/x/crypto/ssh"
)

func TestSSHAgentOriginalCancellationClosesBlockedIdentityRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix agent protocol fixture")
	}
	endpoint, hostKey := sshServerConfig(t, &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }})
	socket := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	seen := make(chan net.Conn, 1)
	ended := make(chan error, 1)
	go func() {
		c, e := listener.Accept()
		if e != nil {
			ended <- e
			return
		}
		defer c.Close()
		var head [4]byte
		if _, e = io.ReadFull(c, head[:]); e != nil {
			ended <- e
			return
		}
		if binary.BigEndian.Uint32(head[:]) != 1 {
			ended <- errors.New("unexpected request length")
			return
		}
		var typ [1]byte
		if _, e = io.ReadFull(c, typ[:]); e != nil || typ[0] != 11 {
			ended <- errors.New("not an identity list request")
			return
		}
		seen <- c
		var b [1]byte
		_, e = c.Read(b[:])
		ended <- e
	}()
	host, portText, _ := net.SplitHostPort(endpoint)
	port, _ := strconv.Atoi(portText)
	d, err := transport.NewWithConfig(connections.TransportOptions{SSH: connections.SSHOptions{Enabled: true, Host: host, Port: port, Username: "fixture", AuthMethod: connections.MySQLSSHAgent, AgentSocket: socket}}, transport.Options{Hosts: &credentials.MemoryHostStore{}, ConfirmHost: func(_ context.Context, h transport.HostIdentity) (bool, error) {
		return h.Fingerprint == ssh.FingerprintSHA256(hostKey.PublicKey()), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		c, e := d.DialContext(ctx, "tcp", "unused:1")
		if c != nil {
			c.Close()
			result <- errors.New("canceled agent published")
			return
		}
		result <- e
	}()
	select {
	case c := <-seen:
		defer c.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("original agent list not reached")
	}
	cancel()
	select {
	case err = <-result:
		if err == nil {
			t.Fatal("canceled agent accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent list ignored original cancellation")
	}
	select {
	case err = <-ended:
		if !errors.Is(err, io.EOF) {
			t.Fatal("agent stream not closed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original agent socket leaked")
	}
}
