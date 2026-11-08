package cassandra

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestCassandraNativeOriginalPreconnectAndMetadataCancellation(t *testing.T) {
	cfg, secret := sessionFixture(t)
	for _, mode := range []string{"cancel", "release", "close"} {
		t.Run(mode, func(t *testing.T) {
			var identities atomic.Int32
			parked := make(chan struct{}, 1)
			proxy := nativeFrameProxy(t, cfg, func(op byte, body []byte, _ frame, front net.Conn) bool {
				if op == 7 && bytes.Contains(body, []byte(identityQuery)) && identities.Add(1) == 2 {
					parked <- struct{}{}
					_, _ = io.Copy(io.Discard, front)
					return true
				}
				return false
			})
			s := fixtureSession(t, proxy, secret, "")
			l := fixtureLease(t, s, 31)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- l.Preconnect(ctx) }()
			select {
			case <-parked:
			case <-time.After(10 * time.Second):
				t.Fatal("original preconnect did not reach real identity response")
			}
			switch mode {
			case "cancel":
				cancel()
			case "release":
				l.Release()
			case "close":
				s.Close()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("preconnect cancellation cause lost", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original unpublished preconnect did not settle")
			}
			if l.(*queryLease).client != nil {
				t.Fatal("canceled unpublished physical client attached late")
			}
			if mode == "cancel" {
				if !s.Alive() || l.(*queryLease).lost {
					t.Fatal("unpublished preconnect revoked original session")
				}
				if err := l.Preconnect(context.Background()); err != nil {
					t.Fatal("explicit preconnect after unpublished cancellation failed", err)
				}
			}
		})
	}
	t.Run("original-metadata", func(t *testing.T) {
		parked := make(chan struct{}, 1)
		var fired atomic.Bool
		proxy := nativeFrameProxy(t, cfg, func(op byte, body []byte, _ frame, front net.Conn) bool {
			if op == 7 && bytes.Contains(body, []byte("SELECT * FROM system_schema.keyspaces")) && fired.CompareAndSwap(false, true) {
				parked <- struct{}{}
				_, _ = io.Copy(io.Discard, front)
				return true
			}
			return false
		})
		s := fixtureSession(t, proxy, secret, "")
		l := fixtureLease(t, s, 32)
		if err := l.Preconnect(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		type result struct {
			rows []contract.Namespace
			err  error
		}
		done := make(chan result, 1)
		go func() { rows, err := s.Namespaces(ctx); done <- result{rows, err} }()
		select {
		case <-parked:
		case <-time.After(10 * time.Second):
			t.Fatal("original metadata did not reach real response")
		}
		cancel()
		select {
		case out := <-done:
			if !errors.Is(out.err, contract.ErrRevoked) || !errors.Is(out.err, context.Canceled) || out.rows != nil {
				t.Fatal("canceled original metadata published/rebound", out.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("original metadata did not acknowledge cancellation")
		}
		if s.Alive() || s.client.Alive() {
			t.Fatal("lost original metadata session remained alive")
		}
		if _, err := l.Replace(); !errors.Is(err, contract.ErrRevoked) {
			t.Fatal("original metadata loss silently replaced session")
		}
	})
}
