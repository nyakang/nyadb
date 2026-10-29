package dataworkspace

import (
	"context"
	"errors"
	"time"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

type infoState struct {
	fieldMode          bool
	info               *mysqlsvc.MySQLTableInfo
	attempted, loading bool
	err                string
	request            uint64
	cancel             context.CancelFunc
	results            <-chan infoResult
	count              *int64
	countAt            time.Time
	countLoading       bool
	countErr           string
	countRequest       uint64
	countCancel        context.CancelFunc
	countResults       <-chan countResult
}

type infoResult struct {
	generation      uint64
	profileID       int64
	request         uint64
	database, table string
	info            *mysqlsvc.MySQLTableInfo
	err             error
}

type countResult struct {
	generation      uint64
	profileID       int64
	request         uint64
	database, table string
	count           int64
	at              time.Time
	err             error
}

// CancelInfo revokes both table-information readers for panel disposal. Metadata
// refresh uses LoadInfo so an independent exact-count request can keep running.
func (s *inspectorState) CancelInfo(t *Panel) {
	if t == nil {
		return
	}
	d := &t.info
	d.request++
	if d.cancel != nil {
		d.cancel()
	}
	d.cancel, d.results, d.loading = nil, nil, false
	s.CancelCount(t)
}

func (s *inspectorState) CancelCount(t *Panel) {
	if t == nil {
		return
	}
	d := &t.info
	d.countRequest++
	if d.countCancel != nil {
		d.countCancel()
	}
	d.countCancel, d.countResults, d.countLoading = nil, nil, false
}

func (s *inspectorState) LoadInfo(t *Panel, invalidate func(), refresh bool) {
	if !s.connected(t) {
		return
	}
	d := &t.info
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
	loader := s.InfoLoader
	if loader == nil && s.deps.InfoLoader != nil {
		loader = s.deps.InfoLoader()
	}
	if loader == nil {
		d.err = locales.Text("MySQL 会话不可用")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	d.cancel, d.loading = cancel, true
	request, generation, profile := d.request, s.session().Generation, t.profileID
	database, table := t.database, t.table
	out := make(chan infoResult, 1)
	d.results = out
	go func() {
		info, err := loader(ctx, profile, database, table)
		out <- infoResult{generation, profile, request, database, table, info, err}
		if invalidate != nil {
			invalidate()
		}
	}()
}

func (s *inspectorState) LoadCount(t *Panel, invalidate func()) {
	if !s.connected(t) || t.info.countLoading {
		return
	}
	d := &t.info
	s.CancelCount(t)
	d.countErr = ""
	loader := s.CountLoader
	if loader == nil && s.deps.CountLoader != nil {
		loader = s.deps.CountLoader()
	}
	if loader == nil {
		d.countErr = locales.Text("MySQL 会话不可用")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	d.countCancel, d.countLoading = cancel, true
	request, generation, profile := d.countRequest, s.session().Generation, t.profileID
	database, table := t.database, t.table
	out := make(chan countResult, 1)
	d.countResults = out
	go func() {
		count, err := loader(ctx, profile, database, table)
		out <- countResult{generation, profile, request, database, table, count, time.Now(), err}
		if invalidate != nil {
			invalidate()
		}
	}()
}

func (s *inspectorState) consumeInfo() {
	session := s.session()
	for p := range s.panels {
		if p.profileID != session.ProfileID {
			continue
		}
		consumePanelInfo(p, session)
		consumePanelCount(p, session)
	}
}
func consumePanelInfo(p *Panel, session Session) {
	d := &p.info
	select {
	case r := <-d.results:
		if r.request != d.request || r.profileID != p.profileID || r.database != p.database || r.table != p.table {
			return
		}
		if d.cancel != nil {
			d.cancel()
		}
		d.cancel, d.results, d.loading = nil, nil, false
		if r.generation != session.Generation || !session.Connected {
			d.err = locales.Text("MySQL 会话不可用")
			return
		}
		if r.err != nil {
			if !errors.Is(r.err, context.Canceled) {
				d.err = r.err.Error()
			}
			return
		}
		if r.info == nil || r.info.Database != p.database || r.info.Name != p.table {
			d.err = locales.Text("服务器未返回当前表的信息")
			return
		}
		d.info, d.err = r.info, ""
	default:
	}
}
func consumePanelCount(p *Panel, session Session) {
	d := &p.info
	select {
	case r := <-d.countResults:
		if r.request != d.countRequest || r.profileID != p.profileID || r.database != p.database || r.table != p.table {
			return
		}
		if d.countCancel != nil {
			d.countCancel()
		}
		d.countCancel, d.countResults, d.countLoading = nil, nil, false
		if r.generation != session.Generation || !session.Connected {
			d.countErr = locales.Text("MySQL 会话不可用")
			return
		}
		if r.err != nil {
			if !errors.Is(r.err, context.Canceled) {
				d.countErr = r.err.Error()
			}
			return
		}
		if r.count < 0 {
			d.countErr = locales.Text("服务器返回了无效行数")
			return
		}
		count := r.count
		d.count, d.countAt, d.countErr = &count, r.at, ""
	default:
	}
}
