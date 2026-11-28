package manticore

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/transport"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestNativeSSHTransportOriginalAgent(t *testing.T) {
	if os.Getenv("NYADB_SSH_AGENT_IT") != "1" {
		t.Skip("requires owned ssh-agent and OpenSSH")
	}
	for _, mode := range []string{"all", "selected", "normalized-selected", "case-mutated", "environment", "missing-socket", "missing-key"} {
		t.Run(mode, func(t *testing.T) {
			cfg, secret, owner := nativeSSHTransportOptions(t, func(options *connections.TransportOptions, security *transport.Options) {
				options.SSH.AuthMethod = connections.MySQLSSHAgent
				options.SSH.AgentSocket = os.Getenv("NYADB_AGENT_SOCKET")
				options.SSH.AgentFingerprint = ""
				options.SSH.PrivateKey = filepath.Join(t.TempDir(), "must-not-open")
				security.Secrets.SSHPassword = "must-not-send"
				security.Secrets.SSHPassphrase = "must-not-use"
				if mode == "selected" || mode == "normalized-selected" || mode == "case-mutated" {
					options.SSH.AgentFingerprint = os.Getenv("NYADB_AGENT_FINGERPRINT")
					if mode == "normalized-selected" {
						options.SSH.AgentFingerprint = " sha256_" + strings.TrimPrefix(options.SSH.AgentFingerprint, "SHA256:") + "= "
					}
					if mode == "case-mutated" {
						b := []byte(options.SSH.AgentFingerprint)
						for i := 7; i < len(b); i++ {
							if b[i] >= 'a' && b[i] <= 'z' {
								b[i] -= 32
								break
							}
							if b[i] >= 'A' && b[i] <= 'Z' {
								b[i] += 32
								break
							}
						}
						options.SSH.AgentFingerprint = string(b)
					}
				}
				if mode == "environment" {
					options.SSH.AgentSocket = ""
				}
				if mode == "missing-socket" {
					options.SSH.AgentSocket = filepath.Join(t.TempDir(), "absent.sock")
				}
				if mode == "missing-key" {
					options.SSH.AgentFingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
				}
			})
			session, err := OpenWithTransport(context.Background(), 89, cfg, secret, owner)
			if mode == "missing-socket" || mode == "missing-key" || mode == "case-mutated" {
				if err == nil || session != nil || owner.dials.Load() != 1 || owner.closes.Load() != 1 {
					t.Fatal("invalid original agent published", err)
				}
				objectEvidence(t, "ssh-agent-"+mode+".json", map[string]any{"published": false, "dials": owner.dials.Load(), "closes": owner.closes.Load(), "error": err.Error()})
				return
			}
			if err != nil {
				t.Fatal("agent authentication failed", err)
			}
			t.Cleanup(func() { session.Close() })
			q := nativeLease(t, session, 8901)
			actual := runNative(t, q, "SELECT 1")
			if fmt.Sprint(actual.Rows) != "[[1]]" {
				t.Fatal(actual)
			}
			objectEvidence(t, "ssh-agent-"+mode+".json", map[string]any{"actual": actual, "inactive_credentials_ignored": true, "selected": mode == "selected"})
		})
	}
}

func TestNativeSSHTransportAgentHeldSignatureCancellation(t *testing.T) {
	if os.Getenv("NYADB_SSH_AGENT_IT") != "1" {
		t.Skip("requires actual isolated ssh-agent and OpenSSH")
	}
	dir, err := os.MkdirTemp("", "nyadb-agent89-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "sign.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	held := make(chan int, 1)
	closed := make(chan error, 1)
	frame := func(c net.Conn) ([]byte, error) {
		var h [4]byte
		if _, e := io.ReadFull(c, h[:]); e != nil {
			return nil, e
		}
		n := binary.BigEndian.Uint32(h[:])
		if n > 1<<20 {
			return nil, fmt.Errorf("oversized agent frame")
		}
		b := make([]byte, 4+int(n))
		copy(b, h[:])
		_, e := io.ReadFull(c, b[4:])
		return b, e
	}
	go func() {
		front, e := listener.Accept()
		if e != nil {
			closed <- e
			return
		}
		defer front.Close()
		front.SetDeadline(time.Now().Add(20 * time.Second))
		back, e := net.Dial("unix", os.Getenv("NYADB_AGENT_SOCKET"))
		if e != nil {
			closed <- e
			return
		}
		defer back.Close()
		back.SetDeadline(time.Now().Add(20 * time.Second))
		for {
			request, e := frame(front)
			if e != nil {
				closed <- e
				return
			}
			if _, e = back.Write(request); e != nil {
				closed <- e
				return
			}
			reply, e := frame(back)
			if e != nil {
				closed <- e
				return
			}
			if len(request) > 4 && request[4] == 13 {
				if len(reply) < 5 || reply[4] != 14 {
					closed <- fmt.Errorf("real agent refused signing")
					return
				}
				held <- len(reply)
				var b [1]byte
				_, e = front.Read(b[:])
				closed <- e
				return
			}
			if _, e = front.Write(reply); e != nil {
				closed <- e
				return
			}
		}
	}()
	cfg, secret, owner := nativeSSHTransportOptions(t, func(options *connections.TransportOptions, _ *transport.Options) {
		options.SSH.AuthMethod = connections.MySQLSSHAgent
		options.SSH.AgentSocket = socket
		options.SSH.AgentFingerprint = os.Getenv("NYADB_AGENT_FINGERPRINT")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		s   *Session
		err error
	}
	done := make(chan result, 1)
	go func() { s, e := OpenWithTransport(ctx, 89, cfg, secret, owner); done <- result{s, e} }()
	size := 0
	select {
	case size = <-held:
	case <-time.After(15 * time.Second):
		t.Fatal("real signature not observed")
	}
	cancel()
	select {
	case r := <-done:
		if r.s != nil || r.err == nil {
			t.Fatal("canceled agent signature published", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original sign did not acknowledge cancellation")
	}
	select {
	case err = <-closed:
		if err != io.EOF {
			t.Fatal("original agent socket not closed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original sign socket retained")
	}
	if owner.dials.Load() != 1 || owner.closes.Load() != 1 {
		t.Fatal("original owner cleanup mismatch")
	}
	cfg, secret, fresh := nativeSSHTransportOptions(t, func(options *connections.TransportOptions, _ *transport.Options) {
		options.SSH.AuthMethod = connections.MySQLSSHAgent
		options.SSH.AgentSocket = os.Getenv("NYADB_AGENT_SOCKET")
		options.SSH.AgentFingerprint = os.Getenv("NYADB_AGENT_FINGERPRINT")
	})
	session, err := OpenWithTransport(context.Background(), 89, cfg, secret, fresh)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	q := nativeLease(t, session, 8902)
	actual := runNative(t, q, "SELECT 1")
	if fmt.Sprint(actual.Rows) != "[[1]]" {
		t.Fatal(actual)
	}
	objectEvidence(t, "ssh-agent-held-sign-cancel.json", map[string]any{"published": false, "real_signature_bytes": size, "dials": owner.dials.Load(), "closes": owner.closes.Load(), "original_agent_socket_closed": true, "fresh_actual": actual})
}

func TestNativeSSHTransportAgentLockedRemovedAndExplicitRecovery(t *testing.T) {
	if os.Getenv("NYADB_SSH_AGENT_IT") != "1" {
		t.Skip("requires owned mutable ssh-agent")
	}
	conn, err := net.Dial("unix", os.Getenv("NYADB_AGENT_SOCKET"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := agent.NewClient(conn)
	passphrase := []byte("owned-agent-test-lock")
	if err = client.Lock(passphrase); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			client.Unlock(passphrase)
		}
	}()
	open := func() (connections.ManticoreConfig, string, *countedNativeSSH) {
		return nativeSSHTransportOptions(t, func(options *connections.TransportOptions, _ *transport.Options) {
			options.SSH.AuthMethod = connections.MySQLSSHAgent
			options.SSH.AgentSocket = os.Getenv("NYADB_AGENT_SOCKET")
			options.SSH.AgentFingerprint = os.Getenv("NYADB_AGENT_FINGERPRINT")
		})
	}
	cfg, secret, owner := open()
	session, err := OpenWithTransport(context.Background(), 89, cfg, secret, owner)
	if err == nil || session != nil || owner.closes.Load() != 1 {
		t.Fatal("locked original agent published", err)
	}
	if err = client.Unlock(passphrase); err != nil {
		t.Fatal(err)
	}
	locked = false
	cfg, secret, owner = open()
	session, err = OpenWithTransport(context.Background(), 89, cfg, secret, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	q := nativeLease(t, session, 8903)
	actual := runNative(t, q, "SELECT 1")
	if fmt.Sprint(actual.Rows) != "[[1]]" {
		t.Fatal(actual)
	}
	identities, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, key := range identities {
		pub, e := ssh.ParsePublicKey(key.Blob)
		if e != nil {
			t.Fatal(e)
		}
		if ssh.FingerprintSHA256(pub) == os.Getenv("NYADB_AGENT_FINGERPRINT") {
			if e = client.Remove(pub); e != nil {
				t.Fatal(e)
			}
			removed = true
			break
		}
	}
	if !removed {
		t.Fatal("owned selected identity absent")
	}
	t.Cleanup(func() {
		keyBytes, e := os.ReadFile(os.Getenv("NYADB_SSH_CLIENT_KEY"))
		if e != nil {
			t.Error(e)
			return
		}
		key, e := ssh.ParseRawPrivateKey(keyBytes)
		clear(keyBytes)
		if e != nil {
			t.Error(e)
			return
		}
		restore, e := net.Dial("unix", os.Getenv("NYADB_AGENT_SOCKET"))
		if e != nil {
			t.Error(e)
			return
		}
		defer restore.Close()
		if e = agent.NewClient(restore).Add(agent.AddedKey{PrivateKey: key, Comment: "restored owned fixture"}); e != nil {
			t.Error(e)
		}
	})
	cfg, secret, revoked := open()
	attempt, err := OpenWithTransport(context.Background(), 89, cfg, secret, revoked)
	if err == nil || attempt != nil || revoked.closes.Load() != 1 {
		t.Fatal("removed selected identity published", err)
	}
	actual = runNative(t, q, "SELECT 1")
	if fmt.Sprint(actual.Rows) != "[[1]]" || !session.Alive() {
		t.Fatal("agent identity removal broke original authenticated sibling", actual)
	}
	objectEvidence(t, "ssh-agent-lock-remove.json", map[string]any{"locked_published": false, "removed_published": false, "selected_removed": removed, "sibling_alive": session.Alive(), "actual": actual})
}

func TestNativeSSHTransportAgentCapturesOriginalEnvironment(t *testing.T) {
	if os.Getenv("NYADB_SSH_AGENT_IT") != "1" {
		t.Skip("requires owned isolated SSH agent")
	}
	originalSocket := os.Getenv("NYADB_AGENT_SOCKET")
	t.Setenv("SSH_AUTH_SOCK", originalSocket)
	capture := func() (connections.ManticoreConfig, string, *countedNativeSSH) {
		return nativeSSHTransportOptions(t, func(options *connections.TransportOptions, _ *transport.Options) {
			options.SSH.AuthMethod = connections.MySQLSSHAgent
			options.SSH.AgentSocket = ""
			options.SSH.AgentFingerprint = os.Getenv("NYADB_AGENT_FINGERPRINT")
		})
	}
	cfg, secret, owner := capture()
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(t.TempDir(), "late-missing-agent.sock"))
	original, err := OpenWithTransport(context.Background(), 89, cfg, secret, owner)
	if err != nil {
		t.Fatal("original environment replaced", err)
	}
	defer original.Close()
	q := nativeLease(t, original, 8904)
	cfg, secret, late := capture()
	other, err := OpenWithTransport(context.Background(), 89, cfg, secret, late)
	if err == nil || other != nil || late.closes.Load() != 1 {
		t.Fatal("late environment failed to stay separate", err)
	}
	actual := runNative(t, q, "SELECT 1")
	if fmt.Sprint(actual.Rows) != "[[1]]" || !original.Alive() {
		t.Fatal("late attempt affected original", actual)
	}
	objectEvidence(t, "ssh-agent-original-environment.json", map[string]any{"original_socket_captured": true, "late_published": false, "late_closes": late.closes.Load(), "actual": actual, "original_alive": original.Alive()})
}

func TestNativeSSHTransportNoneProbeIgnoresUnavailableAgent(t *testing.T) {
	if os.Getenv("NYADB_SSH_AUTH_METHOD") != connections.MySQLSSHNone {
		t.Skip("requires owned none-accepting OpenSSH")
	}
	cfg, secret, owner := nativeSSHTransportOptions(t, func(options *connections.TransportOptions, security *transport.Options) {
		options.SSH.AuthMethod = connections.MySQLSSHAgent
		options.SSH.AgentSocket = filepath.Join(t.TempDir(), "must-not-connect.sock")
		options.SSH.AgentFingerprint = "not-needed-invalid-fingerprint"
		options.SSH.PrivateKey = filepath.Join(t.TempDir(), "must-not-open")
		security.Secrets.SSHPassword = "must-not-send"
		security.Secrets.SSHPassphrase = "must-not-use"
	})
	session, err := OpenWithTransport(context.Background(), 89, cfg, secret, owner)
	if err != nil {
		t.Fatal("none acceptance required inactive agent", err)
	}
	defer session.Close()
	q := nativeLease(t, session, 8905)
	actual := runNative(t, q, "SELECT 1")
	if fmt.Sprint(actual.Rows) != "[[1]]" {
		t.Fatal(actual)
	}
	objectEvidence(t, "ssh-agent-none-ignored.json", map[string]any{"actual": actual, "unavailable_agent_ignored": true, "selected_method": connections.MySQLSSHAgent})
}
