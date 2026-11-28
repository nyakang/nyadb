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

func TestNativeSSHNoneRequiredCredentialsRejectWithoutFallback(t *testing.T) {
	if os.Getenv("NYADB_SSH_NONE_REJECT_IT") != "1" {
		t.Skip("requires owned SSH server enforcing authentication")
	}
	cfg, secret, siblingOwner := nativeSSHTransportOptions(t, func(options *connections.TransportOptions, _ *transport.Options) {
		options.SSH.AuthMethod = connections.MySQLSSHPasswordAndKey
	})
	sibling, err := OpenWithTransport(context.Background(), 69, cfg, secret, siblingOwner)
	if err != nil {
		t.Fatal("credential control failed", err)
	}
	t.Cleanup(func() { sibling.Close() })
	query := nativeLease(t, sibling, 8701)
	cfg, secret, original := nativeSSHTransportOptions(t, func(options *connections.TransportOptions, _ *transport.Options) {
		options.SSH.AuthMethod = connections.MySQLSSHNone
		options.SSH.PrivateKey = filepath.Join(t.TempDir(), "unused-inactive-key")
	})
	session, err := OpenWithTransport(context.Background(), 69, cfg, secret, original)
	if session != nil || err == nil || original.dials.Load() != 1 || original.closes.Load() != 1 {
		t.Fatal("rejected none published or retained owner", err)
	}
	select {
	case <-original.Done():
	default:
		t.Fatal("rejected owner was not closed")
	}
	actual := runNative(t, query, "SELECT 1")
	if fmt.Sprint(actual.Rows) != "[[1]]" || !sibling.Alive() {
		t.Fatal("none rejection affected credential sibling", actual)
	}
	objectEvidence(t, "ssh-none-rejected.json", map[string]any{"published": false, "original_dials": original.dials.Load(), "original_closes": original.closes.Load(), "sibling_alive": sibling.Alive(), "actual": actual, "error": err.Error()})
}

func TestNativeSSHTransportNoneIgnoresInactiveCredentials(t *testing.T) {
	if os.Getenv("NYADB_SSH_AUTH_METHOD") != connections.MySQLSSHNone {
		t.Skip("requires owned none-accepting SSH server")
	}
	cfg, secret, owner := nativeSSHTransportOptions(t, func(options *connections.TransportOptions, security *transport.Options) {
		options.SSH.PrivateKey = filepath.Join(t.TempDir(), "must-not-open")
		security.Secrets.SSHPassword = "must-not-send-password"
		security.Secrets.SSHPassphrase = "must-not-use-passphrase"
	})
	session, err := OpenWithTransport(context.Background(), 69, cfg, secret, owner)
	if err != nil {
		t.Fatal("none depended on inactive credentials", err)
	}
	t.Cleanup(func() { session.Close() })
	query := nativeLease(t, session, 8702)
	actual := runNative(t, query, "SELECT 1")
	if fmt.Sprint(actual.Rows) != "[[1]]" {
		t.Fatal(actual)
	}
	objectEvidence(t, "ssh-none-ignored-credentials.json", map[string]any{"actual": actual, "ssh_method": connections.MySQLSSHNone, "inactive_credentials_ignored": true, "database_authentication_enforced": os.Getenv("NYADB_MANTICORE_AUTH_IT") == "1"})
}
