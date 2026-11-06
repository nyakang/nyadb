package shell

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *State) sqlNamespace(id int64, database string) contract.NamespaceRef {
	n := s.runtime.Namespace(id, database)
	if n.Catalog != "" || n.Schema != "" {
		return n
	}
	for _, p := range s.navigation.Profiles() {
		if p.ID != id {
			continue
		}
		switch p.Kind {
		case connections.MySQL, connections.MariaDB, connections.TiDB, connections.OceanBase:
			return contract.NamespaceRef{ProfileID: id, Catalog: database}
		case connections.PostgreSQL, connections.CockroachDB, connections.SQLServer:
			return contract.NamespaceRef{ProfileID: id, Catalog: p.Database, Schema: database}
		}
	}
	return contract.NamespaceRef{ProfileID: id, Schema: database}
}
