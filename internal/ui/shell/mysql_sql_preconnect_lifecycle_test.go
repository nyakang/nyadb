package shell

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSQLPreconnectCompletionSurfacesFailureWithoutDiscardingEditor(t *testing.T) {
	s := New()
	s.openQuery()
	tab := s.currentTab()
	tab.editor.Buffer.Set("SELECT @session")
	ch := make(chan error, 1)
	ch <- errors.New("dial failed")
	tab.sqlPrepareResults = ch
	ctx, cancel := context.WithCancel(context.Background())
	tab.sqlPrepareCancel = cancel

	s.consumeMySQLSQLResults()
	if !strings.Contains(tab.sqlError, "dial failed") {
		t.Fatalf("preconnect error was hidden: %q", tab.sqlError)
	}
	if tab.sqlPrepareResults != nil || tab.sqlPrepareCancel != nil || ctx.Err() == nil {
		t.Fatal("completed preconnect retained cancellation or channel state")
	}
	if got := tab.editor.Buffer.String(); got != "SELECT @session" {
		t.Fatalf("preconnect overwrote unsaved SQL: %q", got)
	}
}

func TestSQLPreconnectTabCloseRevokesPendingWork(t *testing.T) {
	s := New()
	s.openQuery()
	tab := s.currentTab()
	ctx, cancel := context.WithCancel(context.Background())
	tab.sqlPrepareCancel = cancel
	tab.sqlPrepareResults = make(chan error, 1)

	s.closeActive()
	if ctx.Err() == nil || tab.sqlPrepareResults != nil || tab.sqlPrepareCancel != nil {
		t.Fatal("closing tab did not revoke pending preconnect")
	}
	if len(s.tabs) != 1 {
		t.Fatalf("closed SQL tab still present: %d", len(s.tabs))
	}
}

func TestSQLPreconnectFailureDoesNotOverwriteExecutingQuery(t *testing.T) {
	s := New()
	s.openQuery()
	tab := s.currentTab()
	tab.sqlLoading = true
	ch := make(chan error, 1)
	ch <- errors.New("dial timeout")
	tab.sqlPrepareResults = ch
	s.consumeMySQLSQLResults()
	if tab.sqlError != "" {
		t.Fatalf("preconnect failure overwrote running query: %q", tab.sqlError)
	}
}
