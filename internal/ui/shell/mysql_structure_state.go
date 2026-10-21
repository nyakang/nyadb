package shell

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// The UI owns tabs and their cache. Queries only publish immutable results
// through a one-shot channel; no goroutine may mutate State or tab directly.
type mysqlStructureLoader func(context.Context, int64, string, string) (*connections.MySQLTableStructure, error)

type mysqlStructureResult struct {
	sessionGeneration uint64
	profileID         int64
	request           uint64
	data              *connections.MySQLTableStructure
	err               error
}

func (s *State) openMySQLStructure(services ui.Services, database, table string) {
	if s.mysqlSessionStatus != "已连接" || s.mysqlSessionProfileID <= 0 ||
		!validStructureSelection(database, table) {
		return
	}
	for i, t := range s.tabs {
		if t.kind == tabStructure && t.structureDatabase == database &&
			t.structureTable == table && t.structureProfileID == s.mysqlSessionProfileID {
			s.activeTab = i
			if t.structure == nil && !t.structureLoading && t.structureErr == "" {
				s.loadMySQLStructure(services, t, false)
			}
			return
		}
	}
	s.nextTabID++
	t := &tab{
		id: s.nextTabID, kind: tabStructure, title: table,
		structureDatabase: database, structureTable: table,
		structureProfileID: s.mysqlSessionProfileID,
	}
	s.tabs = append(s.tabs, t)
	s.activeTab = len(s.tabs) - 1
	s.loadMySQLStructure(services, t, false)
}

func validStructureSelection(database, table string) bool {
	return database != "" && table != ""
}

func (s *State) cancelMySQLStructure(t *tab) {
	t.structureRequest++
	if t.structureCancel != nil {
		t.structureCancel()
	}
	t.structureCancel = nil
	t.structureResults = nil
	t.structureLoading = false
}

func (s *State) loadMySQLStructure(services ui.Services, t *tab, refresh bool) {
	if t == nil || t.kind != tabStructure || t.structureProfileID != s.mysqlSessionProfileID ||
		s.mysqlSessionStatus != "已连接" || s.mysqlSessionProfileID <= 0 {
		return
	}
	if t.structureLoading && !refresh {
		return
	}
	if t.structure != nil && !refresh {
		return
	}
	s.cancelMySQLStructure(t)
	t.structureErr = ""
	t.structureLoading = true
	ctx, cancel := context.WithCancel(context.Background())
	t.structureCancel = cancel
	request := t.structureRequest
	generation := s.mysqlSessionGeneration
	profile := s.mysqlSessionProfileID
	database, table := t.structureDatabase, t.structureTable
	loader := s.mysqlStructureLoader
	if loader == nil {
		if s.mysqlSessionManager == nil {
			s.cancelMySQLStructure(t)
			t.structureErr = "MySQL 会话不可用"
			return
		}
		loader = s.mysqlSessionManager.TableStructure
	}
	out := make(chan mysqlStructureResult, 1)
	t.structureResults = out
	go func() {
		data, err := loader(ctx, profile, database, table)
		out <- mysqlStructureResult{
			sessionGeneration: generation, profileID: profile,
			request: request, data: data, err: err,
		}
		services.Invalidate()
	}()
}

func (s *State) consumeMySQLStructures() {
	for _, t := range s.tabs {
		if t.kind != tabStructure || t.structureResults == nil {
			continue
		}
		select {
		case result := <-t.structureResults:
			if result.sessionGeneration != s.mysqlSessionGeneration ||
				result.profileID != s.mysqlSessionProfileID ||
				result.profileID != t.structureProfileID ||
				result.request != t.structureRequest {
				continue
			}
			s.cancelMySQLStructure(t)
			if result.err != nil {
				if !errors.Is(result.err, context.Canceled) {
					t.structureErr = result.err.Error()
				}
				continue
			}
			if result.data == nil {
				t.structureErr = "服务器未返回表结构"
				continue
			}
			t.structure = result.data
			t.structureErr = ""
		default:
		}
	}
}

// Removing tabs also revokes their in-flight work. Inactive SQL/mock tabs are
// intentionally preserved so unrelated workspace documents are not lost.
func (s *State) clearMySQLStructureTabs() {
	activeID := s.currentTab().id
	kept := s.tabs[:0]
	for _, t := range s.tabs {
		if t.kind == tabStructure {
			s.cancelMySQLStructure(t)
			continue
		}
		kept = append(kept, t)
	}
	s.tabs = kept
	s.activeTab = 0
	for i, t := range kept {
		if t.id == activeID {
			s.activeTab = i
			break
		}
	}
}

func (s *State) pruneMySQLStructureTabs(known map[string]bool) {
	activeID := s.currentTab().id
	kept := s.tabs[:0]
	for _, t := range s.tabs {
		if t.kind == tabStructure && !known[t.structureDatabase] {
			s.cancelMySQLStructure(t)
			continue
		}
		kept = append(kept, t)
	}
	s.tabs = kept
	s.activeTab = 0
	for i, t := range kept {
		if t.id == activeID {
			s.activeTab = i
			break
		}
	}
}
