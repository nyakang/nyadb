package connectioneditor

import (
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDamengURIRequiresRegisteredWorkbenchAndPreservesExistingDraft(t *testing.T) {
	s, _, _, _ := profileTestState(t)
	if err := s.OpenNew(connections.PostgreSQL, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name, s.connectionSecret = "existing draft", "existing-secret"
	s.BeginURIFromEditor()
	before := s.connectionDraft
	q := url.Values{"library_file": {filepath.Join(t.TempDir(), "libdmdpi.so")}, "sslmode": {"disable"}}
	u := url.URL{Scheme: "dameng", Host: "localhost", Path: "/DAMENG", User: url.UserPassword("SYSDBA", "replacement-secret"), RawQuery: q.Encode()}
	if err := s.ImportURI(u.String()); !errors.Is(err, connections.ErrURI) {
		t.Fatal("unregistered native workbench exposed through URI import", err)
	}
	if !reflect.DeepEqual(s.connectionDraft, before) || s.connectionSecret != "existing-secret" || !s.URIActive() || !s.uriReturn {
		t.Fatal("rejected native URI replaced a parked editor or credential")
	}
	s.ReturnFromURI()
	if !s.active || s.URIActive() || !reflect.DeepEqual(s.connectionDraft, before) {
		t.Fatal("existing editor could not resume after rejected native URI")
	}
}
