package sqlworkspace

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/locales"
)

func persistenceError(err error) string {
	if errors.Is(err, queries.ErrKeyUnavailable) {
		return locales.Text("系统密钥服务不可用，无法保存工作区；已有加密内容已保留。请重试或另存 SQL 文件。")
	}
	return err.Error()
}
func (s *State) PersistenceBlocked() bool { return s.persistence.blocked && !s.persistence.loading }
func (s *State) RetryPersistence() {
	p := &s.persistence
	if p.store == nil || p.loading || p.closed || !p.blocked {
		return
	}
	p.loading = true
	store := p.store
	s.startIO(nil, nil, func() ioResult {
		store.RetryAuthentication()
		w, e := store.LoadWorkspace(context.Background())
		if e != nil {
			return ioResult{kind: "load", err: e}
		}
		n, e := store.ListNamed(context.Background(), 0)
		if e != nil {
			return ioResult{kind: "load", err: e}
		}
		catalogs, e := store.ListCatalogs(context.Background())
		return ioResult{kind: "load", workspace: w, names: n, catalogs: catalogs, err: e}
	})
}
