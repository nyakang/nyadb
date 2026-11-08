package shell

import (
	"context"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"strings"
	"testing"
)

func TestCassandraPublicQueryBeforeConnectionKeepsNativeProfile(t *testing.T) {
	s, _, profiles, _ := profileTestState(t)
	p, _ := connections.Defaults(connections.Cassandra)
	p.Name = "Unconnected Cassandra"
	p.Database = "MixedCase"
	saved, err := profiles.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.loadProfiles()
	s.selectProfileByName(saved.Name)
	s.openQuery()
	q := s.currentTab()
	if !strings.HasPrefix(q.title, "CQL ") || q.query.ProfileID() != saved.ID || s.runtime.IsConnected(saved.ID) {
		t.Fatal("unconnected query lost native identity or connected")
	}
	if n := s.sqlNamespace(saved.ID, "MixedCase"); n != (contract.NamespaceRef{ProfileID: saved.ID, Catalog: "MixedCase"}) {
		t.Fatal("offline CQL keyspace became SQL schema", n)
	}
	tt := ui.NewTester(s.View, 1280, 800)
	if _, ok := tt.Find("CQL 编辑器"); !ok {
		t.Fatal("offline query has SQL editor label")
	}
}
