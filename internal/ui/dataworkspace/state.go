// Package dataworkspace owns bounded data pages and their editing lifecycle.
package dataworkspace

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"io"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/datasources"
)

type Session struct {
	ProfileID  int64
	Generation uint64
	Connected  bool
}
type Verifier func(context.Context, int64, string, string, *contract.DataEditResult) (*contract.DataVerification, error)

// Runtime captures the service methods available for the current UI request.
type Runtime = contract.DataRuntime

type Dependencies struct {
	OpenKVEvents      func(ui.Services, contract.KVKeyRead, bool) bool
	KVReadActive      func(*Document) bool
	ReopenDocument    func(ui.Services, *Document)
	CloseDocument     func(ui.Services, *Document)
	NamespaceLabel    func(contract.NamespaceRef) string
	EditFocus         *string
	EditTarget        func(theme.EditTarget)
	RecordActivity    contract.ActivityRecorder
	ImportTargets     func(int64, string) []string
	SessionFor        func(int64) Session
	RuntimeFor        func(int64) Runtime
	Session           func() Session
	Runtime           func() Runtime
	Inspector         InspectorDependencies
	Status            func(string)
	Activate          func(*Document)
	TableImported     func(ui.Services, TableImportEvent)
	OpenImportedTable func(ui.Services, TableImportEvent)
}

// Document has stable qualified identity. All mutable grid, draft and request
// state belongs to this module and is accessed only on the UI thread.
type Document struct {
	keyRead                          *kvKeyReadOperation
	keyReadError                     string
	selectorOpen                     bool
	selectorResponseOpen             bool
	selectorDraft, selectorError     string
	selectorHistory                  bool
	selectorHistorySorts             []contract.DataSortRule
	pagingRead                       bool
	readRetired, readStopping        bool
	runtimeLease                     contract.DataDocumentLease
	boundRuntime                     *Runtime
	mainFilterDisabled               bool
	disabledGroupRules               map[*contract.DataFilterRule]bool
	recordFormMode                   bool
	pageNumberDraft, pageNumberError string
	showSQLDetails                   bool

	importDialect string
	exportFormats []string
	generation    uint64
	object        contract.ObjectRef
	capabilities  contract.Capabilities
	filterModes   []string

	importOnly                  bool
	graphTransferOnly           bool
	hostAction                  string
	hostProfile                 string
	hostActionGeneration        uint64
	toolbar                     mysqlDataToolbarState
	fill                        mysqlDataFillState
	id                          uint64
	closed                      bool
	database, table             string
	objectKind                  string
	profileID                   int64
	offset                      int64
	limit                       int
	page                        *contract.DataPage
	panel                       *Panel
	readInfo                    mysqlDataReadInfo
	layout                      mysqlDataLayoutState
	grid                        ui.ListState
	columns                     []ui.TableColumn
	loading                     bool
	err                         string
	cancel                      context.CancelFunc
	results                     <-chan mysqlDataResult
	request                     uint64
	edit                        mysqlDataEditState
	mutation                    dataMutationState
	keyCopy                     *dataKeyCopyChoice
	deleteChoice                *dataDeleteChoice
	graphEndpoints              *dataGraphEndpointChoice
	nativeReceipt               nativeWriteReceiptState
	transaction                 dataTransactionState
	export                      mysqlDataExportState
	transfer                    mysqlDataTransferState
	selection                   ui.Selection[int]
	selectedRow                 int
	sort                        ui.SortOrder
	sortColumn                  string
	sortDescending              bool
	filterColumnDraft           string
	filterModeDraft             string
	filterValueDraft            string
	filterColumn                string
	filterMode                  string
	filterValue                 string
	rulesExpanded               bool
	ruleError                   string
	filterExtraDraft            []mysqlDataRuleDraft
	filterRules                 []contract.DataFilterRule
	filterCondition, groupDraft *contract.FilterExpression[contract.DataFilterRule]
	sortDraft                   []mysqlDataSortDraft
	sortRules                   []contract.DataSortRule
	hiddenColumns               map[string]bool
	showFieldTypes              bool
	useKeyset                   bool
	after                       *contract.DataCursor
	cursorHistory               map[int64]*contract.DataCursor
	tools                       mysqlDataToolsState
}
type State struct {
	savePath    func(string) (string, error)
	writeStream func(string, func(io.Writer) error) error
	guardDoc    *Document

	SourceOpen            func(context.Context, datasources.Config) (datasources.Source, error)
	SourceCapabilities    func(context.Context, datasources.Config) []datasources.Capability
	deps                  Dependencies
	documents             map[uint64]*Document
	inspector             *inspectorState
	dataLayoutPreferences mysqlDataLayoutPreferences
	Loader                mysqlDataLoader
	AdvancedLoader        mysqlDataAdvancedLoader
	Saver                 mysqlDataSaver
	RowReader             mysqlDataRowReader
	Verify                Verifier
	Exporter              mysqlDataExporter
	Analyzer              mysqlDataAnalyzer
	Transactions          DataTransactions
	NewTableImporter      mysqlDataNewTableImporter
	Importer              mysqlDataImporter
	StreamImporter        mysqlDataStreamImporter
}

func New(deps Dependencies) *State {
	return &State{deps: deps, inspector: newInspector(deps.Inspector), documents: make(map[uint64]*Document)}
}
func (s *State) session() Session {
	if s.deps.Session != nil {
		return s.deps.Session()
	}
	return Session{}
}
func (s *State) runtime() Runtime {
	if s.deps.Runtime != nil {
		return s.deps.Runtime()
	}
	return Runtime{}
}
func (s *State) pageLoader() mysqlDataLoader {
	if s.Loader != nil {
		return s.Loader
	}
	return s.runtime().Loader
}
func (s *State) advancedLoader() mysqlDataAdvancedLoader {
	if s.AdvancedLoader != nil {
		return s.AdvancedLoader
	}
	if s.Loader != nil {
		return nil
	}
	return s.runtime().AdvancedLoader
}
func (s *State) saver() mysqlDataSaver {
	if s.Saver != nil {
		return s.Saver
	}
	return s.runtime().Saver
}
func (s *State) rowReader() mysqlDataRowReader {
	if s.RowReader != nil {
		return s.RowReader
	}
	return s.runtime().RowReader
}
func (s *State) verifier() Verifier {
	if s.Verify != nil {
		return s.Verify
	}
	return s.runtime().Verify
}
func (s *State) setStatus(message string) {
	if s.deps.Status != nil {
		s.deps.Status(message)
	}
}
func (s *State) activate(d *Document) {
	if s.deps.Activate != nil {
		s.deps.Activate(d)
	}
}
func (s *State) Open(id uint64, profileID int64, database, table string, kind ...string) *Document {
	if profileID <= 0 || database == "" || table == "" {
		return nil
	}
	objectKind := "表"
	if len(kind) > 0 && kind[0] != "" {
		objectKind = kind[0]
	}
	d := &Document{importDialect: s.runtimeFor(&Document{profileID: profileID}).ImportDialect, objectKind: objectKind, id: id, profileID: profileID, database: database, table: table, limit: contract.DataDefaultPageSize, selectedRow: -1, filterColumnDraft: "选择字段", filterModeDraft: "包含", showFieldTypes: true, hiddenColumns: map[string]bool{}, useKeyset: true}
	if old := s.documents[id]; old != nil {
		s.Close(old)
	}
	s.documents[id] = d
	return d
}
func (d *Document) ProfileID() int64         { return d.profileID }
func (d *Document) Database() string         { return d.database }
func (d *Document) Table() string            { return d.table }
func (d *Document) Page() *contract.DataPage { return d.page }
func (d *Document) Loading() bool            { return d.loading }
func (d *Document) Error() string            { return d.err }
func (d *Document) Offset() int64            { return d.offset }
func (d *Document) HasDraft() bool           { return mysqlDataHasDraft(d) }
func (s *State) Consume(services ui.Services) {
	s.consumeDataTransactions(services)
	for _, d := range s.documents {
		if !d.closed && d.profileID == s.session().ProfileID {
			s.consumeMySQLDataExport(d)
		}
	}
	s.consumePages()
	s.consumeEdits(services)
	s.consumeDataTransfers(services)
	s.consumeDataAnalysis()
	s.inspector.Consume()
	s.consumeHostActions(services)
	s.consumeKVKeyReads(services)
}
func (s *State) inspectorPanel(t *Document) *Panel {
	if t == nil || t.closed || t.graphTransferOnly {
		return nil
	}
	if t.panel == nil {
		t.panel = s.inspector.Open(t.profileID, t.database, t.table, func() Selection {
			return Selection{Page: t.page, Column: t.grid.Cells.Cursor, Row: t.selectedRow, Hidden: t.hiddenColumns, Offset: t.offset, Loading: t.loading, Error: t.err}
		})
	}
	t.panel.runtime = t.boundRuntime
	t.panel.chunkError = func(err error) { s.dataTransactionReadFailed(t, err) }
	t.panel.chunkLoader = func() ChunkLoader {
		if dataTransactionUnavailable(t) || t.transfer.Busy || t.transfer.Uncertain {
			return nil
		}
		if token, read := t.transaction.token, t.transaction.runtime.Chunk; token != nil {
			if read == nil {
				return nil
			}
			return func(ctx context.Context, profile int64, database, table, column string, ref contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
				return read(ctx, token, profile, database, table, column, ref, offset, units)
			}
		}
		if t.boundRuntime != nil {
			return t.boundRuntime.Chunk
		}
		if s.inspector.ChunkLoader != nil {
			return s.inspector.ChunkLoader
		}
		if s.inspector.deps.ChunkLoader != nil {
			return s.inspector.deps.ChunkLoader()
		}
		return nil
	}
	return t.panel
}
func (s *State) ShowField(t *Document) {
	if t != nil && !t.closed {
		s.inspector.ShowField(s.inspectorPanel(t))
	}
}
func (s *State) cancelMySQLChunk(t *Document) {
	if t != nil {
		s.inspector.CancelChunk(t.panel)
	}
}
func (s *State) InspectorView(c *ui.Context, t *Document) { s.inspector.View(c, s.inspectorPanel(t)) }

func (s *State) CloseAll() {
	for _, d := range s.documents {
		s.Close(d)
	}
}

func (d *Document) Inspector() *Panel { return d.panel }

func (d *Document) Kind() string { return d.objectKind }

func (d *Document) GraphExchange() bool { return d.graphTransferOnly }
