// Package datainspector owns data-table information, exact row counts and
// bounded field previews. The host supplies read-only grid/session snapshots.
package datainspector

import (
	"context"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type InfoLoader func(context.Context, int64, string, string) (*mysqlsvc.MySQLTableInfo, error)
type CountLoader func(context.Context, int64, string, string) (int64, error)
type ChunkLoader func(context.Context, int64, string, string, string, mysqlsvc.MySQLRowRef, int64, int) (*mysqlsvc.MySQLFieldChunk, error)
type Session struct {
	ProfileID  int64
	Generation uint64
	Connected  bool
}
type Dependencies struct {
	Session     func() Session
	InfoLoader  func() InfoLoader
	CountLoader func() CountLoader
	ChunkLoader func() ChunkLoader
	Profile     func(int64) (connections.Profile, bool)
	Show, Hide  func()
}

// Panel belongs to one data tab. Identity is immutable; private channels and
// cancellation functions are consumed only on the UI thread.
type Panel struct {
	profileID               int64
	database, table         string
	selection               func() Selection
	info                    infoState
	chunkField              string
	chunkRow                int
	chunkPage               *mysqlsvc.MySQLDataPage
	chunkNext, chunkTotal   int64
	chunkText, chunkErr     string
	chunkDone, chunkLoading bool
	chunkCancel             context.CancelFunc
	chunkResults            <-chan chunkResult
	chunkRequest            uint64
	closed                  bool
}

func (p *Panel) Selection() Selection {
	if p.selection != nil {
		return p.selection()
	}
	return Selection{Row: -1}
}
func (p *Panel) FieldMode() bool                { return p.info.fieldMode }
func (p *Panel) Info() *mysqlsvc.MySQLTableInfo { return p.info.info }
func (p *Panel) InfoLoading() bool              { return p.info.loading }
func (p *Panel) InfoError() string              { return p.info.err }
func (p *Panel) Count() *int64                  { return p.info.count }
func (p *Panel) CountAt() time.Time             { return p.info.countAt }
func (p *Panel) CountLoading() bool             { return p.info.countLoading }
func (p *Panel) CountError() string             { return p.info.countErr }
func (p *Panel) ChunkLoading() bool             { return p.chunkLoading }
func (p *Panel) ChunkNext() int64               { return p.chunkNext }
func (p *Panel) ChunkTotal() int64              { return p.chunkTotal }
func (p *Panel) ChunkText() string              { return p.chunkText }
func (p *Panel) ChunkError() string             { return p.chunkErr }

type State struct {
	InfoLoader  InfoLoader
	CountLoader CountLoader
	ChunkLoader ChunkLoader
	deps        Dependencies
	panels      map[*Panel]struct{}
}

func New(deps Dependencies) *State { return &State{deps: deps, panels: make(map[*Panel]struct{})} }
func (s *State) session() Session {
	if s.deps.Session != nil {
		return s.deps.Session()
	}
	return Session{}
}
func (s *State) connected(p *Panel) bool {
	w := s.session()
	return p != nil && !p.closed && p.profileID > 0 && p.profileID == w.ProfileID && w.Connected
}
func (s *State) Open(profileID int64, database, table string, selection func() Selection) *Panel {
	p := &Panel{profileID: profileID, database: database, table: table, selection: selection, chunkRow: -1}
	s.panels[p] = struct{}{}
	return p
}
func (s *State) ShowField(p *Panel) {
	if p == nil || p.closed {
		return
	}
	p.info.fieldMode = true
	if s.deps.Show != nil {
		s.deps.Show()
	}
}
func (s *State) Close(p *Panel) {
	if p == nil || p.closed {
		return
	}
	s.CancelInfo(p)
	s.CancelChunk(p)
	p.closed = true
	delete(s.panels, p)
}
func (s *State) CloseAll() {
	for p := range s.panels {
		s.Close(p)
	}
}
