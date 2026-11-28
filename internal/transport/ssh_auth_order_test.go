package transport_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/transport"
	"golang.org/x/crypto/ssh"
)

func TestSSHCombinedAuthenticationUsesKeyBeforeOfferedPassword(t *testing.T) {
	for _, tc := range []struct {
		name string
		want []string
		fail bool
	}{
		{"key-first", []string{"key"}, false},
		{"key-first-without-password", []string{"key"}, false},
		{"rejected-key-fallback", []string{"key", "password"}, false},
		{"key-only", []string{"key"}, false},
		{"password-only", []string{"password"}, false},
		{"partial-key-then-password", []string{"key", "password"}, false},
		{"no-password-offered", []string{"key"}, true},
		{"both-rejected", []string{"key", "password"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var attempts []string
			record := func(method string) { mu.Lock(); attempts = append(attempts, method); mu.Unlock() }
			password := func(m ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
				record("password")
				if tc.name == "both-rejected" || m.User() != "fixture" || string(p) != "password-secret" {
					return nil, errors.New("password rejected")
				}
				return nil, nil
			}
			public := func(m ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
				record("key")
				switch tc.name {
				case "rejected-key-fallback", "no-password-offered", "both-rejected":
					return nil, errors.New("key rejected")
				case "partial-key-then-password":
					return nil, &ssh.PartialSuccessError{Next: ssh.ServerAuthCallbacks{PasswordCallback: password}}
				}
				return nil, nil
			}
			cfg := &ssh.ServerConfig{PublicKeyCallback: public, PasswordCallback: password}
			if tc.name == "key-only" || tc.name == "no-password-offered" || tc.name == "partial-key-then-password" {
				cfg.PasswordCallback = nil
			}
			if tc.name == "password-only" {
				cfg.PublicKeyCallback = nil
			}
			endpoint, hostKey := sshServerConfig(t, cfg)
			_, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			block, err := ssh.MarshalPrivateKey(private, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(t.TempDir(), "client_key")
			if err = os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
				t.Fatal(err)
			}
			host, portText, _ := net.SplitHostPort(endpoint)
			port, _ := strconv.Atoi(portText)
			opts := connections.TransportOptions{SSH: connections.SSHOptions{Enabled: true, Host: host, Port: port, Username: "fixture", AuthMethod: connections.MySQLSSHPasswordAndKey, PrivateKey: keyPath}}
			secret := "password-secret"
			if tc.name == "key-first" || tc.name == "key-only" {
				secret = "not-needed-password"
			}
			if tc.name == "key-first-without-password" {
				secret = ""
			}
			d, err := transport.NewWithConfig(opts, transport.Options{Secrets: transport.Secrets{SSHPassword: secret}, Hosts: &credentials.MemoryHostStore{}, ConfirmHost: func(_ context.Context, h transport.HostIdentity) (bool, error) {
				return h.Fingerprint == ssh.FingerprintSHA256(hostKey.PublicKey()), nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := d.DialContext(ctx, "tcp", echo(t))
			if tc.fail {
				if err == nil || c != nil {
					t.Fatal("rejected methods opened a channel")
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
					t.Fatal("original channel readback", err)
				}
			}
			mu.Lock()
			got := append([]string(nil), attempts...)
			mu.Unlock()
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("authentication order = %v, want %v", got, tc.want)
			}
		})
	}
}
