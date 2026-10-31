package sessionbrowser

import (
	"context"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// EngineRuntime is injected by bootstrap/shell and does not expose drivers.
type EngineRuntime struct {
	Connect    func(context.Context, connections.Profile, string) error
	Session    func(int64) (contract.Session, error)
	Disconnect func(int64) error
	Close      func() error
}

func (s *State) SetEngineRuntime(r EngineRuntime) { s.engines = r }
func (s *State) EngineCatalog(id int64) string {
	w := s.Workspace(id)
	if w != nil && w.Kind == connections.PostgreSQL {
		return w.Catalog
	}
	return ""
}
func (s *State) EngineKind(id int64) connections.Kind {
	if w := s.Workspace(id); w != nil {
		return w.Kind
	}
	return ""
}
func (s *State) connectEngine(p connections.Profile, invalidate func()) {
	if s.engines.Connect == nil || s.engines.Session == nil {
		s.report("此引擎运行接口尚未接入")
		return
	}
	s.Activate(p.ID)
	w := s.current
	password := w.Password
	var vaultErr error
	if password == "" && s.deps.ReadSecret != nil {
		var exists bool
		password, exists, vaultErr = s.deps.ReadSecret(p.ID, "")
		if !exists {
			password = ""
		}
	}
	if w.ProfileID == p.ID {
		s.Disconnect(p.ID)
	}
	w = s.current
	ctx, cancel := context.WithCancel(context.Background())
	w.sessionCancel = cancel
	s.bumpGeneration(w)
	generation := w.Generation
	w.ProfileID, w.Status, w.Error = p.ID, "连接中", ""
	w.Kind, w.Catalog = p.Kind, p.Database
	w.Password = ""
	w.Databases = nil
	s.ResetBrowser()
	connect, lookup := s.engines.Connect, s.engines.Session
	results := make(chan sessionResult, 1)
	w.sessionResults = results
	s.report("正在连接：" + p.Name)
	go func() {
		e := connect(ctx, p, password)
		var names []string
		var metaErr error
		if e == nil && ctx.Err() == nil {
			session, er := lookup(p.ID)
			if er != nil {
				metaErr = er
			} else {
				names, metaErr = session.Schemas(ctx)
			}
		}
		results <- sessionResult{generation: generation, id: p.ID, names: names, err: e, metadataErr: metaErr, vaultErr: vaultErr}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) engineSchemas(ctx context.Context, id int64) ([]string, error) {
	session, e := s.engines.Session(id)
	if e != nil {
		return nil, e
	}
	return session.Schemas(ctx)
}
func (s *State) engineObjects(ctx context.Context, id int64, schema string) ([]mysqlsvc.MySQLDBObject, error) {
	session, e := s.engines.Session(id)
	if e != nil {
		return nil, e
	}
	objects, e := session.Objects(ctx, schema)
	if e != nil {
		return nil, e
	}
	out := make([]mysqlsvc.MySQLDBObject, len(objects))
	for i, o := range objects {
		kind := "表"
		if o.Ref.Kind == "view" {
			kind = "视图"
		}
		out[i] = mysqlsvc.MySQLDBObject{Name: o.Ref.Name, Kind: kind}
	}
	return out, nil
}
func (s *State) engineLabel(w *Workspace) string {
	if w.Kind == connections.PostgreSQL {
		return "PostgreSQL"
	}
	if w.Kind == connections.SQLite {
		return "SQLite"
	}
	return "MySQL"
}
func (s *State) engineStatus(w *Workspace, n int) string {
	return fmt.Sprintf("已连接 %s（%d 个命名空间）", s.engineLabel(w), n)
}
