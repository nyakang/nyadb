package dataworkspace

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/locales"
	"math"
	"strconv"
	"strings"
)

func dataPageOffset(text string, limit int) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || n < 1 || limit < 1 || n-1 > (math.MaxInt64-int64(limit))/int64(limit) {
		return 0, fmtPageError()
	}
	return (n - 1) * int64(limit), nil
}
func fmtPageError() error { return &pageInputError{} }

type pageInputError struct{}

func (*pageInputError) Error() string { return locales.Text("页号必须是有效的正整数") }
func (s *State) jumpDataPage(services ui.Services, t *Document) bool {
	if t == nil || t.closed || t.readRetired || t.page == nil || t.loading || t.edit.Busy || t.edit.Uncertain != nil || t.transfer.Busy || t.transfer.Uncertain || !s.canUseSession(t) {
		return false
	}
	offset, err := dataPageOffset(t.pageNumberDraft, t.limit)
	if err != nil {
		t.pageNumberError = err.Error()
		return false
	}
	if !t.page.HasMore && offset > t.offset {
		t.pageNumberError = locales.Text("页号超出已知的数据范围")
		return false
	}
	if !documentCursorAllows(t, offset) {
		t.pageNumberError = locales.Text("原生游标仅支持已读取页或下一页")
		return false
	}
	t.pageNumberError = ""
	s.changeMySQLDataPage(services, t, offset)
	return true
}

// A document-bound keyset never invents a seek boundary for an unseen page.
// Relational offset loaders retain their existing arbitrary-page behavior.
func documentCursorAllows(t *Document, offset int64) bool {
	if t.runtimeLease == nil || t.page == nil || !t.page.KeysetAvailable || offset == 0 {
		return true
	}
	if boundary := t.cursorHistory[offset]; boundary != nil {
		return true
	}
	return offset == t.offset+int64(t.limit) && t.page.NextCursor != nil && t.page.NextCursor.Offset == offset
}
