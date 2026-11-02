package sessionbrowser

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

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
