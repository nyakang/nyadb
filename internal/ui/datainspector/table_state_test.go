package datainspector

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func tableInfoTestState(selection *Selection) (*State, *Panel, *Session) {
	session := &Session{ProfileID: 8, Connected: true}
	s := New(Dependencies{Session: func() Session { return *session }})
	p := s.Open(8, "test_schema", "entries", func() Selection { return *selection })
	return s, p, session
}
func tableInfoTester(s *State, p *Panel) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) { s.Consume(); theme.Apply(c); s.View(c, p) }, 320, 720)
}
func waitTableInfo(t *testing.T, tt *ui.Tester, waiting func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for waiting() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		tt.Frame()
	}
	tt.Frame()
	if waiting() {
		t.Fatal("table inspector operation did not finish")
	}
}

func TestMySQLTableInspectorDefaultsToTableAndExplicitlySelectsField(t *testing.T) {
	doc := sampleSelection()
	s, p, _ := tableInfoTestState(doc)
	var counts atomic.Int32
	s.InfoLoader = func(context.Context, int64, string, string) (*mysqlsvc.MySQLTableInfo, error) {
		rows := int64(21)
		return &mysqlsvc.MySQLTableInfo{Database: p.database, Name: p.table, Kind: "BASE TABLE", Engine: "InnoDB", EstimatedRows: &rows}, nil
	}
	s.CountLoader = func(context.Context, int64, string, string) (int64, error) { counts.Add(1); return 37, nil }
	tt := tableInfoTester(s, p)
	waitTableInfo(t, tt, func() bool { return p.info.loading })
	if !tt.HasText("估算行数") || !tt.HasText("InnoDB") || counts.Load() != 0 {
		t.Fatalf("initial panel: %v", tt.Texts())
	}
	doc.Row = 0
	doc.Column = "label"
	tt.Frame()
	if p.info.fieldMode || !tt.HasText("估算行数") {
		t.Fatal("cell selection replaced table mode")
	}
	if err := tt.Click("字段信息"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !p.info.fieldMode || !tt.HasText("varchar(80)") || !tt.HasText("sample") {
		t.Fatalf("field mode lost preview: %v", tt.Texts())
	}
	if err := tt.Click("表信息"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("获取整表行数"); err != nil {
		t.Fatal(err)
	}
	waitTableInfo(t, tt, func() bool { return p.info.countLoading })
	if counts.Load() != 1 || p.info.count == nil || *p.info.count != 37 || !tt.HasText("行数统计不应用当前筛选条件") {
		t.Fatalf("count semantics: %v", tt.Texts())
	}
}

func TestMySQLTableInfoFailureDoesNotChangeDataOrCancelCount(t *testing.T) {
	doc := sampleSelection()
	s, p, _ := tableInfoTestState(doc)
	page := doc.Page
	started, release := make(chan struct{}), make(chan struct{})
	s.InfoLoader = func(context.Context, int64, string, string) (*mysqlsvc.MySQLTableInfo, error) {
		return nil, errors.New("metadata denied")
	}
	s.CountLoader = func(ctx context.Context, _ int64, _, _ string) (int64, error) {
		close(started)
		select {
		case <-release:
			return 0, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	tt := tableInfoTester(s, p)
	waitTableInfo(t, tt, func() bool { return p.info.loading })
	if doc.Page != page || doc.Error != "" || !tt.HasText("metadata denied") {
		t.Fatal("metadata failure affected data page")
	}
	if err := tt.Click("获取整表行数"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("count did not start")
	}
	s.CancelChunk(p)
	if !p.info.countLoading {
		t.Fatal("data cancellation canceled independent count")
	}
	close(release)
	waitTableInfo(t, tt, func() bool { return p.info.countLoading })
	if p.info.count == nil || *p.info.count != 0 {
		t.Fatal("exact zero count was lost")
	}
}

func TestMySQLTableCountCancelPreservesLastResultAndRejectsLateResult(t *testing.T) {
	doc := sampleSelection()
	s, p, _ := tableInfoTestState(doc)
	p.info.attempted = true
	oldCount := int64(14)
	p.info.count = &oldCount
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.CountLoader = func(context.Context, int64, string, string) (int64, error) {
		close(started)
		<-release
		close(finished)
		return 99, nil
	}
	tt := tableInfoTester(s, p)
	if err := tt.Click("获取整表行数"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("count did not start")
	}
	if err := tt.Click("取消计数"); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-finished
	tt.Frame()
	if p.info.countLoading || *p.info.count != 14 || !tt.HasText("行数统计已取消") {
		t.Fatal("late count replaced canceled result")
	}
}

func TestMySQLTableInfoDiscardStaleScopeAndDisposeBothRequests(t *testing.T) {
	doc := sampleSelection()
	s, p, session := tableInfoTestState(doc)
	infoCanceled, countCanceled := false, false
	p.info.loading, p.info.countLoading = true, true
	p.info.cancel = func() { infoCanceled = true }
	p.info.countCancel = func() { countCanceled = true }
	out := make(chan infoResult, 1)
	out <- infoResult{generation: session.Generation + 1, profileID: 8, database: p.database, table: p.table,
		info: &mysqlsvc.MySQLTableInfo{Database: p.database, Name: p.table, Engine: "obsolete"}}
	p.info.results = out
	s.Consume()
	if p.info.info != nil {
		t.Fatal("stale session result was accepted")
	}
	s.CancelInfo(p)
	if !infoCanceled || !countCanceled || p.info.loading || p.info.countLoading || p.info.results != nil || p.info.countResults != nil {
		t.Fatal("disposal did not cancel both requests")
	}
}

func TestInspectorMetadataRefreshKeepsCountAndCloseCancelsAllReaders(t *testing.T) {
	selection := fieldSelection()
	s, p, _ := tableInfoTestState(selection)
	release := make(chan struct{})
	started := make(chan struct{}, 3)
	canceled := make(chan struct{}, 3)
	finished := make(chan struct{}, 3)
	wait := func(ctx context.Context) { started <- struct{}{}; <-ctx.Done(); canceled <- struct{}{}; <-release }
	s.InfoLoader = func(ctx context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableInfo, error) {
		wait(ctx)
		return &mysqlsvc.MySQLTableInfo{Database: db, Name: table, Engine: "obsolete"}, nil
	}
	s.CountLoader = func(ctx context.Context, _ int64, _, _ string) (int64, error) { wait(ctx); return 99, nil }
	s.ChunkLoader = func(ctx context.Context, _ int64, _, _, _ string, _ mysqlsvc.MySQLRowRef, _ int64, _ int) (*mysqlsvc.MySQLFieldChunk, error) {
		wait(ctx)
		return &mysqlsvc.MySQLFieldChunk{Text: "obsolete"}, nil
	}
	invalidate := func() { finished <- struct{}{} }
	s.LoadInfo(p, invalidate, false)
	s.LoadCount(p, invalidate)
	s.LoadChunk(p, invalidate)
	for range 3 {
		waitInspectorSignal(t, started)
	}
	s.InfoLoader = func(_ context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableInfo, error) {
		return &mysqlsvc.MySQLTableInfo{Database: db, Name: table, Engine: "InnoDB"}, nil
	}
	refreshed := make(chan struct{}, 1)
	s.LoadInfo(p, func() { refreshed <- struct{}{} }, true)
	waitInspectorSignal(t, canceled)
	waitInspectorSignal(t, refreshed)
	s.Consume()
	if p.Info() == nil || p.Info().Engine != "InnoDB" || !p.CountLoading() || !p.ChunkLoading() {
		t.Fatal("metadata refresh canceled an independent reader")
	}
	s.CloseAll()
	s.CloseAll()
	// A disposed panel cannot restart a reader, even with an explicit refresh.
	s.LoadInfo(p, invalidate, true)
	s.LoadCount(p, invalidate)
	s.LoadChunk(p, invalidate)
	for range 2 {
		waitInspectorSignal(t, canceled)
	}
	close(release)
	for range 3 {
		waitInspectorSignal(t, finished)
	}
	s.Consume()
	if p.InfoLoading() || p.CountLoading() || p.ChunkLoading() || p.info.results != nil || p.info.countResults != nil || p.chunkResults != nil || len(s.panels) != 0 {
		t.Fatal("terminal close kept pending readers")
	}
	if p.Info().Engine != "InnoDB" || p.Count() != nil || p.ChunkText() != "" {
		t.Fatal("late closed requests replaced retained metadata")
	}
}

func TestInspectorParkedResultsAndCountsAreScopedToProfileGeneration(t *testing.T) {
	selection := fieldSelection()
	s, a, session := tableInfoTestState(selection)
	b := s.Open(9, "test_schema", "entries", func() Selection { return *selection })
	done := make(chan struct{}, 2)
	var infoCalls, countCalls atomic.Int32
	s.InfoLoader = func(_ context.Context, id int64, db, table string) (*mysqlsvc.MySQLTableInfo, error) {
		infoCalls.Add(1)
		return &mysqlsvc.MySQLTableInfo{Database: db, Name: table, Engine: fmt.Sprint(id)}, nil
	}
	s.CountLoader = func(_ context.Context, id int64, _, _ string) (int64, error) { countCalls.Add(1); return id, nil }
	invalidate := func() { done <- struct{}{} }
	s.LoadInfo(a, invalidate, false)
	s.LoadCount(a, invalidate)
	waitInspectorSignal(t, done)
	waitInspectorSignal(t, done)
	session.ProfileID, session.Generation = 9, 2
	s.Consume()
	if !a.InfoLoading() || !a.CountLoading() || a.Info() != nil {
		t.Fatal("parked results were consumed under another profile")
	}
	s.LoadInfo(b, invalidate, false)
	s.LoadCount(b, invalidate)
	waitInspectorSignal(t, done)
	waitInspectorSignal(t, done)
	s.Consume()
	if b.Info() == nil || b.Info().Engine != "9" || b.Count() == nil || *b.Count() != 9 {
		t.Fatal("sibling received wrong metadata or count")
	}
	session.ProfileID, session.Generation = 8, 0
	s.Consume()
	if a.Info() == nil || a.Info().Engine != "8" || a.Count() == nil || *a.Count() != 8 {
		t.Fatal("returning profile lost its buffered metadata/count")
	}
	s.LoadCount(a, invalidate)
	waitInspectorSignal(t, done)
	session.Generation++
	s.Consume()
	if a.CountLoading() || a.CountError() == "" || *a.Count() != 8 {
		t.Fatal("stale generation remained busy or overwrote cached count")
	}
	s.Consume()
	if infoCalls.Load() != 2 || countCalls.Load() != 3 {
		t.Fatal("consumption automatically launched another metadata/count request")
	}
	s.Close(a)
	if b.Info() == nil || b.Count() == nil || len(s.panels) != 1 {
		t.Fatal("closing one profile erased its sibling panel")
	}
	s.CloseAll()
}

func TestInspectorStaleGenerationSettlesAllReadersAndKeepsCachedValues(t *testing.T) {
	selection := fieldSelection()
	s, p, session := tableInfoTestState(selection)
	done := make(chan struct{}, 3)
	invalidate := func() { done <- struct{}{} }
	s.InfoLoader = func(_ context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableInfo, error) {
		return &mysqlsvc.MySQLTableInfo{Database: db, Name: table, Engine: "InnoDB"}, nil
	}
	s.CountLoader = func(context.Context, int64, string, string) (int64, error) { return 7, nil }
	s.ChunkLoader = func(context.Context, int64, string, string, string, mysqlsvc.MySQLRowRef, int64, int) (*mysqlsvc.MySQLFieldChunk, error) {
		return &mysqlsvc.MySQLFieldChunk{Text: "cached fragment", NextOffset: 1024, TotalUnits: 8192, HasMore: true}, nil
	}
	s.LoadInfo(p, invalidate, false)
	s.LoadCount(p, invalidate)
	s.LoadChunk(p, invalidate)
	for range 3 {
		waitInspectorSignal(t, done)
	}
	s.Consume()
	info, countAt := p.Info(), p.CountAt()
	if info == nil || p.Count() == nil || p.ChunkNext() != 1024 {
		t.Fatal("initial cache was not populated")
	}
	s.InfoLoader = func(_ context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableInfo, error) {
		return &mysqlsvc.MySQLTableInfo{Database: db, Name: table, Engine: "obsolete"}, nil
	}
	s.CountLoader = func(context.Context, int64, string, string) (int64, error) { return 99, nil }
	s.ChunkLoader = func(context.Context, int64, string, string, string, mysqlsvc.MySQLRowRef, int64, int) (*mysqlsvc.MySQLFieldChunk, error) {
		return &mysqlsvc.MySQLFieldChunk{Text: "obsolete", NextOffset: 2048, TotalUnits: 8192}, nil
	}
	s.LoadInfo(p, invalidate, true)
	s.LoadCount(p, invalidate)
	s.LoadChunk(p, invalidate)
	for range 3 {
		waitInspectorSignal(t, done)
	}
	session.Generation++
	s.Consume()
	if p.InfoLoading() || p.CountLoading() || p.ChunkLoading() || p.InfoError() == "" || p.CountError() == "" || p.ChunkError() == "" {
		t.Fatal("one stale result postponed another reader or left it busy")
	}
	if p.Info() != info || *p.Count() != 7 || p.CountAt() != countAt || p.ChunkText() != "cached fragment" || p.ChunkNext() != 1024 {
		t.Fatal("stale generation replaced cached metadata/count/fragment")
	}
	s.CloseAll()
}

func TestInspectorInvalidResponsesPreserveCacheAndRequireExplicitRetry(t *testing.T) {
	for _, kind := range []string{"missing-info", "other-table"} {
		t.Run(kind, func(t *testing.T) {
			selection := sampleSelection()
			s, p, _ := tableInfoTestState(selection)
			cached := &mysqlsvc.MySQLTableInfo{Database: p.database, Name: p.table, Engine: "InnoDB"}
			count := int64(7)
			p.info.info, p.info.count = cached, &count
			done := make(chan struct{}, 2)
			invalidate := func() { done <- struct{}{} }
			var calls atomic.Int32
			s.InfoLoader = func(_ context.Context, _ int64, db, _ string) (*mysqlsvc.MySQLTableInfo, error) {
				calls.Add(1)
				if kind == "missing-info" {
					return nil, nil
				}
				return &mysqlsvc.MySQLTableInfo{Database: db, Name: "other_table"}, nil
			}
			s.CountLoader = func(context.Context, int64, string, string) (int64, error) { return -1, nil }
			s.LoadInfo(p, invalidate, true)
			s.LoadCount(p, invalidate)
			waitInspectorSignal(t, done)
			waitInspectorSignal(t, done)
			s.Consume()
			if p.Info() != cached || *p.Count() != 7 || p.InfoError() == "" || p.CountError() == "" || p.InfoLoading() || p.CountLoading() {
				t.Fatal("invalid response replaced cache or remained busy")
			}
			s.LoadInfo(p, invalidate, false)
			if calls.Load() != 1 || p.InfoLoading() {
				t.Fatal("failed metadata automatically retried")
			}
			s.InfoLoader = func(_ context.Context, _ int64, db, table string) (*mysqlsvc.MySQLTableInfo, error) {
				return &mysqlsvc.MySQLTableInfo{Database: db, Name: table, Engine: "refreshed"}, nil
			}
			s.CountLoader = func(context.Context, int64, string, string) (int64, error) { return 0, nil }
			s.LoadInfo(p, invalidate, true)
			s.LoadCount(p, invalidate)
			waitInspectorSignal(t, done)
			waitInspectorSignal(t, done)
			s.Consume()
			if p.Info().Engine != "refreshed" || *p.Count() != 0 || p.InfoError() != "" || p.CountError() != "" {
				t.Fatal("explicit retry did not recover metadata and exact zero count")
			}
			s.CloseAll()
		})
	}
}
