package connectioneditor

import (
	"github.com/nyakang/nyadb/internal/services/connections"
	"testing"
)

func TestURIBackRetainsRawDraftAndReplacementKeepsIdentity(t *testing.T) {
	s, _, _, _ := profileTestState(t)
	if err := s.OpenNew(connections.MySQL, "group"); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.ID = 42
	s.connectionDraft.Name = "production"
	s.connectionDraft.Notes = "notes"
	s.connectionDraft.Starred = true
	s.connectionPort = "invalid port"
	s.connectionSecret = "old secret"
	s.connectionEditorTab = 4
	s.BeginURIFromEditor()
	s.uriText = "bad uri"
	s.ReturnFromURI()
	if !s.active || s.connectionPort != "invalid port" || s.connectionSecret != "old secret" || s.connectionEditorTab != 4 {
		t.Fatal("URI navigation discarded form")
	}
	s.BeginURIFromEditor()
	if err := s.ImportURI("postgres://new:replacement@localhost/newdb"); err != nil {
		t.Fatal(err)
	}
	p := s.connectionDraft
	if p.ID != 42 || p.Name != "production" || p.GroupName != "group" || !p.Starred || p.Notes != "notes" || p.Kind != connections.PostgreSQL || p.Database != "newdb" || s.connectionSecret != "replacement" || s.connectionSavePassword {
		t.Fatal("replacement lost identity or opted into vault save")
	}
}

func TestConnectionValidationLocatesErrorAndPreservesInput(t *testing.T) {
	s, _, _, _ := profileTestState(t)
	_ = s.OpenNew(connections.MySQL, "")
	s.connectionDraft.Name = "fixture"
	s.connectionPort = "not a port"
	s.connectionEditorTab = 4
	s.Save()
	if s.validationField != "端口" || s.connectionEditorTab != 0 || s.connectionPort != "not a port" {
		t.Fatal("validation did not locate raw port input", s.Error())
	}
}
