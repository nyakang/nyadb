package sqlworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSQLPreconnectCompletionSurfacesFailureWithoutDiscardingEditor(t *testing.T) {
	s := New(Dependencies{})
	tab := s.Open(2, "", 1, "db_test", false)
	tab.Editor.Buffer.Set("SELECT @session")
	ch := make(chan error, 1)
	ch <- errors.New("dial failed")
	tab.prepareResults = ch
	ctx, cancel := context.WithCancel(context.Background())
	tab.prepareCancel = cancel

	s.Consume()
	if !strings.Contains(tab.Error(), "dial failed") {
		t.Fatalf("preconnect error was hidden: %q", tab.Error())
	}
	if tab.prepareResults != nil || tab.prepareCancel != nil || ctx.Err() == nil {
		t.Fatal("completed preconnect retained cancellation or channel state")
	}
	if got := tab.Editor.Buffer.String(); got != "SELECT @session" {
		t.Fatalf("preconnect overwrote unsaved SQL: %q", got)
	}
}

func TestSQLPreconnectTabCloseRevokesPendingWork(t *testing.T) {
	s := New(Dependencies{})
	tab := s.Open(2, "", 1, "db_test", false)
	ctx, cancel := context.WithCancel(context.Background())
	tab.prepareCancel = cancel
	tab.prepareResults = make(chan error, 1)

	s.Close(tab)
	if ctx.Err() == nil || tab.prepareResults != nil || tab.prepareCancel != nil {
		t.Fatal("closing tab did not revoke pending preconnect")
	}
	if len(s.documents) != 0 {
		t.Fatalf("closed SQL tab still present: %d", len(s.documents))
	}
}

func TestSQLPreconnectFailureDoesNotOverwriteExecutingQuery(t *testing.T) {
	s := New(Dependencies{})
	tab := s.Open(2, "", 1, "db_test", false)
	tab.loading = true
	ch := make(chan error, 1)
	ch <- errors.New("dial timeout")
	tab.prepareResults = ch
	s.Consume()
	if tab.Error() != "" {
		t.Fatalf("preconnect failure overwrote running query: %q", tab.Error())
	}
}
