package shell

import (
	"context"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func (s *State) SetInitialSQLStore(store *queries.Store) {
	s.connectionEditor.SetInitialSQLStore(store)
	if s.sessions.Manager != nil {
		s.sessions.Manager.InitialSQLLoader = func(ctx context.Context, id string) (string, error) {
			v, e := store.LoadInitialSQL(ctx, id)
			return v.Text, e
		}
	}
}
func (s *State) mysqlObjectChanged(services ui.Services, event objectworkspace.ObjectEvent) {
	if event.Outcome.State == "not_executed" {
		return
	}
	w := s.sessions.Workspace(event.ProfileID)
	if w == nil || w.Generation != event.Generation {
		return
	}
	if event.Ref.Kind == "database" || event.Ref.Kind == "数据库" {
		if w == s.sessions.Current() {
			s.sessions.RefreshDatabases(services.Invalidate)
		}
	} else if s.sessions.InvalidateDatabaseObjects(event.ProfileID, event.Generation, event.Ref.Database) && w == s.sessions.Current() && w.Database == event.Ref.Database {
		s.loadMySQLObjects(services, event.Ref.Database, true)
	}
	if event.Action == "drop" && event.Outcome.Verified {
		s.removeTabs(func(t *tab) bool {
			if t.object != nil && t.object.ProfileID() == event.ProfileID && t.object.Database() == event.Ref.Database {
				return event.Ref.Kind == "database" || event.Ref.Kind == "数据库" || t.object.Table() == event.Ref.Name
			}
			if t.data != nil && t.data.ProfileID() == event.ProfileID && t.data.Database() == event.Ref.Database {
				return event.Ref.Kind == "database" || event.Ref.Kind == "数据库" || t.data.Table() == event.Ref.Name
			}
			return false
		})
	}
}

// EnableAutoConnect is bootstrap-only; each opted profile is scheduled once.
// Restored SQL pages remain disconnected and never replay their text.
func (s *State) EnableAutoConnect() { s.autoConnectPending = true }
func (s *State) startAutoConnect(services ui.Services) {
	if !s.autoConnectPending {
		return
	}
	s.autoConnectPending = false
	original := s.sessions.Current().ProfileID
	for _, p := range s.navigation.Profiles() {
		settings, e := connections.ResolveActiveSettings(p)
		if e == nil && settings.MySQLAdvanced.AutoConnect {
			s.sessions.Connect(p, services.Invalidate)
		}
	}
	s.sessions.Activate(original)
}
