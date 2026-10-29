package shell

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type mysqlChunkResult struct {
	generation, request uint64
	profileID           int64
	row                 int
	field               string
	chunk               *mysqlsvc.MySQLFieldChunk
	err                 error
}

func mysqlDataPageMethod(page *mysqlsvc.MySQLDataPage) string {
	if page.KeysetAvailable {
		return "Keyset"
	}
	return "OFFSET"
}
func (s *State) cancelMySQLChunk(t *tab) {
	t.dataChunkRequest++
	if t.dataChunkCancel != nil {
		t.dataChunkCancel()
	}
	t.dataChunkCancel = nil
	t.dataChunkResults = nil
	t.dataChunkLoading = false
	t.dataChunkField = ""
	t.dataChunkRow = -1
	t.dataChunkNext = 0
	t.dataChunkText = ""
	t.dataChunkTotal = 0
	t.dataChunkDone = false
	t.dataChunkErr = ""
}
func (s *State) loadMySQLChunk(services ui.Services, t *tab) {
	if t == nil || t.dataChunkLoading || t.dataPage == nil ||
		t.dataProfileID != s.mysqlSessionProfileID || s.mysqlSessionStatus != "已连接" ||
		s.mysqlSessionManager == nil {
		return
	}
	field, _, ok := mysqlSelectedDataCell(t)
	if !ok || t.dataSelectedRow >= len(t.dataPage.RowRefs) ||
		t.dataPage.RowRefs[t.dataSelectedRow] == nil {
		return
	}
	if t.dataChunkField != field || t.dataChunkRow != t.dataSelectedRow {
		s.cancelMySQLChunk(t)
		t.dataChunkField = field
		t.dataChunkRow = t.dataSelectedRow
	}
	if t.dataChunkDone {
		return
	}
	ref := *t.dataPage.RowRefs[t.dataSelectedRow]
	offset := t.dataChunkNext
	gen, profile := s.mysqlSessionGeneration, s.mysqlSessionProfileID
	row := t.dataSelectedRow
	request := t.dataChunkRequest + 1
	t.dataChunkRequest = request
	ctx, cancel := context.WithCancel(context.Background())
	t.dataChunkCancel = cancel
	t.dataChunkLoading = true
	t.dataChunkErr = ""
	out := make(chan mysqlChunkResult, 1)
	t.dataChunkResults = out
	database, table := t.dataDatabase, t.dataTable
	manager := s.mysqlSessionManager // capture before goroutine; session may disconnect.
	go func() {
		chunk, err := manager.ReadMySQLFieldChunk(ctx, profile, database, table, field, ref, offset, 1024)
		out <- mysqlChunkResult{generation: gen, profileID: profile, request: request, row: row, field: field, chunk: chunk, err: err}
		services.Invalidate()
	}()
}
func (s *State) consumeMySQLChunks() {
	for _, t := range s.tabs {
		if t.kind != tabMySQLData || t.dataChunkResults == nil || t.dataProfileID != s.mysqlSessionProfileID {
			continue
		}
		select {
		case result := <-t.dataChunkResults:
			if result.generation != s.mysqlSessionGeneration ||
				result.profileID != s.mysqlSessionProfileID ||
				result.profileID != t.dataProfileID ||
				result.request != t.dataChunkRequest ||
				result.row != t.dataSelectedRow ||
				result.field != t.dataState.Cells.Cursor {
				continue
			}
			if t.dataChunkCancel != nil {
				t.dataChunkCancel()
			}
			t.dataChunkCancel = nil
			t.dataChunkResults = nil
			t.dataChunkLoading = false
			if result.err != nil {
				if !errors.Is(result.err, context.Canceled) {
					t.dataChunkErr = result.err.Error()
				}
				continue
			}
			if result.chunk == nil {
				t.dataChunkErr = "没有返回字段数据"
				continue
			}
			t.dataChunkText = result.chunk.Text
			t.dataChunkNext = result.chunk.NextOffset
			t.dataChunkTotal = result.chunk.TotalUnits
			t.dataChunkDone = !result.chunk.HasMore
			t.dataChunkErr = ""
		default:
		}
	}
}
