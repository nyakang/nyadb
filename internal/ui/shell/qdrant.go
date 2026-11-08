package shell

import (
	"net/url"

	"github.com/nyakang/nyadb/locales"
)

// A point browser starts an explicitly executed native read, owned by the
// original profile and collection. It never creates an unsupported data grid.
func (s *State) openQdrantPoints(profileID int64, collection string) {
	if s.sqlWorkspace.PersistenceLoading() {
		s.status = locales.Text("等待加载")
		return
	}
	s.nextTabID++
	source := "POST /collections/" + url.PathEscape(collection) + "/points/scroll\n{\"limit\":100,\"with_payload\":true,\"with_vector\":true}"
	t := &tab{id: s.nextTabID, title: collection + " · Qdrant REST", kind: tabSQL}
	t.query = s.sqlWorkspace.Open(t.id, source, profileID, collection, false)
	s.tabs = append(s.tabs, t)
	s.activeTab = len(s.tabs) - 1
	s.sqlWorkspace.Prepare(t.query)
	s.sqlWorkspace.Focus(t.query)
}
