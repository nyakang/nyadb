package connectioneditor

import (
	"context"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/queries"
)

type InitialSQLStore interface {
	LoadInitialSQL(context.Context, string) (queries.InitialSQL, error)
	SaveInitialSQL(context.Context, queries.InitialSQL) error
}

type initialSQLResult struct {
	request uint64
	profile int64
	config  int
	id      string
	value   queries.InitialSQL
	err     error
}

func (s *State) SetInitialSQLStore(store InitialSQLStore) { s.initialSQLStore = store }
func (s *State) loadInitialSQL() {
	s.initialSQLRequest++
	s.initialSQLResults = nil
	s.mysqlInitialQuery = ""
	s.mysqlInitialQueryPreview = ""
	s.initialSQLSavedText = ""
	s.initialSQLLoadFailed = false
	id := s.connectionDraft.MySQLAdvanced.InitialSQLID
	if id == "" {
		return
	}
	s.initialSQLLoadFailed = true
	if s.initialSQLStore == nil {
		s.mysqlInitialQueryError = "加密初始 SQL 不可读取，原记录和配置引用保留"
		return
	}
	ch := make(chan initialSQLResult, 1)
	s.initialSQLResults = ch
	request, profile, config, store := s.initialSQLRequest, s.connectionDraft.ID, s.connectionActiveConfig, s.initialSQLStore
	go func() {
		v, e := store.LoadInitialSQL(context.Background(), id)
		ch <- initialSQLResult{request, profile, config, id, v, e}
	}()
}
func (s *State) consumeInitialSQL() {
	if s.initialSQLResults == nil {
		return
	}
	select {
	case r := <-s.initialSQLResults:
		s.initialSQLResults = nil
		if !s.active || r.request != s.initialSQLRequest || r.profile != s.connectionDraft.ID || r.config != s.connectionActiveConfig || r.id != s.connectionDraft.MySQLAdvanced.InitialSQLID {
			return
		}
		if r.err != nil {
			s.initialSQLLoadFailed = true
			s.mysqlInitialQueryError = r.err.Error()
			return
		}
		s.initialSQLLoadFailed = false
		s.mysqlInitialQueryError = ""
		s.mysqlInitialQuery = r.value.Text
		s.initialSQLSavedText = r.value.Text
		s.mysqlInitialQueryPreview = strings.Join(strings.Fields(r.value.Text), " ")
		s.captureConnectionConfiguration()
	default:
	}
}
func (s *State) prepareInitialSQLRefs(p *connections.Profile) {
	if p.Kind != connections.MySQL {
		return
	}
	if p.ID == 0 {
		if s.mysqlInitialQuery != s.initialSQLSavedText {
			p.MySQLAdvanced.InitialSQLID = queries.NewID()
		}
		return
	}
	if s.connectionMainConfig.initialSQL != s.connectionMainConfig.initialSaved {
		p.MySQLAdvanced.InitialSQLID = queries.NewID()
	}
	for i := range p.Variants {
		snap := s.connectionOtherConfigs[i]
		if snap.initialSQL != snap.initialSaved {
			p.Variants[i].MySQLAdvanced.InitialSQLID = queries.NewID()
		}
	}
}
func (s *State) persistInitialSQL(saved connections.Profile) error {
	write := func(id, text string) error {
		if s.initialSQLStore == nil {
			return queries.ErrKeyUnavailable
		}
		return s.initialSQLStore.SaveInitialSQL(context.Background(), queries.InitialSQL{ID: id, ProfileID: saved.ID, Text: text, Revision: 1})
	}
	if s.connectionDraft.ID == 0 {
		if s.mysqlInitialQuery != s.initialSQLSavedText {
			return write(saved.MySQLAdvanced.InitialSQLID, s.mysqlInitialQuery)
		}
		return nil
	}
	if s.connectionMainConfig.initialSQL != s.connectionMainConfig.initialSaved {
		if e := write(saved.MySQLAdvanced.InitialSQLID, s.connectionMainConfig.initialSQL); e != nil {
			return e
		}
	}
	for i, v := range saved.Variants {
		snap := s.connectionOtherConfigs[i]
		if snap.initialSQL != snap.initialSaved {
			if e := write(v.MySQLAdvanced.InitialSQLID, snap.initialSQL); e != nil {
				return e
			}
		}
	}
	return nil
}
