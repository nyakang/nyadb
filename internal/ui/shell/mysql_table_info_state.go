package shell

import (
	"context"
	"errors"
	"time"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

type mysqlTableInfoLoader func(context.Context, int64, string, string) (*mysqlsvc.MySQLTableInfo, error)
type mysqlTableCountLoader func(context.Context, int64, string, string) (int64, error)

type mysqlTableInfoState struct {
	fieldMode          bool
	info               *mysqlsvc.MySQLTableInfo
	attempted, loading bool
	err                string
	request            uint64
	cancel             context.CancelFunc
	results            <-chan mysqlTableInfoResult
	count              *int64
	countAt            time.Time
	countLoading       bool
	countErr           string
	countRequest       uint64
	countCancel        context.CancelFunc
	countResults       <-chan mysqlTableCountResult
}

type mysqlTableInfoResult struct {
	generation      uint64
	profileID       int64
	request         uint64
	database, table string
	info            *mysqlsvc.MySQLTableInfo
	err             error
}

type mysqlTableCountResult struct {
	generation      uint64
	profileID       int64
	request         uint64
	database, table string
	count           int64
	at              time.Time
	err             error
}

func (s *State) cancelMySQLTableInfo(t *tab) {
	if t == nil {
		return
	}
	d := &t.dataInfo
	d.request++
	if d.cancel != nil {
		d.cancel()
	}
	d.cancel, d.results, d.loading = nil, nil, false
	s.cancelMySQLTableCount(t)
}

func (s *State) cancelMySQLTableCount(t *tab) {
	d := &t.dataInfo
	d.countRequest++
	if d.countCancel != nil {
		d.countCancel()
	}
	d.countCancel, d.countResults, d.countLoading = nil, nil, false
}

func (s *State) loadMySQLTableInfo(services ui.Services, t *tab, refresh bool) {
	if t == nil || t.kind != tabMySQLData || t.dataProfileID != s.mysqlSessionProfileID ||
		s.mysqlSessionProfileID <= 0 || s.mysqlSessionStatus != "已连接" {
		return
	}
	d := &t.dataInfo
	if !refresh && (d.loading || d.attempted) {
		return
	}
	// Metadata refresh must not cancel an independent exact-row-count request.
	d.request++
	if d.cancel != nil {
		d.cancel()
	}
	d.cancel, d.results, d.loading = nil, nil, false
	d.attempted, d.err = true, ""
	loader := s.mysqlTableInfoLoader
	if loader == nil {
		if s.mysqlSessionManager == nil {
			d.err = locales.Text("MySQL 会话不可用")
			return
		}
		loader = s.mysqlSessionManager.TableInfo
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	d.cancel, d.loading = cancel, true
	request, generation, profile := d.request, s.mysqlSessionGeneration, t.dataProfileID
	database, table := t.dataDatabase, t.dataTable
	out := make(chan mysqlTableInfoResult, 1)
	d.results = out
	go func() {
		info, err := loader(ctx, profile, database, table)
		out <- mysqlTableInfoResult{generation, profile, request, database, table, info, err}
		services.Invalidate()
	}()
}

func (s *State) loadMySQLTableCount(services ui.Services, t *tab) {
	if t == nil || t.kind != tabMySQLData || t.dataProfileID != s.mysqlSessionProfileID ||
		s.mysqlSessionProfileID <= 0 || s.mysqlSessionStatus != "已连接" || t.dataInfo.countLoading {
		return
	}
	d := &t.dataInfo
	s.cancelMySQLTableCount(t)
	d.countErr = ""
	loader := s.mysqlTableCountLoader
	if loader == nil {
		if s.mysqlSessionManager == nil {
			d.countErr = locales.Text("MySQL 会话不可用")
			return
		}
		loader = s.mysqlSessionManager.CountTableRows
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	d.countCancel, d.countLoading = cancel, true
	request, generation, profile := d.countRequest, s.mysqlSessionGeneration, t.dataProfileID
	database, table := t.dataDatabase, t.dataTable
	out := make(chan mysqlTableCountResult, 1)
	d.countResults = out
	go func() {
		count, err := loader(ctx, profile, database, table)
		out <- mysqlTableCountResult{generation, profile, request, database, table, count, time.Now(), err}
		services.Invalidate()
	}()
}

func (s *State) consumeMySQLTableInfo() {
	for _, t := range s.tabs {
		if t.kind != tabMySQLData || t.dataProfileID != s.mysqlSessionProfileID {
			continue
		}
		d := &t.dataInfo
		select {
		case result := <-d.results:
			if result.generation != s.mysqlSessionGeneration || result.profileID != s.mysqlSessionProfileID ||
				result.profileID != t.dataProfileID || result.request != d.request ||
				result.database != t.dataDatabase || result.table != t.dataTable {
				continue
			}
			if d.cancel != nil {
				d.cancel()
			}
			d.cancel, d.results, d.loading = nil, nil, false
			if result.err != nil {
				if !errors.Is(result.err, context.Canceled) {
					d.err = result.err.Error()
				}
			} else if result.info == nil || result.info.Database != t.dataDatabase || result.info.Name != t.dataTable {
				d.err = locales.Text("服务器未返回当前表的信息")
			} else {
				d.info, d.err = result.info, ""
			}
		default:
		}
		select {
		case result := <-d.countResults:
			if result.generation != s.mysqlSessionGeneration || result.profileID != s.mysqlSessionProfileID ||
				result.profileID != t.dataProfileID || result.request != d.countRequest ||
				result.database != t.dataDatabase || result.table != t.dataTable {
				continue
			}
			if d.countCancel != nil {
				d.countCancel()
			}
			d.countCancel, d.countResults, d.countLoading = nil, nil, false
			if result.err != nil {
				if !errors.Is(result.err, context.Canceled) {
					d.countErr = result.err.Error()
				}
			} else if result.count < 0 {
				d.countErr = locales.Text("服务器返回了无效行数")
			} else {
				count := result.count
				d.count, d.countAt, d.countErr = &count, result.at, ""
			}
		default:
		}
	}
}

func (s *State) showMySQLFieldInspector(t *tab) {
	if t == nil || t.kind != tabMySQLData {
		return
	}
	t.dataInfo.fieldMode = true
	s.inspectorOn = true
}
