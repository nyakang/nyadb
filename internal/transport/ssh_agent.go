package transport

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func captureSSHAgentSocket(configured string) string {
	if configured == "" {
		configured = os.Getenv("SSH_AUTH_SOCK")
	}
	if strings.HasPrefix(configured, "~/") {
		if dir, err := os.UserHomeDir(); err == nil {
			configured = filepath.Join(dir, configured[2:])
		}
	}
	return configured
}

func originalSSHAgentSigners(ctx context.Context, socket, fingerprint string) ([]ssh.Signer, func(), error) {
	noop := func() {}
	if runtime.GOOS == "windows" {
		return nil, noop, errors.New("SSH agent authentication via Windows named pipe or Pageant is not supported")
	}
	if socket == "" {
		return nil, noop, errors.New("SSH agent socket is not configured")
	}
	wanted := strings.TrimSpace(fingerprint)
	if wanted != "" {
		if len(wanted) < 7 || (!strings.EqualFold(wanted[:7], "SHA256:") && !strings.EqualFold(wanted[:7], "SHA256_")) {
			return nil, noop, errors.New("SSH agent fingerprint must be SHA256")
		}
		body := strings.TrimRight(wanted[7:], "=")
		decoded, err := base64.RawStdEncoding.DecodeString(body)
		if err != nil || len(decoded) != 32 {
			return nil, noop, errors.New("invalid SSH agent fingerprint")
		}
		wanted = "SHA256:" + base64.RawStdEncoding.EncodeToString(decoded)
	}
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		if ctx.Err() != nil {
			return nil, noop, ctx.Err()
		}
		return nil, noop, errors.New("original SSH agent socket is unavailable")
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	release := func() { stop(); _ = conn.Close() }
	deadline := time.Now().Add(120 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		release()
		return nil, noop, err
	}
	remote := agent.NewClient(conn)
	signers, err := remote.Signers()
	if err != nil {
		release()
		if ctx.Err() != nil {
			return nil, noop, ctx.Err()
		}
		return nil, noop, errors.New("original SSH agent identity request failed")
	}
	if wanted != "" {
		filtered := make([]ssh.Signer, 0, 1)
		for _, signer := range signers {
			if ssh.FingerprintSHA256(signer.PublicKey()) == wanted {
				filtered = append(filtered, signer)
			}
		}
		signers = filtered
	}
	if len(signers) == 0 {
		release()
		return nil, noop, errors.New("no SSH agent identity matches original selection")
	}
	return signers, release, nil
}
