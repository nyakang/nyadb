// Package sqlworkspace owns SQL documents, execution, results and physical
// session leases. Its host owns workbench tabs and profile activation.
package sqlworkspace

import (
	"context"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type Executor func(context.Context, int64, string, string) (mysqlsvc.MySQLSQLResult, error)

// Lease is one document's physical SQL session. Replace revokes the old lease
// without executing SQL. Release is idempotent and affects only this lease.
type Lease interface {
	Preconnect(context.Context) error
	Execute(context.Context, string, string) (mysqlsvc.MySQLSQLResult, error)
	Replace() (Lease, error)
	Release()
}
type Runtime interface {
	IsConnected(int64) bool
	Reserve(int64, uint64) (Lease, error)
}

// MySQLRuntime adapts the existing service without exposing its tokens to shell.
func MySQLRuntime(manager *mysqlsvc.MySQLSessionManager) Runtime {
	if manager == nil {
		return nil
	}
	return mySQLRuntime{manager: manager}
}

type mySQLRuntime struct{ manager *mysqlsvc.MySQLSessionManager }

func (r mySQLRuntime) IsConnected(id int64) bool { return r.manager.IsConnected(id) }
func (r mySQLRuntime) Reserve(id int64, tabID uint64) (Lease, error) {
	token, err := r.manager.ReserveSQLTabToken(id, tabID)
	if err != nil {
		return nil, err
	}
	return &mySQLLease{manager: r.manager, token: token}, nil
}

type mySQLLease struct {
	manager *mysqlsvc.MySQLSessionManager
	token   mysqlsvc.MySQLSQLTabToken
}

func (l *mySQLLease) Preconnect(ctx context.Context) error {
	return l.manager.PreconnectSQLTabToken(ctx, l.token)
}
func (l *mySQLLease) Execute(ctx context.Context, database, statement string) (mysqlsvc.MySQLSQLResult, error) {
	return l.manager.ExecuteSQLTabToken(ctx, l.token, database, statement)
}
func (l *mySQLLease) Replace() (Lease, error) {
	token, err := l.manager.ReplaceSQLTabToken(l.token)
	if err != nil {
		return nil, err
	}
	return &mySQLLease{manager: l.manager, token: token}, nil
}
func (l *mySQLLease) Release() { l.manager.ReleaseSQLTabToken(l.token) }

type Dependencies struct {
	Completions      func(int64, string) []string
	StatementAtCaret func(int64, string, int) string
	FormatSQL        func(int64, string) (string, error)
	ValidateExplain  func(int64, string) error
	Runtime          func() Runtime
	Connected        func(int64) bool
	Active           func(*Document) bool
	Status           func(string)
	Recovered        func(int64)
	Preview          func(*ui.Context)
}

// Document is owned by the UI thread. Workers capture a lease/executor and
// immutable request values; only Consume reads their private buffered channels.
type Document struct {
	name, path, queryID, savedText, text   string
	revision, bufferVersion, namedRevision uint64
	resultSet, selectedRow                 int
	selection                              ui.Selection[int]

	Editor                   *editor.State
	saving                   bool
	id                       uint64
	profileID                int64
	database                 string
	lease                    Lease
	recoveryNeeded           bool
	recoveryProfile          int64
	recoverySchema           string
	loading, cancelRequested bool
	err                      string
	result                   *mysqlsvc.MySQLSQLResult
	grid                     ui.ListState
	cancel                   context.CancelFunc
	results                  <-chan executionResult
	request                  uint64
	prepareCancel            context.CancelFunc
	prepareResults           <-chan error
	preview, closed          bool
}

func (d *Document) ProfileID() int64                 { return d.profileID }
func (d *Document) Database() string                 { return d.database }
func (d *Document) Loading() bool                    { return d.loading }
func (d *Document) CancelRequested() bool            { return d.cancelRequested }
func (d *Document) Error() string                    { return d.err }
func (d *Document) Result() *mysqlsvc.MySQLSQLResult { return d.result }
func (d *Document) NeedsRecovery() bool              { return d.recoveryNeeded }
func (d *Document) RecoveryProfile() int64           { return d.recoveryProfile }
func (d *Document) RecoverySchema() string           { return d.recoverySchema }

// State owns all SQL documents and their shared split position. Executor and
// Invalidate are injectable; background jobs capture them before starting.
type State struct {
	SplitPercent float32
	Executor     Executor
	Invalidate   func()
	documents    map[uint64]*Document
	demoRuns     int
	deps         Dependencies
	persistence  persistence
}

func New(deps Dependencies) *State {
	return &State{SplitPercent: 48, documents: make(map[uint64]*Document), deps: deps}
}
func (s *State) Open(id uint64, text string, profileID int64, database string, preview bool) *Document {
	if previous := s.documents[id]; previous != nil {
		s.Close(previous)
	}
	buffer := editor.NewBuffer(text)
	buffer.SetLanguage(editor.LanguageNamed("SQL"))
	d := &Document{id: id, profileID: profileID, database: database, preview: preview, Editor: &editor.State{Buffer: buffer, WrapGuides: []int{100}}}
	d.Editor.OnCompletion = func(r editor.CompletionRequest) { s.completions(d, r) }
	d.savedText, d.text, d.bufferVersion = text, text, buffer.Version()
	s.documents[id] = d
	return d
}
func (s *State) runtime() Runtime {
	if s.deps.Runtime != nil {
		return s.deps.Runtime()
	}
	return nil
}
func (s *State) connected(id int64) bool { return s.deps.Connected != nil && s.deps.Connected(id) }
func (s *State) active(d *Document) bool { return s.deps.Active == nil || s.deps.Active(d) }
func (s *State) report(message string) {
	if s.deps.Status != nil {
		s.deps.Status(message)
	}
}
func (s *State) DemoRuns() int { return s.demoRuns }

// Prepare reserves a new document's lease without blocking the UI. An invalid
// document requires explicit recovery and cannot silently obtain another lease.
func (s *State) Prepare(d *Document) {
	if d == nil || d.closed || d.preview || d.recoveryNeeded || d.profileID <= 0 || d.lease != nil || d.prepareResults != nil {
		return
	}
	runtime := s.runtime()
	if runtime == nil || !runtime.IsConnected(d.profileID) {
		return
	}
	lease, err := runtime.Reserve(d.profileID, d.id)
	if err != nil {
		d.err = "SQL 会话预留失败：" + err.Error()
		s.report(d.err)
		return
	}
	d.lease = lease
	s.preconnect(d)
}
func (s *State) Close(d *Document) {
	if d == nil || d.closed {
		return
	}
	s.dispose(d)
	p := &s.persistence
	if p.guardDoc == d {
		p.guardDoc, p.guardNext = nil, nil
	}
	if p.nameDoc == d {
		p.nameDoc, p.nameOpen = nil, false
	}
	if p.explainDoc == d {
		p.explainDoc, p.explainOpen = nil, false
	}
	for _, job := range p.jobs {
		if job.doc == d {
			job.next = nil
		}
	}
	d.Editor, d.result = nil, nil
	d.grid = ui.ListState{}
	d.closed = true
	if s.documents[d.id] == d {
		delete(s.documents, d.id)
	}
}
func (s *State) CloseAll() {
	s.persistence.closed = true
	if s.persistence.timer != nil {
		s.persistence.timer.Stop()
	}
	s.persistence.jobs = nil
	s.persistence.pending = nil
	s.persistence.restored = nil
	s.persistence.flushNext, s.persistence.awaitClose, s.persistence.guardNext, s.persistence.exitNext = nil, nil, nil, nil
	s.persistence.nameDoc, s.persistence.guardDoc, s.persistence.explainDoc = nil, nil, nil
	for _, d := range s.documents {
		s.Close(d)
	}
}
