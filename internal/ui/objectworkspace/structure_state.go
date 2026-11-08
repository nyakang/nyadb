package objectworkspace

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

type Loader func(context.Context, int64, string, string) (*contract.TableStructure, error)

// Session is a UI-thread snapshot. A parked profile's results remain buffered
// until the host activates it; its generation must still match on consumption.
type Session struct {
	ProfileID  int64
	Generation uint64
	Connected  bool
}

type Dependencies struct {
	CollectionManagementFor func(int64) (contract.CollectionManagementProvider, error)
	BeforeCollections       func(ui.Services, uint64, int64, uint64, contract.CollectionManagementPlan, func(ui.Services)) bool
	CollectionsChanged      func(ui.Services, CollectionManagementEvent)
	KeysDocumentsFor        func(int64) (contract.KeysDocumentsProvider, error)
	DocumentActive          func(uint64) bool
	SettingsDocumentsFor    func(int64) (contract.SettingsDocumentsProvider, error)
	SettingsChanged         func(ui.Services, SettingsEvent)
	TaskDocumentsFor        func(int64) (contract.TaskDocumentsProvider, error)
	BeforeNamespace         func(ui.Services, uint64, contract.TaskMutationPlan, func(ui.Services)) bool
	NamespaceChanged        func(ui.Services, NamespaceEvent)
	MetadataFor             func(int64) (contract.MetadataProvider, error)
	NamespaceLabel          func(contract.NamespaceRef) string
	ProfileName             func(int64) string
	EditFocus               *string
	EditTarget              func(theme.EditTarget)
	Engine                  func(int64) string
	FieldTypes              func(int64) []string
	OpenDesign              func(ui.Services, int64, string, string)
	BackupsFor              func(int64) contract.BackupOperations
	BeforeRestore           func(ui.Services, contract.BackupPlan, func(ui.Services)) bool
	RestoreGuardPending     func(contract.BackupPlan) bool
	BackupCompleted         func(ui.Services, BackupEvent)
	MaintenanceFor          func(int64) contract.MaintenanceOperations
	Maintained              func(ui.Services, MaintenanceEvent)
	Availability            func(int64, string, contract.SchemaDesignTarget) contract.Availability
	Namespace               func(int64, string) contract.NamespaceRef
	SchemaDesignFor         func(int64) contract.SchemaDesignOperations
	SchemaTarget            func(int64, string, string) contract.SchemaDesignTarget
	SchemaDesigned          func(ui.Services, SchemaDesignEvent)
	SchemaDropped           func(ui.Services, SchemaDropEvent)
	SchemaCopied            func(ui.Services, SchemaCopyEvent)
	SchemaRenamed           func(ui.Services, SchemaRenameEvent)
	BeforeDrop              func(ui.Services, int64, string, string, func(ui.Services)) bool
	BeforeCopy              func(ui.Services, int64, string, string, func(ui.Services)) bool
	BeforeRename            func(ui.Services, int64, string, string, func(ui.Services)) bool
	TableGuardPending       func(int64, string, string) bool
	ViewDesignFor           func(int64) contract.ViewDesignOperations
	ViewTarget              func(int64, string, string) contract.SchemaDesignTarget
	ViewChanged             func(ui.Services, ViewEvent)
	RenameSchema            func(ui.Services, int64, string, string, string)
	Favorite                func(ui.Services, int64, string, string, string)
	CanFavorite             func() bool
	SearchRuntime           func(int64) (ObjectSearchRuntime, error)
	OpenSearchObject        func(ui.Services, int64, uint64, ObjectSearchEntry)
	RecordActivity          contract.ActivityRecorder
	TableProfiles           func(int64, string, string) ([]TableProfile, error)
	OpenTableProfile        func(ui.Services, int64, string, string, string)
	AdministrationFor       func(int64) mysqlsvc.Administration
	LoadObjectGroups        func(int64, string) map[string]string
	SaveObjectGroups        func(int64, string, map[string]string)

	Capabilities  func(int64) contract.Capabilities
	BatchExecutor func(context.Context, int64, uint64, []mysqlsvc.ObjectBatchItem) []mysqlsvc.ObjectBatchResult

	AccountLoader   func(context.Context, int64) ([]mysqlsvc.MySQLAccount, error)
	AccountGrants   func(context.Context, int64, mysqlsvc.MySQLAccount) ([]string, error)
	AccountExecutor func(context.Context, int64, uint64, []mysqlsvc.AccountChange) mysqlsvc.DDLOutcome

	ObjectLoader      func(context.Context, int64, mysqlsvc.ObjectIdentity) (mysqlsvc.ObjectDefinition, error)
	ObjectExecutor    func(context.Context, int64, mysqlsvc.ObjectPlan) mysqlsvc.ObjectOutcome
	ObjectChanged     func(ui.Services, ObjectEvent)
	RuntimeGeneration func(int64) uint64
	Backup            func(ui.Services, int64, string, string, string)
	Maintenance       func(context.Context, int64, string, string, string) ([]mysqlsvc.MaintenanceMessage, error)

	DataAction       func(ui.Services, int64, string, string, string)
	ActivateDesign   func(*Document)
	DesignLoader     func() DesignLoader
	DesignExecutor   func() DesignExecutor
	DesignGeneration func(int64) uint64
	BeforeDDL        func(ui.Services, int64, string, string, func(ui.Services)) bool
	Designed         func(ui.Services, DesignEvent)
	NewTable         func(ui.Services, int64, string, string)
	Browser          func() BrowserSnapshot
	SessionFor       func(int64) Session
	Runner           func() TableRunner
	OpenData         func(ui.Services, int64, string, string)
	OpenStructure    func(ui.Services, int64, string, string)
	OpenProgram      func(int64, string, string, string)
	Selected         func(int64, string, string, string)
	FilterChanged    func(int64, string, string)
	LoadObjects      func(ui.Services, string, bool)
	CancelObjects    func()
	RefreshDatabases func(ui.Services)
	Changed          func(ui.Services, TableEvent)
	Status           func(string)
	Session          func() Session
	Loader           func() Loader
	Back             func()
}

// Document owns its immutable object identity and mutable presentation state.
// Only the UI thread changes it; workers publish to private buffered channels.
type Document struct {
	collections                   *collectionManagementDocument
	keys                          *keysDocument
	settings                      *settingsDocument
	tasks                         *taskDocument
	formFocusHandle               ui.Handle
	formFocusTarget               *string
	formFocusPending              bool
	inline                        map[string]*designCell
	object                        contract.ObjectRef
	id                            uint64
	profileID                     int64
	database, table               string
	objectKind                    string
	data                          *contract.TableStructure
	nativeDefinitionChoice        string
	page, fieldRow                int
	columns, indexes, foreignKeys ui.ListState
	loading                       bool
	err                           string
	cancel                        context.CancelFunc
	results                       <-chan loadResult
	request                       uint64
	closed                        bool
	program                       *programDocument
	design                        *designDocument
	schemaDesign                  *schemaDesignDocument
}

func (d *Document) ProfileID() int64 { return d.profileID }
func (d *Document) Database() string { return d.database }
func (d *Document) Table() string    { return d.table }

// Data returns metadata for read-only host presentation.
func (d *Document) Data() *contract.TableStructure { return d.data }
func (d *Document) Loading() bool                  { return d.loading }
func (d *Document) Error() string                  { return d.err }
func (d *Document) SelectedField() (contract.StructureColumn, bool) {
	if d == nil || d.closed {
		return contract.StructureColumn{}, false
	}
	if x := d.schemaDesign; x != nil {
		if x.page != 0 || x.fieldRow < 0 || x.fieldRow >= len(x.draft.Clauses) {
			return contract.StructureColumn{}, false
		}
		if schemaNativeFieldGrammar(x.original.Schema.Engine) {
			f := x.pgField
			out := contract.StructureColumn{Name: x.name, Type: f.Type, Nullable: !f.NotNull, Generated: f.Generation != ""}
			clause := x.draft.Clauses[x.fieldRow]
			if clause.Postgres != nil {
				out.Comment = clause.Postgres.Comment
			}
			if clause.OpenGauss != nil && clause.OpenGauss.Comment != nil {
				out.Comment = *clause.OpenGauss.Comment
			}
			if f.HasDefault {
				out.Default = &f.Default
			}
			d.inlineFieldPreview(x.draft.Clauses[x.fieldRow].ID, &out)
			return out, true
		}
		f := x.field
		out := contract.StructureColumn{Name: f.Name, Type: f.Type, Nullable: !f.NotNull}
		if f.HasDefault {
			out.Default = &f.Default
		}
		if f.Primary {
			out.Key = "PRI"
		}
		d.inlineFieldPreview(x.draft.Clauses[x.fieldRow].ID, &out)
		return out, true
	}
	if x := d.design; x != nil {
		if x.page != 0 || x.formRow < 0 {
			return contract.StructureColumn{}, false
		}
		f := x.form
		out := contract.StructureColumn{Name: f.Name, Type: f.Type, Nullable: f.Nullable, Comment: f.Comment, Generated: f.Generation != ""}
		if f.DefaultKind != mysqlddl.NoDefault {
			out.Default = &f.Default
		}
		d.inlineFieldPreview(x.draft.Table.Clauses[x.formRow].ID, &out)
		return out, true
	}
	if d.data == nil || d.fieldRow < 0 || d.fieldRow >= len(d.data.Columns) {
		return contract.StructureColumn{}, false
	}
	return d.data.Columns[d.fieldRow], true
}

type loadResult struct {
	generation uint64
	profileID  int64
	request    uint64
	data       *contract.TableStructure
	err        error
}

type State struct {
	schemaCopy                              *schemaCopyOperation
	schemaRename                            *schemaRenameOperation
	schemaDrop                              *schemaDropOperation
	dictionary                              *dictionaryState
	dictionaryMenu                          *dictionaryScope
	dictionarySave                          func(string) (string, error)
	referenceRequest                        *objectReferenceRequest
	search                                  *objectSearchState
	profileMenu                             *tableProfileMenu
	contextEntryMenu                        *contextEntryScope
	moreScope                               *browserActionScope
	groupDialog                             *objectGroupDialog
	tableGeneration, tableRuntimeGeneration uint64

	backup         *backupOperation
	backupRuntime  func() BackupRuntime
	backupFiles    BackupFiles
	portableBackup *portableBackupPanel

	batch *objectBatch

	accounts         *accountPanel
	maintenance      *maintenanceOperation
	maintenancePanel *maintenancePanel

	objectOp, parkedObjectOp *objectOperation
	viewOp, parkedViewOp     *viewOperation

	Loader                                                                Loader
	deps                                                                  Dependencies
	documents                                                             map[uint64]*Document
	browsers                                                              map[int64]*browserState
	Runner                                                                TableRunner
	tableDialog                                                           bool
	tableAction                                                           mysqlsvc.MySQLTableAction
	tableProfileID                                                        int64
	tableDatabase, tableName, tableNewName, tableConfirmation, tableError string
	tableCancel                                                           context.CancelFunc
	tablePending                                                          bool
	tableResults                                                          <-chan mysqlTableChangeResult
}

func New(deps Dependencies) *State {
	return &State{deps: deps, documents: make(map[uint64]*Document), browsers: make(map[int64]*browserState)}
}
func (s *State) session() Session {
	if s.deps.Session != nil {
		return s.deps.Session()
	}
	return Session{}
}

// Open registers a document without loading metadata or changing the host tab.
func (s *State) OpenStructure(id uint64, profileID int64, database, table string, kind ...string) *Document {
	if profileID <= 0 || database == "" || table == "" {
		return nil
	}
	if previous := s.documents[id]; previous != nil {
		s.Close(previous)
	}
	objectKind := "表"
	if len(kind) > 0 && kind[0] != "" {
		objectKind = kind[0]
	}
	d := &Document{objectKind: objectKind, id: id, profileID: profileID, database: database, table: table}
	s.documents[id] = d
	return d
}

// Load uses cached metadata unless refresh is explicit. Refresh revokes the
// prior worker before starting another. It never executes user-authored SQL or
// modifies a table.
func (s *State) Load(d *Document, invalidate func(), refresh bool) {
	if d.CollectionManagementMode() {
		if refresh {
			s.ReadCollectionManagement(d, invalidate)
		}
		return
	}
	if d.KeysMode() {
		s.LoadKeys(d, invalidate, refresh)
		return
	}
	if d.SettingsMode() {
		s.LoadSettings(d, invalidate, refresh)
		return
	}
	if d != nil && d.tasks != nil {
		s.LoadTasks(d, invalidate, refresh)
		return
	}
	session := s.session()
	if d == nil || d.closed || d.program != nil || d.profileID <= 0 || d.profileID != session.ProfileID || !session.Connected || d.database == "" || d.table == "" {
		return
	}
	if !refresh && (d.loading || d.data != nil || d.err != "") {
		return
	}
	s.cancel(d)
	d.err = ""
	loader := s.Loader
	if loader == nil && s.deps.Loader != nil {
		loader = s.deps.Loader()
	}
	if s.Loader == nil && d.object.ProfileID != 0 && s.deps.MetadataFor != nil {
		provider, err := s.deps.MetadataFor(d.profileID)
		if err != nil || provider == nil {
			if err == nil {
				err = contract.ErrUnsupported
			}
			d.err = err.Error()
			return
		}
		object := d.object
		loader = func(ctx context.Context, _ int64, _, _ string) (*contract.TableStructure, error) {
			return provider.TableMetadata(ctx, object)
		}
	}
	if loader == nil {
		d.err = "MySQL 会话不可用"
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.loading = cancel, true
	request, profile, generation := d.request, d.profileID, session.Generation
	database, table := d.database, d.table
	out := make(chan loadResult, 1)
	d.results = out
	go func() {
		data, err := loader(ctx, profile, database, table)
		out <- loadResult{generation: generation, profileID: profile, request: request, data: data, err: err}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) cancel(d *Document) {
	d.request++
	if d.cancel != nil {
		d.cancel()
	}
	d.cancel, d.results, d.loading = nil, nil, false
}

// Cancel preserves the existing read-only cache while revoking pending work.
func (s *State) Cancel(d *Document) {
	if d.CollectionManagementMode() {
		s.CancelCollectionManagement(d)
		return
	}
	if d.KeysMode() {
		s.CancelKeys(d)
		return
	}
	if d != nil && d.tasks != nil {
		s.CancelTasks(d)
		return
	}
	if d == nil || d.closed {
		return
	}
	s.cancel(d)
	d.err = "加载已取消"
}
func (s *State) Close(d *Document) {
	if d == nil || d.closed {
		return
	}
	s.closeCollections(d)
	s.closeSettings(d)
	s.closeKeys(d)
	s.closeTasks(d)
	s.cancel(d)
	s.closeDesign(d)
	d.formFocusHandle.CancelFocus()
	d.formFocusHandle = ui.Handle{}
	d.formFocusTarget, d.formFocusPending = nil, false
	d.closed = true
	d.inline = nil
	if d.program != nil {
		d.program.Close()
	}
	d.data = nil
	d.nativeDefinitionChoice = ""
	d.columns, d.indexes, d.foreignKeys = ui.ListState{}, ui.ListState{}, ui.ListState{}
	if s.documents[d.id] == d {
		delete(s.documents, d.id)
	}
}
func (s *State) CloseAll() {
	s.closeMaintenance()
	s.closePortableBackup()
	s.closeView()
	s.parkedViewOp = nil
	s.closeSchemaCopy()
	s.closeSchemaDrop()
	s.closeSchemaRename()
	s.closeDictionary()
	s.dictionaryMenu = nil
	s.closeObjectReference()
	s.closeObjectSearch()
	s.moreScope, s.groupDialog = nil, nil
	s.profileMenu = nil
	s.contextEntryMenu = nil
	s.closeBackup()
	if s.batch != nil && s.batch.cancel != nil {
		s.batch.cancel()
	}
	s.batch = nil

	if s.accounts != nil && s.accounts.cancel != nil {
		s.accounts.cancel()
	}
	if s.maintenance != nil && s.maintenance.cancel != nil {
		s.maintenance.cancel()
	}
	s.accounts, s.maintenance = nil, nil

	if s.objectOp != nil && s.objectOp.cancel != nil {
		s.objectOp.cancel()
	}
	s.objectOp, s.parkedObjectOp = nil, nil

	if s.tableCancel != nil {
		s.tableCancel()
	}
	s.tableCancel, s.tableResults = nil, nil
	s.tablePending, s.tableDialog = false, false
	s.tableDatabase, s.tableName, s.tableNewName, s.tableConfirmation, s.tableError = "", "", "", "", ""
	clear(s.browsers)
	for _, d := range s.documents {
		s.Close(d)
	}
}

// Consume runs on the UI thread. Generation changes invalidate completed work
// without leaving a document stuck loading or automatically retrying it.
func (s *State) Consume(services ui.Services) {
	s.consumeCollections(services)
	s.consumeKeys()
	s.consumeSettings(services)
	s.consumeTasks(services)
	s.consumeMaintenance(services)
	s.consumePortableBackup(services)
	s.consumeView(services)
	s.consumeSchemaCopy(services)
	s.consumeSchemaDrop(services)
	s.consumeSchemaRename(services)
	s.consumeDictionary(services)
	s.consumeObjectReference(services)
	s.consumeObjectSearch()
	s.validateGroupDialog()
	s.consumeBackup(services)
	s.consumeMySQLTableChange(services)
	s.consumeDesign(services)
	s.consumeSchemaDesign(services)
	s.consumeObjectOperations(services)
	s.consumeAdmin(services)
	s.consumeBatch(services)
	session := s.session()
	for _, d := range s.documents {
		if d.closed || d.results == nil || d.profileID != session.ProfileID {
			continue
		}
		select {
		case r := <-d.results:
			if r.profileID != d.profileID || r.request != d.request {
				continue
			}
			s.cancel(d)
			if r.generation != session.Generation || !session.Connected {
				d.err = "原 MySQL 会话已失效，请刷新表结构"
				continue
			}
			if r.err != nil {
				if !errors.Is(r.err, context.Canceled) {
					d.err = r.err.Error()
				}
				continue
			}
			if r.data == nil {
				d.err = "服务器未返回表结构"
				continue
			}
			d.data, d.err = r.data, ""
		default:
		}
	}
}

func (s *State) OpenProgram(id uint64, profileID int64, database string, data contract.DBObject) *Document {
	p := newProgram(profileID, database, data)
	if p == nil {
		return nil
	}
	if old := s.documents[id]; old != nil {
		s.Close(old)
	}
	d := &Document{object: data.Ref, id: id, profileID: profileID, database: database, table: data.Name, program: p}
	s.documents[id] = d
	return d
}
func (d *Document) Name() string { return d.table }
func (d *Document) Kind() string {
	if d.program != nil {
		return d.program.Kind()
	}
	return "structure"
}
func (d *Document) ProgramData() contract.DBObject {
	if d.program == nil {
		return contract.DBObject{}
	}
	return d.program.Data()
}
func (d *Document) Update(data contract.DBObject) bool {
	return !d.closed && d.program != nil && d.program.Update(data)
}

func (d *Document) ObjectKind() string {
	if d.program != nil {
		return d.program.Kind()
	}
	return d.objectKind
}

func (d *Document) Object() contract.ObjectRef { return d.object }
func (s *State) OpenQualifiedStructure(id uint64, o contract.ObjectRef, kind string) *Document {
	if o.Kind == "collection_management" {
		return s.OpenCollectionManagement(id, o)
	}
	if o.Kind == "keys" {
		return s.OpenKeys(id, o)
	}
	if o.Kind == "settings" && s.SettingsScopeSupported(o) {
		return s.OpenSettings(id, o)
	}
	if o.Kind == "tasks" {
		return s.OpenTasks(id, o)
	}
	namespace := o.Schema
	if namespace == "" {
		namespace = o.Catalog
	}
	if s.deps.NamespaceLabel != nil {
		namespace = s.deps.NamespaceLabel(o.Namespace())
	}
	d := s.OpenStructure(id, o.ProfileID, namespace, o.Name, kind)
	if d != nil {
		d.object = o
	}
	return d
}
