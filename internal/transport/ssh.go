package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"golang.org/x/crypto/ssh"
)

var ErrHostChanged = errors.New("SSH host fingerprint changed; connection refused")
var ErrHostUnconfirmed = errors.New("SSH host fingerprint requires confirmation")

func newSSHDial(a connections.MySQLSSH, o Options, onLost func()) (func(context.Context, string, string) (net.Conn, error), func() error, error) {
	if a.Host == "" || a.Username == "" {
		return nil, nil, errors.New("SSH host and username required")
	}
	if a.AuthMethod == connections.MySQLSSHAgent {
		a.AgentSocket = captureSSHAgentSocket(a.AgentSocket)
	}
	endpoint := net.JoinHostPort(a.Host, fmt.Sprint(a.Port))
	var mu sync.Mutex
	var client *ssh.Client
	closed := false
	finish := func() error {
		mu.Lock()
		closed = true
		c := client
		client = nil
		mu.Unlock()
		if c != nil {
			return c.Close()
		}
		return nil
	}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return nil, ErrRevoked
		}
		if client == nil {
			raw, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", endpoint)
			if err != nil {
				return nil, err
			}
			stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
			auth, beforeAuth, releaseAuth := sshAuthentication(ctx, a, o.Secrets)
			cfg := &ssh.ClientConfig{User: a.Username, Auth: auth, AuthCallback: beforeAuth, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
				identity := HostIdentity{Host: endpoint, Fingerprint: ssh.FingerprintSHA256(key)}
				if o.Hosts == nil {
					return ErrHostUnconfirmed
				}
				known, found, err := o.Hosts.Lookup(endpoint)
				if err != nil {
					return err
				}
				if found {
					if known != identity.Fingerprint {
						return ErrHostChanged
					}
					return nil
				}
				if o.ConfirmHost == nil {
					return ErrHostUnconfirmed
				}
				ok, err := o.ConfirmHost(ctx, identity)
				if err != nil {
					return err
				}
				if !ok {
					return ErrHostUnconfirmed
				}
				return o.Hosts.Remember(endpoint, identity.Fingerprint)
			}}
			c, ch, r, err := ssh.NewClientConn(raw, endpoint, cfg)
			releaseAuth()
			stop()
			if err != nil {
				_ = raw.Close()
				return nil, err
			}
			if ctx.Err() != nil {
				_ = c.Close()
				return nil, ctx.Err()
			}
			client = ssh.NewClient(c, ch, r)
			active := client
			go func() {
				_ = active.Wait()
				if onLost != nil {
					onLost()
				}
			}()
		}

		type outcome struct {
			conn net.Conn
			err  error
		}
		result := make(chan outcome, 1)
		active := client
		go func() { c, err := active.Dial(network, addr); result <- outcome{c, err} }()
		select {
		case r := <-result:
			if r.err != nil {
				return nil, r.err
			}
			return withDeadlines(r.conn), nil
		case <-ctx.Done():
			closed = true
			_ = active.Close()
			client = nil
			r := <-result
			if r.conn != nil {
				_ = r.conn.Close()
			}
			return nil, ctx.Err()
		}

	}
	return dial, finish, nil
}

// loadSSHPrivateKey is called after the original verified server rejects none.
func loadSSHPrivateKey(path, passphrase string) (ssh.Signer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open SSH private key: %w", err)
	}
	info, err := f.Stat()
	if err != nil || info.Size() > 1<<20 {
		_ = f.Close()
		return nil, errors.New("SSH private key exceeds 1 MiB")
	}
	b := make([]byte, info.Size())
	_, err = io.ReadFull(f, b)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	var signer ssh.Signer
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(b, []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(b)
	}
	clear(b)
	if err != nil {
		return nil, errors.New("unable to decrypt or parse SSH private key")
	}
	return signer, nil
}

// sshAuthentication owns one original handshake. The callback runs only after
// host verification and a rejected standard none probe.
func sshAuthentication(ctx context.Context, a connections.SSHOptions, secrets Secrets) ([]ssh.AuthMethod, ssh.ClientAuthCallback, func()) {
	release := func() {}
	var agentSigners []ssh.Signer

	var auth []ssh.AuthMethod
	var signer ssh.Signer
	if a.AuthMethod == connections.MySQLSSHAgent {
		auth = append(auth, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) { return agentSigners, nil }))
	}
	if a.UsesPrivateKey() {
		auth = append(auth, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			if signer == nil {
				return nil, nil
			}
			return []ssh.Signer{signer}, nil
		}))
	}
	if a.UsesPassword() {
		auth = append(auth, ssh.PasswordCallback(func() (string, error) {
			if secrets.SSHPassword == "" {
				return "", errors.New("SSH password required")
			}
			return secrets.SSHPassword, nil
		}))
	}
	prepared := false
	before := func(*ssh.ClientAuthContext) (ssh.AuthMethod, error) {
		if prepared {
			return nil, nil
		}
		prepared = true
		if a.AuthMethod == connections.MySQLSSHAgent {
			var err error
			agentSigners, release, err = originalSSHAgentSigners(ctx, a.AgentSocket, a.AgentFingerprint)
			if err != nil {
				return nil, err
			}
		}
		if a.UsesPrivateKey() && (a.PrivateKey != "" || a.AuthMethod == connections.MySQLSSHPublicKey) {
			var err error
			signer, err = loadSSHPrivateKey(a.PrivateKey, secrets.SSHPassphrase)
			if err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	return auth, before, func() { release() }
}
