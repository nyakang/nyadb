package dbruntime

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/engines"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func RegisterBuiltins(m *Manager, mysqlTemplate *mysqlsvc.MySQLSessionManager) {
	m.Register(connections.MySQL, ProviderFunc(func(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
		return mysqlsvc.OpenSession(ctx, p, password, mysqlTemplate)
	}))
	for _, kind := range []connections.Kind{connections.PostgreSQL, connections.SQLite} {
		m.Register(kind, ProviderFunc(func(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
			return engines.OpenSession(ctx, p, password)
		}))
	}
}

// Administration returns an operation-bound optional backend interface.
func (m *Manager) Administration(id int64) mysqlsvc.Administration {
	s, e := m.Session(id)
	if e != nil {
		return nil
	}
	if !s.Capabilities().DesignTables {
		return nil
	}
	p, e := contract.Require[interface {
		Administration() mysqlsvc.Administration
	}](s)
	if e != nil {
		return nil
	}
	return &administration{Administration: p.Administration(), id: id, generation: s.Generation(), session: s.(*session)}
}
