// Package sqlworkspace owns SQL documents, execution, results and physical
// session leases. Its host owns workbench tabs and profile activation.
package sqlworkspace

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/ui/theme"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

type Executor func(context.Context, int64, string, string) (contract.SQLResult, error)

// Lease is one document's physical SQL session. Replace revokes the old lease
// without executing SQL. Release is idempotent and affects only this lease.
type Lease interface {
	Preconnect(context.Context) error
	Execute(context.Context, string, string) (contract.SQLResult, error)
	Replace() (Lease, error)
	Release()
}
type Runtime interface {
	IsConnected(int64) bool
	Reserve(int64, uint64) (Lease, error)
}

type Dependencies struct {
	Namespace         func(int64, string) contract.NamespaceRef
	Profiles          func() []ProfileChoice
	ConnectProfile    func(ui.Services, int64)
	EditFocus         *string
	BuildExplain      func(int64, string) (string, error)
	PlanParser        func(int64) func(contract.SQLResult) (*contract.ExecutionPlan, error)
	CompletionSource  func(int64) CompletionSource
	ProfileName       func(int64) string
	ProfileColor      func(int64) string
	EditTarget        func(theme.EditTarget)
	AIProposal        func(int64, string, string)
	AIDialect         func(int64) string
	FavoriteAsset     func(queries.Named)
	CanFavorite       func() bool
	AssetsChanged     func(int64, string, bool)
	Completions       func(int64, string) []string
	CompletionCatalog func(int64, string) sqltext.CompletionCatalog
	StatementAtCaret  func(int64, string, int) string
	FormatSQL         func(int64, string) (string, error)
	ValidateExplain   func(int64, string) error
	Runtime           func() Runtime
	Connected         func(int64) bool
	Active            func(*Document) bool
	Status            func(string)
	Recovered         func(int64)
	Preview           func(*ui.Context)
}
type ProfileChoice struct {
	ID   int64
	Name string
}

// Document is owned by the UI thread. Workers capture a lease/executor and
// immutable request values; only Consume reads their private buffered channels.
type Document struct {
	snapshotOwner    *Document
	cachedExport     cachedExportDraft
	layoutMode       string
	showMessages     bool
	resultFieldRight bool
	fieldSplit       float32
	snapshots        []resultSnapshot
	snapshotIndex    int
	nextSnapshot     uint64

	queryError                             error
	diagnostic                             *contract.QueryDiagnostic
	executedSQL                            string
	executedVersion                        uint64
	plan                                   *contract.ExecutionPlan
	planError                              string
	planVisible, showOrdinary              bool
	ordinaryReading, explainReading        *resultReading
	metadata                               completionMetadata
	name, path, queryID, savedText, text   string
	revision, bufferVersion, namedRevision uint64
	resultSet, selectedRow                 int
	selection                              ui.Selection[int]

	Editor                   *editor.State
	saving                   bool
	id                       uint64
	profileID                int64
	database                 string
	catalog                  string
	namespaceSchema          string
	lease                    Lease
	recoveryNeeded           bool
	recoveryProfile          int64
	recoverySchema           string
	recoveryCatalog          string
	recoveryNamespaceSchema  string
	loading, cancelRequested bool
	err                      string
	result                   *contract.SQLResult
	grid                     ui.ListState
	cancel                   context.CancelFunc
	results                  <-chan executionResult
	request                  uint64
	prepareCancel            context.CancelFunc
	prepareResults           <-chan error
	preview, closed          bool
	focusEditor              bool
	resultViews              map[int]*resultView
	resultLayouts            map[string]ui.TableLayout
	resultPreferences        map[string]resultPreference
	hiddenResultColumns      map[string]bool
	resultFieldOpen          bool
	resultFieldMode          string
}

func (d *Document) ProfileID() int64            { return d.profileID }
func (d *Document) Database() string            { return d.database }
func (d *Document) QueryID() string             { return d.queryID }
func (d *Document) Loading() bool               { return d.loading }
func (d *Document) CancelRequested() bool       { return d.cancelRequested }
func (d *Document) Error() string               { return d.err }
func (d *Document) Result() *contract.SQLResult { return d.result }
func (d *Document) NeedsRecovery() bool         { return d.recoveryNeeded }
func (d *Document) RecoveryProfile() int64      { return d.recoveryProfile }
func (d *Document) RecoverySchema() string      { return d.recoverySchema }
func (d *Document) Namespace() contract.NamespaceRef {
	profile, _ := d.sourceScope()
	schema := d.namespaceSchema
	catalog := d.catalog
	if d.recoveryNeeded {
		catalog, schema = d.recoveryCatalog, d.recoveryNamespaceSchema
	}
	return contract.NamespaceRef{ProfileID: profile, Catalog: catalog, Schema: schema}
}

// sourceScope identifies the saved SQL document even before a new physical
// session is explicitly established. It must not be used to execute SQL.
func (d *Document) sourceScope() (int64, string) {
	if d.recoveryNeeded {
		return d.recoveryProfile, d.recoverySchema
	}
	return d.profileID, d.database
}

// State owns all SQL documents and their shared split position. Executor and
// Invalidate are injectable; background jobs capture them before starting.
type State struct {
	completionCache   map[string][]sqltext.CompletionObject
	completionObjects map[string]sqltext.CompletionObject
	ai                aiState
	SplitPercent      float32
	Executor          Executor
	Invalidate        func()
	documents         map[uint64]*Document
	demoRuns          int
	deps              Dependencies
	persistence       persistence
	history           historyState
}

func New(deps Dependencies) *State {
	return &State{SplitPercent: 48, documents: make(map[uint64]*Document), deps: deps}
}
func (s *State) Open(id uint64, text string, profileID int64, database string, preview bool) *Document {
	if previous := s.documents[id]; previous != nil {
		s.Close(previous)
	}
	buffer := editor.NewBuffer(text)
	buffer.SetLanguage(editor.LanguageNamed("SQL"))
	d := &Document{id: id, profileID: profileID, database: database, preview: preview, Editor: &editor.State{Buffer: buffer, WrapGuides: []int{100}}}
	d.namespaceSchema = database
	if s.deps.Namespace != nil {
		n := s.deps.Namespace(profileID, database)
		d.catalog, d.namespaceSchema = n.Catalog, n.Schema
	}
	if !preview {
		d.Editor.Bindings = sqlBindings()
	}
	d.Editor.OnCompletion = func(r editor.CompletionRequest) { s.completions(d, r) }
	d.savedText, d.text, d.bufferVersion = text, text, buffer.Version()
	s.documents[id] = d
	return d
}
func (s *State) runtime() Runtime {
	if s.deps.Runtime != nil {
		return s.deps.Runtime()
	}
	return nil
}
func (s *State) connected(id int64) bool { return s.deps.Connected != nil && s.deps.Connected(id) }
func (s *State) active(d *Document) bool {
	if d == nil || d.closed {
		return false
	}
	if d.snapshotOwner != nil {
		owner := d.snapshotOwner
		if owner.closed || owner.snapshotIndex <= 0 || owner.snapshotIndex > len(owner.snapshots) || owner.snapshots[owner.snapshotIndex-1].Document != d {
			return false
		}
		return s.deps.Active == nil || s.deps.Active(owner)
	}
	return s.deps.Active == nil || s.deps.Active(d)
}
func (s *State) report(message string) {
	if s.deps.Status != nil {
		s.deps.Status(message)
	}
}
func (s *State) DemoRuns() int { return s.demoRuns }

// Prepare reserves a new document's lease without blocking the UI. An invalid
// document requires explicit recovery and cannot silently obtain another lease.
func (s *State) Prepare(d *Document) {
	if d == nil || d.closed || d.preview || d.recoveryNeeded || d.profileID <= 0 || d.lease != nil || d.prepareResults != nil {
		return
	}
	runtime := s.runtime()
	if runtime == nil || !runtime.IsConnected(d.profileID) {
		return
	}
	lease, err := runtime.Reserve(d.profileID, d.id)
	if err != nil {
		d.err = "SQL 会话预留失败：" + err.Error()
		s.report(d.err)
		return
	}
	d.lease = lease
	s.preconnect(d)
}
func (s *State) Close(d *Document) {
	if d == nil || d.closed {
		return
	}
	if s.ai.target.doc == d {
		s.closeAI()
	}
	s.dispose(d)
	p := &s.persistence
	if p.guardDoc == d {
		p.guardDoc, p.guardNext = nil, nil
	}
	if p.nameDoc == d {
		p.nameDoc, p.nameOpen = nil, false
	}
	if p.explainDoc == d {
		p.explainDoc, p.explainOpen = nil, false
	}
	for _, job := range p.jobs {
		if job.doc == d {
			job.next = nil
		}
	}
	for len(d.snapshots) > 0 {
		d.removeSnapshot(0)
	}
	d.cachedExport = cachedExportDraft{}
	d.Editor, d.result = nil, nil
	d.resultViews, d.resultLayouts = nil, nil
	d.resultPreferences = nil
	d.hiddenResultColumns = nil
	d.grid = ui.ListState{}
	d.closed = true
	if s.documents[d.id] == d {
		delete(s.documents, d.id)
	}
}
func (s *State) CloseAll() {
	s.completionCache = nil
	s.completionObjects = nil
	s.closeAI()
	s.persistence.closed = true
	s.history = historyState{}
	if s.persistence.timer != nil {
		s.persistence.timer.Stop()
	}
	s.persistence.jobs = nil
	s.persistence.pending = nil
	s.persistence.restored = nil
	s.persistence.lastWorkspace = queries.Workspace{}
	s.persistence.flushNext, s.persistence.awaitClose, s.persistence.guardNext, s.persistence.exitNext = nil, nil, nil, nil
	s.persistence.nameDoc, s.persistence.guardDoc, s.persistence.explainDoc = nil, nil, nil
	s.persistence.assetDialog, s.persistence.assetMenu = nil, nil
	s.persistence.assetGroupMenu = nil
	s.persistence.assets, s.persistence.catalogs, s.persistence.assetBusy, s.persistence.catalogBusy = nil, nil, nil, nil
	for _, d := range s.documents {
		s.Close(d)
	}
}
