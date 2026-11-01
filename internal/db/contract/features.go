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
type DataProvider interface{ DataRuntime() DataRuntime }
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

// DataRuntime contains operation-bound functions. Workers capture this value
// before starting; adapters must check the original session on every call.
type DataRuntime struct {
	Loader           func(context.Context, int64, string, string, int64, int) (*DataPage, error)
	AdvancedLoader   func(context.Context, int64, string, string, DataOptions) (*DataPage, error)
	Saver            func(context.Context, int64, string, string, []DataEdit) (*DataEditResult, error)
	RowReader        func(context.Context, int64, string, string, RowRef) (*DataRow, error)
	Verify           func(context.Context, int64, string, string, *DataEditResult) (*DataVerification, error)
	Exporter         func(context.Context, int64, string, string, DataOptions, []string, func([]DataValue) error) error
	Analyzer         func(context.Context, int64, string, string, DataOptions, []string) (*DataAnalysis, error)
	Transactions     DataTransactions
	NewTableImporter func(context.Context, int64, string, string, []DataImportColumn, []DataEdit) (*DataNewTableImportResult, error)
	Importer         func(context.Context, int64, string, string, DataImportRequest) (*DataImportResult, error)
	StreamImporter   func(context.Context, int64, string, string, DataImportStreamRequest) (*DataImportStreamResult, error)
	TableMetadata    func(context.Context, int64, string, string) (*TableStructure, error)
	TableInfo        func(context.Context, int64, string, string) (*TableInfo, error)
	Count            func(context.Context, int64, string, string) (int64, error)
	Chunk            func(context.Context, int64, string, string, string, RowRef, int64, int) (*FieldChunk, error)
	JSONL            func(context.Context, ObjectRef, io.Writer) error
	Capabilities     Capabilities
	FilterModes      []string
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
	Exporter func(context.Context, int64, string, string, DataOptions, []string, func([]DataValue) error) error
	JSONL    func(context.Context, ObjectRef, io.Writer) error
}
type DataExportsProvider interface{ DataExports() DataExports }
type DataAnalysisOperations struct {
	Analyzer func(context.Context, int64, string, string, DataOptions, []string) (*DataAnalysis, error)
}
type DataAnalysisProvider interface{ DataAnalysisOperations() DataAnalysisOperations }
type DataImports struct {
	NewTableImporter func(context.Context, int64, string, string, []DataImportColumn, []DataEdit) (*DataNewTableImportResult, error)
	Importer         func(context.Context, int64, string, string, DataImportRequest) (*DataImportResult, error)
	StreamImporter   func(context.Context, int64, string, string, DataImportStreamRequest) (*DataImportStreamResult, error)
}
type DataImportsProvider interface{ DataImports() DataImports }
type DataTransactionsProvider interface{ DataTransactions() DataTransactions }

// ComposeData resolves each feature independently into the workbench binding.
func ComposeData(s Session) DataRuntime {
	r := DataRuntime{Capabilities: s.Capabilities()}
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
		r.Exporter = v.Exporter
		r.JSONL = v.JSONL
	}
	if p, e := Require[DataAnalysisProvider](s); e == nil {
		v := p.DataAnalysisOperations()
		r.Analyzer = v.Analyzer
	}
	if p, e := Require[DataImportsProvider](s); e == nil {
		v := p.DataImports()
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
