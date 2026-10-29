// Package dataworkspace owns bounded data pages and their editing lifecycle.
package dataworkspace

import (
 "context"
 "github.com/egoist/mygo/ui"
 mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
 "github.com/nyakang/nyadb/internal/ui/datainspector"
)

type Session struct { ProfileID int64; Generation uint64; Connected bool }
type Verifier func(context.Context, int64, string, string, *mysqlsvc.MySQLDataEditResult) (*mysqlsvc.MySQLDataVerification, error)
// Runtime captures the service methods available for the current UI request.
type Runtime struct { Loader mysqlDataLoader; AdvancedLoader mysqlDataAdvancedLoader; Saver mysqlDataSaver; RowReader mysqlDataRowReader; Verify Verifier }
func MySQLRuntime(manager *mysqlsvc.Manager) Runtime {
 if manager == nil { return Runtime{} }
 return Runtime{AdvancedLoader: manager.TableDataPageWithOptions, Saver: manager.ApplyMySQLDataEdits, RowReader: manager.ReadMySQLDataRow, Verify: manager.VerifyMySQLDataEdits}
}
type Dependencies struct {
 Session func() Session
 Runtime func() Runtime
 Inspector datainspector.Dependencies
 Status func(string)
 Back func()
 Activate func(*Document)
}
// Document has stable qualified identity. All mutable grid, draft and request
// state belongs to this module and is accessed only on the UI thread.
type Document struct {
 id uint64
 closed bool
	database, table string
	profileID           int64
	offset              int64
	limit               int
	page                *mysqlsvc.MySQLDataPage
	panel               *datainspector.Panel
	readInfo            mysqlDataReadInfo
	layout              mysqlDataLayoutState
	grid               ui.ListState
	columns             []ui.TableColumn
	loading             bool
	err                 string
	cancel              context.CancelFunc
	results             <-chan mysqlDataResult
	request             uint64
	edit                mysqlDataEditState
	export              mysqlDataExportState
	selection           ui.Selection[int]
	selectedRow         int
	sort                ui.SortOrder
	sortColumn          string
	sortDescending      bool
	filterColumnDraft   string
	filterModeDraft     string
	filterValueDraft    string
	filterColumn        string
	filterMode          string
	filterValue         string
	rulesExpanded       bool
	ruleError           string
	filterExtraDraft    []mysqlDataRuleDraft
	filterRules         []mysqlsvc.MySQLDataFilterRule
	sortDraft           []mysqlDataSortDraft
	sortRules           []mysqlsvc.MySQLDataSortRule
	hiddenColumns       map[string]bool
	showFieldTypes      bool
	useKeyset           bool
	after               *mysqlsvc.MySQLDataCursor
	cursorHistory       map[int64]*mysqlsvc.MySQLDataCursor
}
type State struct {
 deps Dependencies
 documents []*Document
 inspector *datainspector.State
 dataLayoutPreferences mysqlDataLayoutPreferences
 Loader mysqlDataLoader
 AdvancedLoader mysqlDataAdvancedLoader
 Saver mysqlDataSaver
 RowReader mysqlDataRowReader
 Verify Verifier
}
func New(deps Dependencies) *State { return &State{deps:deps, inspector:datainspector.New(deps.Inspector)} }
func (s *State) session() Session { if s.deps.Session != nil { return s.deps.Session() }; return Session{} }
func (s *State) runtime() Runtime { if s.deps.Runtime != nil { return s.deps.Runtime() }; return Runtime{} }
func (s *State) pageLoader() mysqlDataLoader { if s.Loader != nil { return s.Loader }; return s.runtime().Loader }
func (s *State) advancedLoader() mysqlDataAdvancedLoader { if s.AdvancedLoader != nil { return s.AdvancedLoader }; if s.Loader != nil { return nil }; return s.runtime().AdvancedLoader }
func (s *State) saver() mysqlDataSaver { if s.Saver != nil { return s.Saver }; return s.runtime().Saver }
func (s *State) rowReader() mysqlDataRowReader { if s.RowReader != nil { return s.RowReader }; return s.runtime().RowReader }
func (s *State) verifier() Verifier { if s.Verify != nil { return s.Verify }; return s.runtime().Verify }
func (s *State) setStatus(message string) { if s.deps.Status != nil { s.deps.Status(message) } }
func (s *State) activate(d *Document) { if s.deps.Activate != nil { s.deps.Activate(d) } }
func (s *State) Open(id uint64, profileID int64, database, table string) *Document {
 if profileID <= 0 || database == "" || table == "" { return nil }
 d := &Document{id:id, profileID:profileID, database:database, table:table, limit:mysqlsvc.MySQLDataDefaultPageSize, selectedRow:-1, filterColumnDraft:"选择字段", filterModeDraft:"包含", showFieldTypes:true, hiddenColumns:map[string]bool{}, useKeyset:true}
 s.documents=append(s.documents,d)
 return d
}
func (d *Document) ProfileID() int64 { return d.profileID }
func (d *Document) Database() string { return d.database }
func (d *Document) Table() string { return d.table }
func (d *Document) Page() *mysqlsvc.MySQLDataPage { return d.page }
func (d *Document) Loading() bool { return d.loading }
func (d *Document) Error() string { return d.err }
func (d *Document) Offset() int64 { return d.offset }
func (d *Document) HasDraft() bool { return mysqlDataHasDraft(d) }
func (s *State) Consume(services ui.Services) { s.consumePages(); s.consumeEdits(services); s.inspector.Consume() }
func (s *State) Inspector() *datainspector.State { return s.inspector }
func (s *State) inspectorPanel(t *Document) *datainspector.Panel {
 if t == nil || t.closed { return nil }
 if t.panel == nil { t.panel = s.inspector.Open(t.profileID,t.database,t.table,func() datainspector.Selection {
  return datainspector.Selection{Page:t.page,Column:t.grid.Cells.Cursor,Row:t.selectedRow,Hidden:t.hiddenColumns,Offset:t.offset,Loading:t.loading,Error:t.err}
 }) }
 return t.panel
}
func (s *State) ShowField(t *Document) { if t != nil && !t.closed { s.inspector.ShowField(s.inspectorPanel(t)) } }
func (s *State) cancelMySQLChunk(t *Document) { if t != nil { s.inspector.CancelChunk(t.panel) } }
func (s *State) InspectorView(c *ui.Context,t *Document) { s.inspector.View(c,s.inspectorPanel(t)) }
