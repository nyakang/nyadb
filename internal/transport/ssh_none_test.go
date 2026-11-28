package transport_test

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/transport"
	"golang.org/x/crypto/ssh"
)

func TestSSHNoneUsesOnlyOriginalProbeAndHostIdentity(t *testing.T) {
	for _, mode := range []string{"accepted", "requires-password", "unconfirmed-host", "changed-host"} {
		t.Run(mode, func(t *testing.T) {
			var passwords, keys atomic.Int32
			server := &ssh.ServerConfig{NoClientAuth: mode != "requires-password", PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
				passwords.Add(1)
				return nil, nil
			}, PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
				keys.Add(1)
				return nil, nil
			}}
			endpoint, hostKey := sshServerConfig(t, server)
			host, portText, _ := net.SplitHostPort(endpoint)
			port, _ := strconv.Atoi(portText)
			options := connections.TransportOptions{SSH: connections.SSHOptions{Enabled: true, Host: host, Port: port, Username: "fixture", AuthMethod: connections.MySQLSSHNone, PrivateKey: filepath.Join(t.TempDir(), "must-not-be-opened")}}
			hosts := &credentials.MemoryHostStore{}
			security := transport.Options{Hosts: hosts, Secrets: transport.Secrets{SSHPassword: "unused-password", SSHPassphrase: "unused-passphrase"}, ConfirmHost: func(_ context.Context, identity transport.HostIdentity) (bool, error) {
				return identity.Fingerprint == ssh.FingerprintSHA256(hostKey.PublicKey()), nil
			}}
			switch mode {
			case "unconfirmed-host":
				security.ConfirmHost = nil
			case "changed-host":
				if err := hosts.Remember(endpoint, "SHA256:original-other-host"); err != nil {
					t.Fatal(err)
				}
			}
			d, err := transport.NewWithConfig(options, security)
			if err != nil {
				t.Fatal("none required inactive credentials", err)
			}
			defer d.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := d.DialContext(ctx, "tcp", echo(t))
			if mode == "accepted" {
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if _, err = c.Write([]byte("original")); err != nil {
					t.Fatal(err)
				}
				var b [8]byte
				if _, err = io.ReadFull(c, b[:]); err != nil || string(b[:]) != "original" {
					t.Fatal("original channel failed", err)
				}
			} else if err == nil || c != nil {
				t.Fatal("rejected original none handshake published")
			}
			if mode == "unconfirmed-host" && !errors.Is(err, transport.ErrHostUnconfirmed) {
				t.Fatal(err)
			}
			if mode == "changed-host" && !errors.Is(err, transport.ErrHostChanged) {
				t.Fatal(err)
			}
			if passwords.Load() != 0 || keys.Load() != 0 {
				t.Fatal("none fell back to credentials")
			}
		})
	}
}
