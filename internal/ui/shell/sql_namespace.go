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
		case connections.TDengine:
			if database == "" {
				database = p.Database
			}
			return contract.NamespaceRef{ProfileID: id, Catalog: database}
		case connections.QuestDB:
			return contract.NamespaceRef{ProfileID: id, Catalog: "qdb"}
		case connections.SelectDB:
			return contract.NamespaceRef{ProfileID: id, Catalog: p.SelectDBOptions.Catalog, Schema: database}
		case connections.Neo4j, connections.Cassandra, connections.MySQL, connections.MariaDB, connections.TiDB, connections.OceanBase, connections.TDSQL, connections.PolarDB, connections.GreatSQL, connections.Dolt:
			return contract.NamespaceRef{ProfileID: id, Catalog: database}
		case connections.PostgreSQL, connections.Cloudberry, connections.OpenTenBase, connections.CockroachDB, connections.OpenGauss, connections.GaussDB, connections.Kingbase, connections.SQLServer:
			return contract.NamespaceRef{ProfileID: id, Catalog: p.Database, Schema: database}
		}
	}
	return contract.NamespaceRef{ProfileID: id, Schema: database}
}
