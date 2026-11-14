package shell

import (
	"context"
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/sessionbrowser"
)

func TestConsulScopeSaveRevokesParkedOriginalProfileAndPreservesActiveSession(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.Consul)
	p.Name = "original"
	original, err := service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	p.Name = "active"
	active, err := service.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	var revoked []int64
	s.sessions = sessionbrowser.New(sessionbrowser.Dependencies{Disconnected: func(id int64) { revoked = append(revoked, id) }})
	s.sessions.Current().ProfileID = active.ID
	s.sessions.Current().Generation = 91
	presentation := original
	presentation.Color = "blue"
	s.connectionSaved(presentation)
	if len(revoked) != 0 {
		t.Fatal("presentation edit revoked session")
	}
	changed := original
	changed.ConsulOptions.Namespace = "changed-ns"
	changed.ConsulOptions.Partition = "changed-partition"
	changed, err = service.Update(context.Background(), changed)
	if err != nil {
		t.Fatal(err)
	}
	s.connectionSaved(changed)
	if !reflect.DeepEqual(revoked, []int64{original.ID}) || s.sessions.Current().ProfileID != active.ID || s.sessions.Current().Generation != 91 {
		t.Fatal("scope change missed original parked session or revoked active profile")
	}
}
