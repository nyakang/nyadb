package dataworkspace

import (
	"context"
	"errors"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

type chunkResult struct {
	generation, request uint64
	profileID           int64
	row                 int
	field               string
	page                *mysqlsvc.MySQLDataPage
	chunk               *mysqlsvc.MySQLFieldChunk
	err                 error
}

func (s *inspectorState) CancelChunk(p *Panel) {
	if p == nil {
		return
	}
	p.chunkRequest++
	if p.chunkCancel != nil {
		p.chunkCancel()
	}
	p.chunkCancel, p.chunkResults, p.chunkLoading = nil, nil, false
	p.chunkField, p.chunkRow, p.chunkPage = "", -1, nil
	p.chunkNext, p.chunkText, p.chunkTotal, p.chunkDone, p.chunkErr = 0, "", 0, false, ""
}

// LoadChunk retrieves one explicit segment of at most 1024 characters/bytes.
// It copies the row identity before starting a worker; selection changes cannot
// redirect an existing request to another cell or page.
func (s *inspectorState) LoadChunk(p *Panel, invalidate func()) {
	if !s.connected(p) || p.chunkLoading {
		return
	}
	selection := p.Selection()
	field, cell, selected := selection.Cell()
	if !selected || cell.Null || selection.Page == nil || selection.Row < 0 || selection.Row >= len(selection.Page.RowRefs) || selection.Page.RowRefs[selection.Row] == nil || selection.Hidden[field] {
		return
	}
	loader := s.ChunkLoader
	if loader == nil && s.deps.ChunkLoader != nil {
		loader = s.deps.ChunkLoader()
	}
	if p.chunkLoader != nil {
		loader = p.chunkLoader()
	}
	if loader == nil {
		return
	}
	if p.chunkField != field || p.chunkRow != selection.Row || p.chunkPage != selection.Page {
		s.CancelChunk(p)
		p.chunkField, p.chunkRow, p.chunkPage = field, selection.Row, selection.Page
	}
	if p.chunkDone {
		return
	}
	ref := *selection.Page.RowRefs[selection.Row]
	ref.Columns = append([]string(nil), ref.Columns...)
	values := make([][]byte, len(ref.Values))
	for i, value := range ref.Values {
		values[i] = append([]byte(nil), value...)
	}
	ref.Values = values
	p.chunkRequest++
	ctx, cancel := context.WithCancel(context.Background())
	p.chunkCancel, p.chunkLoading, p.chunkErr = cancel, true, ""
	out := make(chan chunkResult, 1)
	p.chunkResults = out
	w := s.session()
	request, profile, generation, row, page, offset := p.chunkRequest, p.profileID, w.Generation, selection.Row, selection.Page, p.chunkNext
	database, table := p.database, p.table
	go func() {
		chunk, err := loader(ctx, profile, database, table, field, ref, offset, 1024)
		out <- chunkResult{generation: generation, profileID: profile, request: request, row: row, field: field, page: page, chunk: chunk, err: err}
		if invalidate != nil {
			invalidate()
		}
	}()
}

// Consume keeps parked profile results buffered and settles only requests from
// the active profile. It never starts metadata/count/chunk reads on its own.
func (s *inspectorState) Consume() {
	s.consumeInfo()
	s.consumeChunks()
}
func (s *inspectorState) consumeChunks() {
	w := s.session()
	for p := range s.panels {
		if p.profileID != w.ProfileID {
			continue
		}
		selection := p.Selection()
		field, _, selected := selection.Cell()
		if p.chunkField != "" && (!selected || field != p.chunkField || selection.Row != p.chunkRow || selection.Page != p.chunkPage || selection.Hidden[field]) {
			s.CancelChunk(p)
		}
		if p.chunkResults == nil {
			continue
		}
		select {
		case r := <-p.chunkResults:
			if r.request != p.chunkRequest || r.profileID != p.profileID || r.row != selection.Row || r.field != field || r.page != selection.Page {
				continue
			}
			if p.chunkCancel != nil {
				p.chunkCancel()
			}
			p.chunkCancel, p.chunkResults, p.chunkLoading = nil, nil, false
			if r.generation != w.Generation || !w.Connected {
				p.chunkErr = locales.Text("MySQL 会话不可用")
				continue
			}
			if r.err != nil {
				if p.chunkError != nil {
					p.chunkError(r.err)
				}
				if errors.Is(r.err, mysqlsvc.ErrMySQLDataTransactionLost) || !errors.Is(r.err, context.Canceled) {
					p.chunkErr = r.err.Error()
				}
				continue
			}
			if r.chunk == nil {
				p.chunkErr = "没有返回字段数据"
				continue
			}
			p.chunkText, p.chunkNext, p.chunkTotal, p.chunkDone, p.chunkErr = r.chunk.Text, r.chunk.NextOffset, r.chunk.TotalUnits, !r.chunk.HasMore, ""
		default:
		}
	}
}
