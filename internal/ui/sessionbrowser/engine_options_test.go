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

func TestPrestoSQLHandshakeCapturesCompleteVaultOrDraftAndRedactsComponents(t *testing.T) {
	for _, draft := range []bool{false, true} {
		want := connections.PrestoSQLCredentials{Password: "original-password", TrustStorePassword: "original-trust", KeyStorePassword: "original-key", AccessToken: "original-token", ExtraCredentials: "original:extra"}
		encoded, _ := connections.EncodePrestoSQLCredentials(want)
		s := New(Dependencies{ReadSecret: func(int64, string) (string, bool, error) { return encoded, true, nil }})
		defer s.Close()
		done := make(chan struct{}, 1)
		captured := make(chan connections.PrestoSQLCredentials, 1)
		s.SetEngineRuntime(EngineRuntime{Connect: func(context.Context, connections.Profile, string) error { return nil }, Session: func(int64) (contract.Session, error) { return nil, contract.ErrUnsupported }, Open: func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
			c, err := connections.DecodePrestoSQLCredentials(secret)
			if err != nil {
				t.Error("worker received invalid credential envelope")
			}
			captured <- c
			return nil, errors.New(c.Password + " " + c.TrustStorePassword + " " + c.KeyStorePassword + " " + c.AccessToken + " " + c.ExtraCredentials)
		}})
		p, _ := connections.Defaults(connections.PrestoSQL)
		p.ID = 67
		p.Name = "original"
		p.Database = "memory"
		s.Activate(p.ID)
		if draft {
			want.Password = "draft-password"
			s.Current().PrestoSQLCredentials = want
		}
		s.Connect(p, func() { done <- struct{}{} })
		if s.Current().PrestoSQLCredentials != (connections.PrestoSQLCredentials{}) {
			t.Fatal("dispatch retained credential drafts")
		}
		s.Current().PrestoSQLCredentials.Password = "later"
		awaitJob(t, done)
		s.Consume()
		if got := <-captured; got != want {
			t.Fatal("worker observed later credentials")
		}
		for _, secret := range []string{want.Password, want.TrustStorePassword, want.KeyStorePassword, want.AccessToken, want.ExtraCredentials} {
			if strings.Contains(s.Current().Error, secret) {
				t.Fatal("error exposed credential component")
			}
		}
		if s.Current().Catalog != "memory" {
			t.Fatal("catalog identity lost")
		}
		s.Disconnect(p.ID)
		if s.Current().PrestoSQLCredentials != (connections.PrestoSQLCredentials{}) {
			t.Fatal("disconnect retained drafts")
		}
	}
}
