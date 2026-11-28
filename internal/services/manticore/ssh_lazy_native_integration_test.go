package manticore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/transport"
)

func TestNativeSSHTransportNoneProbePrecedesUnusedCredentials(t *testing.T) {
	if os.Getenv("NYADB_SSH_AUTH_METHOD") != connections.MySQLSSHNone {
		t.Skip("requires owned none-accepting SSH server")
	}
	for _, method := range []string{connections.MySQLSSHPassword, connections.MySQLSSHPublicKey, connections.MySQLSSHPasswordAndKey} {
		t.Run(method, func(t *testing.T) {
			cfg, secret, owner := nativeSSHTransportOptions(t, func(options *connections.TransportOptions, security *transport.Options) {
				options.SSH.AuthMethod = method
				options.SSH.PrivateKey = filepath.Join(t.TempDir(), "must-not-open")
				security.Secrets.SSHPassword = ""
				security.Secrets.SSHPassphrase = "must-not-use"
			})
			session, err := OpenWithTransport(context.Background(), 88, cfg, secret, owner)
			if err != nil {
				t.Fatal("none probe depended on unused credentials", err)
			}
			t.Cleanup(func() { session.Close() })
			query := nativeLease(t, session, 8801)
			actual := runNative(t, query, "SELECT 1")
			if fmt.Sprint(actual.Rows) != "[[1]]" {
				t.Fatal(actual)
			}
			objectEvidence(t, "ssh-lazy-none-"+method+".json", map[string]any{"method": method, "actual": actual, "missing_password_ignored": true, "unavailable_key_ignored": true, "database_authentication_enforced": os.Getenv("NYADB_MANTICORE_AUTH_IT") == "1"})
		})
	}
}

func TestNativeSSHTransportNeedsOnlyOriginalOfferedCredentials(t *testing.T) {
	method := os.Getenv("NYADB_SSH_AUTH_METHOD")
	if method == "" || method == connections.MySQLSSHNone {
		t.Skip("requires owned credential-enforcing SSH server")
	}
	cfg, secret, owner := nativeSSHTransportOptions(t, func(options *connections.TransportOptions, security *transport.Options) {
		options.SSH.AuthMethod = connections.MySQLSSHPasswordAndKey
		if method == connections.MySQLSSHPassword {
			options.SSH.PrivateKey = ""
			security.Secrets.SSHPassphrase = "unused"
		} else {
			security.Secrets.SSHPassword = ""
		}
	})
	session, err := OpenWithTransport(context.Background(), 88, cfg, secret, owner)
	if method == connections.MySQLSSHPasswordAndKey {
		if err == nil || session != nil || owner.closes.Load() != 1 {
			t.Fatal("mandatory second password was skipped", err)
		}
		objectEvidence(t, "ssh-lazy-required-password.json", map[string]any{"published": false, "dials": owner.dials.Load(), "closes": owner.closes.Load(), "error": err.Error()})
		return
	}
	if err != nil {
		t.Fatal("unused credential required", err)
	}
	t.Cleanup(func() { session.Close() })
	q := nativeLease(t, session, 8802)
	actual := runNative(t, q, "SELECT 1")
	if fmt.Sprint(actual.Rows) != "[[1]]" {
		t.Fatal(actual)
	}
	objectEvidence(t, "ssh-lazy-only-offered.json", map[string]any{"method": method, "actual": actual, "password_absent": method != connections.MySQLSSHPassword, "key_absent": method == connections.MySQLSSHPassword})
}

func TestNativeSSHTransportLocalKeyErrorCannotFallBackToPassword(t *testing.T) {
	method := os.Getenv("NYADB_SSH_AUTH_METHOD")
	if method == "" || method == connections.MySQLSSHNone {
		t.Skip("requires owned credential-enforcing SSH server")
	}
	cfg, secret, siblingOwner := nativeSSHTransportOptions(t, nil)
	sibling, err := OpenWithTransport(context.Background(), 88, cfg, secret, siblingOwner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sibling.Close() })
	q := nativeLease(t, sibling, 8803)
	cfg, secret, owner := nativeSSHTransportOptions(t, func(options *connections.TransportOptions, _ *transport.Options) {
		options.SSH.AuthMethod = connections.MySQLSSHPasswordAndKey
		options.SSH.PrivateKey = filepath.Join(t.TempDir(), "invalid-selected-key")
	})
	session, err := OpenWithTransport(context.Background(), 88, cfg, secret, owner)
	if err == nil || session != nil || owner.closes.Load() != 1 || owner.dials.Load() != 1 {
		t.Fatal("local key error published or retained original owner", err)
	}
	actual := runNative(t, q, "SELECT 1")
	if fmt.Sprint(actual.Rows) != "[[1]]" || !sibling.Alive() {
		t.Fatal("original local error affected sibling", actual)
	}
	objectEvidence(t, "ssh-lazy-local-key-error.json", map[string]any{"published": false, "dials": owner.dials.Load(), "closes": owner.closes.Load(), "error": err.Error(), "sibling_alive": sibling.Alive(), "actual": actual})
}
