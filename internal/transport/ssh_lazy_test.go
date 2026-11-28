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

func TestSSHHostAndNoneProbePrecedeCredentialLoading(t *testing.T) {
	for _, tc := range []struct {
		name, method                                    string
		none, passwordOnly, unconfirmed, emptyKey, fail bool
	}{
		{name: "none-before-password", method: connections.MySQLSSHPassword, none: true},
		{name: "none-before-key", method: connections.MySQLSSHPublicKey, none: true},
		{name: "none-before-combined", method: connections.MySQLSSHPasswordAndKey, none: true},
		{name: "host-before-key", method: connections.MySQLSSHPublicKey, unconfirmed: true, fail: true},
		{name: "password-only-invalid-key-rejected", method: connections.MySQLSSHPasswordAndKey, passwordOnly: true, fail: true},
		{name: "combined-empty-key-password-fallback", method: connections.MySQLSSHPasswordAndKey, emptyKey: true},
		{name: "offered-invalid-key-rejected", method: connections.MySQLSSHPasswordAndKey, fail: true},
		{name: "required-empty-password-rejected", method: connections.MySQLSSHPassword, passwordOnly: true, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var passwords, keys atomic.Int32
			server := &ssh.ServerConfig{NoClientAuth: tc.none, PasswordCallback: func(_ ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
				passwords.Add(1)
				if string(p) != "original-secret" {
					return nil, errors.New("rejected")
				}
				return nil, nil
			}, PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
				keys.Add(1)
				return nil, errors.New("rejected")
			}}
			if tc.passwordOnly {
				server.PublicKeyCallback = nil
			}
			endpoint, hostKey := sshServerConfig(t, server)
			host, portText, _ := net.SplitHostPort(endpoint)
			port, _ := strconv.Atoi(portText)
			key := filepath.Join(t.TempDir(), "must-not-open")
			if tc.emptyKey {
				key = ""
			}
			password := "original-secret"
			if tc.none || tc.name == "required-empty-password-rejected" {
				password = ""
			}
			options := connections.TransportOptions{SSH: connections.SSHOptions{Enabled: true, Host: host, Port: port, Username: "fixture", AuthMethod: tc.method, PrivateKey: key}}
			security := transport.Options{Secrets: transport.Secrets{SSHPassword: password, SSHPassphrase: "unused"}, Hosts: &credentials.MemoryHostStore{}}
			if !tc.unconfirmed {
				security.ConfirmHost = func(_ context.Context, h transport.HostIdentity) (bool, error) {
					return h.Fingerprint == ssh.FingerprintSHA256(hostKey.PublicKey()), nil
				}
			}
			d, err := transport.NewWithConfig(options, security)
			if err != nil {
				t.Fatal("loaded credentials before host/none/offered-method", err)
			}
			defer d.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := d.DialContext(ctx, "tcp", echo(t))
			if tc.fail {
				if err == nil || c != nil {
					t.Fatal("invalid original attempt published")
				}
				if tc.unconfirmed && !errors.Is(err, transport.ErrHostUnconfirmed) {
					t.Fatal("key failure preceded host identity", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if _, err = c.Write([]byte("original")); err != nil {
					t.Fatal(err)
				}
				var b [8]byte
				if _, err = io.ReadFull(c, b[:]); err != nil || string(b[:]) != "original" {
					t.Fatal("forwarding failed", err)
				}
			}
			wantPassword := int32(0)
			if !tc.none && !tc.fail {
				wantPassword = 1
			}
			if passwords.Load() != wantPassword || keys.Load() != 0 {
				t.Fatal("unexpected authentication attempts", passwords.Load(), keys.Load())
			}
		})
	}
}
