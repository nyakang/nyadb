package sessionbrowser

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestRedisSentinelHandshakeCapturesVaultAndDraftCredentials(t *testing.T) {
	for _, draft := range []bool{false, true} {
		s := New(Dependencies{ReadSecret: func(_ int64, key string) (string, bool, error) {
			if key == connections.RedisSentinelCredential {
				return "sentinel-secret", true, nil
			}
			return "data-secret", true, nil
		}})
		defer s.Close()
		done := make(chan struct{}, 1)
		captured := make(chan string, 1)
		s.SetEngineRuntime(EngineRuntime{Connect: func(context.Context, connections.Profile, string) error { return nil }, Session: func(int64) (contract.Session, error) { return nil, contract.ErrUnsupported }, Open: func(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
			secret := connections.RedisSentinelPassword(ctx)
			captured <- secret
			return nil, errors.New(password + " and " + secret)
		}})
		p, _ := connections.Defaults(connections.Redis)
		p.ID, p.Name = 7, "Sentinel"
		p.RedisOptions.Topology = "sentinel"
		p.RedisOptions.MasterName = "mymaster"
		s.Activate(p.ID)
		want := "sentinel-secret"
		if draft {
			s.Current().SentinelPassword = "draft-sentinel"
			want = "draft-sentinel"
		}
		s.Connect(p, func() { done <- struct{}{} })
		s.Current().SentinelPassword = "changed-after-dispatch"
		awaitJob(t, done)
		s.Consume()
		if got := <-captured; got != want {
			t.Fatal("wrong Sentinel credential capture")
		}
		if strings.Contains(s.Current().Error, want) || strings.Contains(s.Current().Error, "data-secret") {
			t.Fatal("session error exposed credentials")
		}
		s.Disconnect(p.ID)
		if s.Current().SentinelPassword != "" {
			t.Fatal("disconnect retained Sentinel draft")
		}
	}
}

func TestSQLiteHandshakeCapturesImmutableAttachments(t *testing.T) {
	s := New(Dependencies{})
	defer s.Close()
	gate := make(chan struct{})
	captured := make(chan string, 1)
	done := make(chan struct{}, 1)
	s.SetEngineRuntime(EngineRuntime{
		Connect: func(context.Context, connections.Profile, string) error { return nil },
		Session: func(int64) (contract.Session, error) { return nil, contract.ErrUnsupported },
		Open: func(ctx context.Context, p connections.Profile, _ string) (contract.Session, error) {
			<-gate
			captured <- p.SQLiteOptions.Attachments[0].Name
			return nil, contract.ErrUnsupported
		},
	})
	p, _ := connections.Defaults(connections.SQLite)
	p.ID, p.Name, p.Database = 1, "fixture", filepath.Join(t.TempDir(), "main.sqlite")
	p.SQLiteOptions.Attachments = []connections.SQLiteAttachment{{Name: "original", File: filepath.Join(t.TempDir(), "attached.sqlite")}}
	s.Connect(p, func() { done <- struct{}{} })
	p.SQLiteOptions.Attachments[0].Name = "changed after dispatch"
	close(gate)
	awaitJob(t, done)
	if name := <-captured; name != "original" {
		t.Fatalf("worker observed later profile mutation: %s", name)
	}
	s.Consume()
}
