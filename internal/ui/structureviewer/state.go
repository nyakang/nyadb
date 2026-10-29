// Package structureviewer owns read-only MySQL structure documents and their
// asynchronous metadata requests. The host owns tabs and connection activation.
package structureviewer

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type Loader func(context.Context, int64, string, string) (*mysqlsvc.MySQLTableStructure, error)

// Session is a UI-thread snapshot. A parked profile's results remain buffered
// until the host activates it; its generation must still match on consumption.
type Session struct {
	ProfileID  int64
	Generation uint64
	Connected  bool
}

type Dependencies struct {
	Session func() Session
	Loader  func() Loader
	Back    func()
}

// Document owns its immutable object identity and mutable presentation state.
// Only the UI thread changes it; workers publish to private buffered channels.
type Document struct {
	id                            uint64
	profileID                     int64
	database, table               string
	data                          *mysqlsvc.MySQLTableStructure
	page, fieldRow                int
	columns, indexes, foreignKeys ui.ListState
	loading                       bool
	err                           string
	cancel                        context.CancelFunc
	results                       <-chan loadResult
	request                       uint64
	closed                        bool
}

func (d *Document) ProfileID() int64 { return d.profileID }
func (d *Document) Database() string { return d.database }
func (d *Document) Table() string    { return d.table }

// Data returns metadata for read-only host presentation.
func (d *Document) Data() *mysqlsvc.MySQLTableStructure { return d.data }
func (d *Document) Loading() bool                       { return d.loading }
func (d *Document) Error() string                       { return d.err }
func (d *Document) SelectedField() (mysqlsvc.MySQLStructureColumn, bool) {
	if d.data == nil || d.fieldRow < 0 || d.fieldRow >= len(d.data.Columns) {
		return mysqlsvc.MySQLStructureColumn{}, false
	}
	return d.data.Columns[d.fieldRow], true
}

type loadResult struct {
	generation uint64
	profileID  int64
	request    uint64
	data       *mysqlsvc.MySQLTableStructure
	err        error
}

type State struct {
	Loader    Loader
	deps      Dependencies
	documents map[uint64]*Document
}

func New(deps Dependencies) *State { return &State{deps: deps, documents: make(map[uint64]*Document)} }
func (s *State) session() Session {
	if s.deps.Session != nil {
		return s.deps.Session()
	}
	return Session{}
}

// Open registers a document without loading metadata or changing the host tab.
func (s *State) Open(id uint64, profileID int64, database, table string) *Document {
	if previous := s.documents[id]; previous != nil {
		s.Close(previous)
	}
	d := &Document{id: id, profileID: profileID, database: database, table: table}
	s.documents[id] = d
	return d
}

// Load uses cached metadata unless refresh is explicit. Refresh revokes the
// prior worker before starting another. It never executes user-authored SQL or
// modifies a table.
func (s *State) Load(d *Document, invalidate func(), refresh bool) {
	session := s.session()
	if d == nil || d.closed || d.profileID <= 0 || d.profileID != session.ProfileID || !session.Connected || d.database == "" || d.table == "" {
		return
	}
	if !refresh && (d.loading || d.data != nil || d.err != "") {
		return
	}
	s.cancel(d)
	d.err = ""
	loader := s.Loader
	if loader == nil && s.deps.Loader != nil {
		loader = s.deps.Loader()
	}
	if loader == nil {
		d.err = "MySQL 会话不可用"
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.loading = cancel, true
	request, profile, generation := d.request, d.profileID, session.Generation
	database, table := d.database, d.table
	out := make(chan loadResult, 1)
	d.results = out
	go func() {
		data, err := loader(ctx, profile, database, table)
		out <- loadResult{generation: generation, profileID: profile, request: request, data: data, err: err}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) cancel(d *Document) {
	d.request++
	if d.cancel != nil {
		d.cancel()
	}
	d.cancel, d.results, d.loading = nil, nil, false
}

// Cancel preserves the existing read-only cache while revoking pending work.
func (s *State) Cancel(d *Document) {
	if d == nil || d.closed {
		return
	}
	s.cancel(d)
	d.err = "加载已取消"
}
func (s *State) Close(d *Document) {
	if d == nil || d.closed {
		return
	}
	s.cancel(d)
	d.closed = true
	if s.documents[d.id] == d {
		delete(s.documents, d.id)
	}
}
func (s *State) CloseAll() {
	for _, d := range s.documents {
		s.Close(d)
	}
}

// Consume runs on the UI thread. Generation changes invalidate completed work
// without leaving a document stuck loading or automatically retrying it.
func (s *State) Consume() {
	session := s.session()
	for _, d := range s.documents {
		if d.results == nil || d.profileID != session.ProfileID {
			continue
		}
		select {
		case r := <-d.results:
			if r.profileID != d.profileID || r.request != d.request {
				continue
			}
			s.cancel(d)
			if r.generation != session.Generation || !session.Connected {
				d.err = "原 MySQL 会话已失效，请刷新表结构"
				continue
			}
			if r.err != nil {
				if !errors.Is(r.err, context.Canceled) {
					d.err = r.err.Error()
				}
				continue
			}
			if r.data == nil {
				d.err = "服务器未返回表结构"
				continue
			}
			d.data, d.err = r.data, ""
		default:
		}
	}
}
