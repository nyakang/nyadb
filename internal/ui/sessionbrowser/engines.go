package sessionbrowser

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/locales"
)

// EngineRuntime is injected by bootstrap/shell and does not expose drivers.
type EngineRuntime struct {
	Open        func(context.Context, connections.Profile, string) (contract.Session, error)
	Connect     func(context.Context, connections.Profile, string) error
	Session     func(int64) (contract.Session, error)
	Disconnect  func(int64) error
	Close       func() error
	IsConnected func(int64) bool
}

func (s *State) SetEngineRuntime(r EngineRuntime) { s.engines = r }
func (s *State) EngineCatalog(id int64) string {
	w := s.Workspace(id)
	if w != nil && (w.Kind.IsPostgreSQLFamily() || w.Kind == connections.SQLServer || w.Kind == connections.Oracle || w.Kind == connections.Dameng || w.Kind == connections.DuckDB) {
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
	p.SQLiteOptions.Attachments = append([]connections.SQLiteAttachment(nil), p.SQLiteOptions.Attachments...)
	if s.engines.Connect == nil || s.engines.Session == nil {
		s.report("此引擎运行接口尚未接入")
		return
	}
	s.Activate(p.ID)
	w := s.current
	password := w.Password
	sentinelPassword := w.SentinelPassword
	var vaultErr error
	if password == "" && s.deps.ReadSecret != nil {
		var exists bool
		password, exists, vaultErr = s.deps.ReadSecret(p.ID, p.ActiveVariant)
		if !exists {
			password = ""
		}
	}
	if p.Kind == connections.Redis && p.RedisOptions.Topology == "sentinel" && sentinelPassword == "" && s.deps.ReadSecret != nil {
		var e error
		sentinelPassword, _, e = s.deps.ReadSecret(p.ID, connections.RedisSentinelCredential)
		vaultErr = errors.Join(vaultErr, e)
	}
	if w.ProfileID == p.ID {
		s.Disconnect(p.ID)
	}
	w = s.current
	ctx, cancel := context.WithCancel(context.Background())
	w.sessionCancel = cancel
	if p.Kind == connections.Redis {
		ctx = connections.WithRedisSentinelPassword(ctx, sentinelPassword)
	}
	s.bumpGeneration(w)
	generation := w.Generation
	w.ProfileID, w.Status, w.Error = p.ID, "连接中", ""
	w.Kind, w.Catalog = p.Kind, ""
	if p.Kind.IsPostgreSQLFamily() || p.Kind == connections.SQLServer || p.Kind == connections.Oracle || p.Kind == connections.Dameng {
		w.Catalog = p.Database
	} else if p.Kind == connections.SQLite {
		w.Catalog = "main"
	}
	w.Password = ""
	w.SentinelPassword = ""
	w.Databases = nil
	w.Namespaces = nil
	s.ResetBrowser()
	connect, lookup, open := s.engines.Connect, s.engines.Session, s.engines.Open
	results := make(chan sessionResult, 1)
	w.sessionResults = results
	s.report("正在连接：" + p.Name)
	go func() {
		var session contract.Session
		var e error
		if open != nil {
			session, e = open(ctx, p, password)
		} else {
			e = connect(ctx, p, password)
			if e == nil {
				session, e = lookup(p.ID)
			}
		}
		var names []string
		var namespaces []contract.Namespace
		var published uint64
		var metaErr error
		if e == nil && ctx.Err() == nil {
			published = session.Generation()
			provider, err := contract.Require[contract.MetadataProvider](session)
			if err != nil {
				metaErr = err
			} else {
				namespaces, metaErr = provider.Namespaces(ctx)
				for _, n := range namespaces {
					names = append(names, n.Label)
				}
			}
		}
		hide := func(err error) error {
			if err == nil {
				return nil
			}
			message := strings.ReplaceAll(err.Error(), "[hidden]", "[已隐藏]")
			if sentinelPassword != "" {
				message = strings.ReplaceAll(message, sentinelPassword, "[已隐藏]")
			}
			if password != "" {
				message = strings.ReplaceAll(message, password, "[已隐藏]")
			}
			return errors.New(message)
		}
		results <- sessionResult{generation: generation, id: p.ID, published: published, namespaces: namespaces, names: names, err: hide(e), metadataErr: hide(metaErr), vaultErr: vaultErr}
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
	return sessionNamespaces(ctx, session)
}
func (s *State) engineObjects(ctx context.Context, id int64, name string) ([]contract.DBObject, error) {
	session, e := s.engines.Session(id)
	if e != nil {
		return nil, e
	}
	return sessionObjects(ctx, session, name)
}
func sessionObjects(ctx context.Context, session contract.Session, name string) ([]contract.DBObject, error) {
	p, e := contract.Require[contract.MetadataProvider](session)
	if e != nil {
		return nil, e
	}
	n, e := contract.Require[interface {
		Namespace(string) contract.NamespaceRef
	}](session)
	if e != nil {
		return nil, e
	}
	return p.Browse(ctx, n.Namespace(name))
}
func sessionNamespaces(ctx context.Context, s contract.Session) ([]string, error) {
	p, e := contract.Require[contract.MetadataProvider](s)
	if e != nil {
		return nil, e
	}
	values, e := p.Namespaces(ctx)
	if e != nil {
		return nil, e
	}
	out := make([]string, len(values))
	for i, n := range values {
		out[i] = n.Label
	}
	return out, nil
}
func (s *State) engineLabel(w *Workspace) string {
	if w.Kind == connections.Redis {
		return "Redis"
	}
	if w.Kind == connections.MongoDB {
		return "MongoDB"
	}
	if w.Kind == connections.Dameng {
		return "达梦"
	}
	if w.Kind == connections.Oracle {
		return "Oracle"
	}
	if w.Kind == connections.DuckDB {
		return "DuckDB"
	}
	if w.Kind == connections.MariaDB {
		return "MariaDB"
	}
	if w.Kind == connections.OceanBase {
		return "OceanBase"
	}
	if w.Kind == connections.TiDB {
		return "TiDB"
	}
	if w.Kind == connections.SQLServer {
		return "SQL Server"
	}
	if w.Kind == connections.ClickHouse {
		return "ClickHouse"
	}
	if w.Kind == connections.CockroachDB {
		return "CockroachDB"
	}
	if w.Kind == connections.Kingbase {
		return "KingbaseES"
	}
	if w.Kind == connections.GaussDB {
		return "GaussDB"
	}
	if w.Kind == connections.OpenGauss {
		return "openGauss"
	}
	if w.Kind == connections.PostgreSQL {
		return "PostgreSQL"
	}
	if w.Kind == connections.SQLite {
		return "SQLite"
	}
	return "MySQL"
}
func (s *State) engineStatus(w *Workspace, n int) string {
	return fmt.Sprintf(locales.Text("已连接 %s（%d 个命名空间）"), s.engineLabel(w), n)
}
