package contract

import (
	"context"
	"errors"
	"io"
)

var ErrUnsupported = errors.New("database operation is not supported")
var ErrRevoked = errors.New("database session revoked; reconnect explicitly")

// NamespaceRef is a database identity, never a navigation label or file path.
type NamespaceRef struct {
	ProfileID       int64
	Catalog, Schema string
}

func (n NamespaceRef) Object(kind, name string) ObjectRef {
	return ObjectRef{n.ProfileID, n.Catalog, n.Schema, kind, name}
}
func (o ObjectRef) Namespace() NamespaceRef { return NamespaceRef{o.ProfileID, o.Catalog, o.Schema} }

type Namespace struct {
	Ref   NamespaceRef
	Label string
}

type MetadataProvider interface {
	Namespaces(context.Context) ([]Namespace, error)
	Browse(context.Context, NamespaceRef) ([]DBObject, error)
	TableMetadata(context.Context, ObjectRef) (*TableStructure, error)
}
type QueryLease interface {
	Preconnect(context.Context) error
	Execute(context.Context, string, string) (SQLResult, error)
	ExecuteStream(context.Context, string, string, func(int, []Column, []Cell) error) (SQLResult, error)
	Replace() (QueryLease, error)
	Release()
}
type QueryFileLease interface {
	ExecuteFile(context.Context, string, io.Reader) (SQLResult, error)
}
type QueryProvider interface {
	ReserveQuery(uint64) (QueryLease, error)
}

// QueryLeaseStatus checks the captured lease/publication without I/O or waiting
// for its executor. UI consumers use it before admitting parked auxiliary reads.
type QueryLeaseStatus interface {
	CheckQueryLease() error
}
type DataProvider interface{ DataRuntime() DataRuntime }

// DataDocumentLease owns server resources for one immutable data document.
// Reservation performs no I/O; Close revokes requests and waits for their
// executors before releasing resources, including unpublished read results.
type DataDocumentLease interface {
	Runtime() DataRuntime
	Close() error
}
type DataDocumentsProvider interface {
	ReserveDataDocument(ObjectRef) (DataDocumentLease, error)
}

// DataDocumentFeaturesProvider describes operations available after reservation,
// without opening a document or performing I/O. Each reserved Runtime is checked
// again against its actual functions and the product/session capabilities.
type DataDocumentFeaturesProvider interface {
	DataDocumentCapabilities() Capabilities
}
type DataTransaction interface{ Close() }

type DataTransactions struct {
	Begin    func(context.Context, int64, string, string) (DataTransaction, error)
	Commit   func(context.Context, DataTransaction) error
	Rollback func(context.Context, DataTransaction) error
	Close    func(DataTransaction)
	Page     func(context.Context, DataTransaction, int64, string, string, DataOptions) (*DataPage, error)
	Save     func(context.Context, DataTransaction, int64, string, string, []DataEdit) (*DataEditResult, error)
	Row      func(context.Context, DataTransaction, int64, string, string, RowRef) (*DataRow, error)
	Chunk    func(context.Context, DataTransaction, int64, string, string, string, RowRef, int64, int) (*FieldChunk, error)
}

// DataSelector describes an adapter's native readonly JSON selector. Validation
// is pure and must not reserve a resource or dispatch a database request.
type DataSelector struct {
	Dialect, Column, Mode string
	Validate              func(string) error
}

// DataRuntime contains operation-bound functions. Workers capture this value
// before starting; adapters must check the original session on every call.
type DataRuntime struct {
	// PageFailureRequiresReopen retires the original data range after a failed
	// or canceled page. The UI waits for acknowledgement and requires a new
	// explicit document reservation; auxiliary readers keep their own scopes.
	PageFailureRequiresReopen bool
	// AsyncWrites requires explicit admission observation through Verify.
	AsyncWrites bool
	// DocumentImport accepts whole JSON documents through Importer; requires Verify.
	DocumentImport       bool
	DocumentImportColumn string              // Empty preserves whole-document imports using document.
	DocumentImportModes  []string            // Nil preserves document_replace/document_merge.
	ImportDescription    func(string) string // Pure native request information for the captured mode.
	WriteDescription     string
	NativeReceipts       bool       // Synchronous native transaction evidence, including unknown outcomes.
	EditKinds            []string   // Nil permits insert/update/delete; an explicit list limits native mutations.
	EditValueKinds       []CellKind // Optional native editor kinds; nil retains the existing editor choices.
	// InsertOmittedValues starts new rows with omitted fields rather than SQL
	// DEFAULT. Generated fields remain read-only; only explicit values are sent.
	InsertOmittedValues bool
	ReserveDocument     func(ObjectRef) (DataDocumentLease, error)
	ExportFormats       []string // nil preserves existing providers' format availability.
	ImportDialect       string
	Selector            DataSelector
	ReadDescription     func(DataOptions) string // pure, parameter-free native request information.
	Loader              func(context.Context, int64, string, string, int64, int) (*DataPage, error)
	AdvancedLoader      func(context.Context, int64, string, string, DataOptions) (*DataPage, error)
	Saver               func(context.Context, int64, string, string, []DataEdit) (*DataEditResult, error)
	// Preview/Submit are an optional original-document mutation path. The UI
	// must obtain explicit approval of the immutable preview before Submit.
	PreviewEditKinds []string
	// ReadGraphEndpoints captures complete directed nodes through the original
	// relationship document. Nil explicitly excludes relationship creation.
	ReadGraphEndpoints func(context.Context, int64, string, string, string, string) (*DataGraphEndpoints, error)
	PreviewEdits       func(context.Context, int64, string, string, []DataEdit) (DataMutationPlan, error)
	SubmitEdits        func(context.Context, int64, string, string, DataMutationPlan) (*DataEditResult, error)
	RowReader          func(context.Context, int64, string, string, RowRef) (*DataRow, error)
	Verify             func(context.Context, int64, string, string, *DataEditResult) (*DataVerification, error)
	Exporter           func(context.Context, int64, string, string, DataOptions, []string, func([]DataValue) error) error
	Analyzer           func(context.Context, int64, string, string, DataOptions, []string) (*DataAnalysis, error)
	Transactions       DataTransactions
	NewTableImporter   func(context.Context, int64, string, string, []DataImportColumn, []DataEdit) (*DataNewTableImportResult, error)
	Importer           func(context.Context, int64, string, string, DataImportRequest) (*DataImportResult, error)
	StreamImporter     func(context.Context, int64, string, string, DataImportStreamRequest) (*DataImportStreamResult, error)
	// Graph exchange preserves full typed entities and explicit source endpoint
	// mappings. Submission consumes the original lease's immutable preview once.
	PreviewGraphImport func(context.Context, int64, string, string, GraphImportRequest) (GraphImportPlan, error)
	SubmitGraphImport  func(context.Context, int64, string, string, GraphImportPlan) (*GraphImportResult, error)
	ExportGraph        func(context.Context, ObjectRef) (GraphImportBundle, error)
	TableMetadata      func(context.Context, int64, string, string) (*TableStructure, error)
	TableInfo          func(context.Context, int64, string, string) (*TableInfo, error)
	Count              func(context.Context, int64, string, string) (int64, error)
	Chunk              func(context.Context, int64, string, string, string, RowRef, int64, int) (*FieldChunk, error)
	JSONL              func(context.Context, ObjectRef, io.Writer) error
	Capabilities       Capabilities
	FilterModes        []string
	FilterColumns      []string // nil permits all fields; empty permits none.
	SortColumns        []string // nil preserves metadata defaults; empty disables sorting.
}

// Require traverses a generation wrapper without exposing drivers to callers.
func Require[T any](s Session) (T, error) {
	for s != nil {
		if v, ok := any(s).(T); ok {
			return v, nil
		}
		if v, ok := s.(interface{ Underlying() Session }); ok {
			s = v.Underlying()
		} else {
			break
		}
	}
	var zero T
	return zero, ErrUnsupported
}

// Feature providers are independent; an absent provider leaves its operations disabled.
type DataReads struct {
	Loader         func(context.Context, int64, string, string, int64, int) (*DataPage, error)
	AdvancedLoader func(context.Context, int64, string, string, DataOptions) (*DataPage, error)
	RowReader      func(context.Context, int64, string, string, RowRef) (*DataRow, error)
	TableMetadata  func(context.Context, int64, string, string) (*TableStructure, error)
	TableInfo      func(context.Context, int64, string, string) (*TableInfo, error)
	Count          func(context.Context, int64, string, string) (int64, error)
	Chunk          func(context.Context, int64, string, string, string, RowRef, int64, int) (*FieldChunk, error)
}
type DataReadsProvider interface{ DataReads() DataReads }
type DataEdits struct {
	Saver  func(context.Context, int64, string, string, []DataEdit) (*DataEditResult, error)
	Verify func(context.Context, int64, string, string, *DataEditResult) (*DataVerification, error)
}
type DataEditsProvider interface{ DataEdits() DataEdits }
type DataExports struct {
	Formats  []string // An explicit list limits the neutral workbench serializers.
	Exporter func(context.Context, int64, string, string, DataOptions, []string, func([]DataValue) error) error
	JSONL    func(context.Context, ObjectRef, io.Writer) error
}
type DataExportsProvider interface{ DataExports() DataExports }
type DataAnalysisOperations struct {
	Analyzer func(context.Context, int64, string, string, DataOptions, []string) (*DataAnalysis, error)
}
type DataAnalysisProvider interface{ DataAnalysisOperations() DataAnalysisOperations }
type DataImports struct {
	Dialect          string
	NewTableImporter func(context.Context, int64, string, string, []DataImportColumn, []DataEdit) (*DataNewTableImportResult, error)
	Importer         func(context.Context, int64, string, string, DataImportRequest) (*DataImportResult, error)
	StreamImporter   func(context.Context, int64, string, string, DataImportStreamRequest) (*DataImportStreamResult, error)
}
type DataImportsProvider interface{ DataImports() DataImports }
type DataTransactionsProvider interface{ DataTransactions() DataTransactions }

// ComposeData resolves each feature independently into the workbench binding.
func ComposeData(s Session) DataRuntime {
	r := DataRuntime{Capabilities: s.Capabilities()}
	if p, e := Require[DataDocumentsProvider](s); e == nil {
		r.ReserveDocument = p.ReserveDataDocument
	}
	if p, e := Require[DataReadsProvider](s); e == nil {
		v := p.DataReads()
		r.Loader = v.Loader
		r.AdvancedLoader = v.AdvancedLoader
		r.RowReader = v.RowReader
		r.TableMetadata = v.TableMetadata
		r.TableInfo = v.TableInfo
		r.Count = v.Count
		r.Chunk = v.Chunk
	}
	if p, e := Require[DataEditsProvider](s); e == nil {
		v := p.DataEdits()
		r.Saver = v.Saver
		r.Verify = v.Verify
	}
	if p, e := Require[DataExportsProvider](s); e == nil {
		v := p.DataExports()
		r.ExportFormats = append([]string(nil), v.Formats...)
		r.Exporter = v.Exporter
		r.JSONL = v.JSONL
	}
	if p, e := Require[DataAnalysisProvider](s); e == nil {
		v := p.DataAnalysisOperations()
		r.Analyzer = v.Analyzer
	}
	if p, e := Require[DataImportsProvider](s); e == nil {
		v := p.DataImports()
		r.ImportDialect = v.Dialect
		r.NewTableImporter = v.NewTableImporter
		r.Importer = v.Importer
		r.StreamImporter = v.StreamImporter
	}
	if p, e := Require[DataTransactionsProvider](s); e == nil {
		r.Transactions = p.DataTransactions()
	}
	if p, e := Require[interface{ DataFilterModes() []string }](s); e == nil {
		r.FilterModes = p.DataFilterModes()
	}
	return r
}
