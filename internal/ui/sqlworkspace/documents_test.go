package sqlworkspace

import (
	"errors"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/queries"
	"testing"
	"time"
)

func waitDocumentIO(t *testing.T, s *State) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(s.persistence.jobs) > 0 && time.Now().Before(deadline) {
		s.ConsumeDocuments(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if len(s.persistence.jobs) > 0 {
		t.Fatal("document worker did not finish")
	}
}
func TestSQLFileSaveFailureAndLateRevisionRetainDraft(t *testing.T) {
	s := New(Dependencies{})
	d := s.Open(2, "SELECT 1", 1, "db", false)
	d.Editor.Insert(" changed")
	s.SetPersistence(nil, Files{Write: func(string, string) error { return errors.New("disk failure") }}, nil)
	d.path = "query.sql"
	s.Save(d, false, nil)
	waitDocumentIO(t, s)
	if !d.Dirty() || d.err == "" {
		t.Fatal("failed save cleared draft")
	}
	started, release := make(chan struct{}), make(chan struct{})
	s.persistence.files.Write = func(string, string) error { close(started); <-release; return nil }
	s.Save(d, false, nil)
	<-started
	d.Editor.Insert(" newer")
	close(release)
	waitDocumentIO(t, s)
	if !d.Dirty() {
		t.Fatal("old save acknowledgement cleared new draft")
	}
}
func TestRestoreSQLDoesNotReserveOrExecuteLease(t *testing.T) {
	s := New(Dependencies{})
	d := s.RestoreDocument(queries.Document{ID: 7, Text: "UPDATE t SET v=1", SavedText: "", ProfileID: 4, Database: "db", Anchor: 2, Cursor: 5, Revision: 9})
	if !d.NeedsRecovery() || d.ProfileID() != 0 || d.lease != nil || d.RecoveryProfile() != 4 || !d.Dirty() {
		t.Fatal("restored SQL rebound to live session")
	}
	if d.Editor.Selection().Anchor != 2 || d.Editor.Cursor() != 5 {
		t.Fatal("selection lost")
	}
}
func TestSQLCloseGuardRequiresExplicitDecision(t *testing.T) {
	s := New(Dependencies{})
	d := s.Open(2, "", 1, "db", false)
	d.Editor.Insert("SELECT 1")
	if !s.GuardLeave(d, nil) || s.persistence.guardDoc != d {
		t.Fatal("dirty close not guarded")
	}
	s.persistence.guardDoc = nil
	if d.Editor.Buffer.String() != "SELECT 1" {
		t.Fatal("guard discarded text")
	}
}
