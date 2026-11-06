package contract

import (
	"context"
	"time"
)

type DataImportRowSource interface {
	Next(context.Context) (DataEdit, error)
	Close() error
}

type DataImportStreamRequest struct {
	Generation            uint64
	CanonicalBackupValues bool

	Request           DataImportRequest
	Rows              int64
	Open              func(context.Context) (DataImportRowSource, error)
	Record            func(DataImportRowResult) error
	Progress          func(DataImportProgress)
	IgnoreForeignKeys bool
	ExtendedInsert    bool
	StatementMaxBytes int
}

type DataImportProgress struct {
	Processed, Prepared, Skipped, Failed int64
}

type DataImportStreamResult struct {
	Created, CreationUncertain, Committed, CommitUncertain bool
	Progress                                               DataImportProgress
	Rows                                                   []DataImportRowResult // bounded diagnostic sample; full records are journaled
}

type DataImportRequest struct {
	Mode            string // append | update | upsert | insert_missing | delete | replace; empty means append
	MatchColumns    []string
	ContinueOnError bool
	NewTable        bool
	Columns         []DataImportColumn
	Edits           []DataEdit
}

type DataImportRowResult struct {
	Database, Table string `json:",omitempty"`
	Row             int
	Kind            string // insert | update | delete | skip | error
	Status          string // committed | skipped | failed | rolled_back | not_processed | uncertain
	Error           string // safe diagnostic; never includes input values or server SQL
	Outcome         *DataEditOutcome
}

type DataImportResult struct {
	Created, CreationUncertain bool
	Committed, CommitUncertain bool
	Rows                       []DataImportRowResult
	Saved                      *DataEditResult
}

type DataImportColumn struct {
	Name, Type           string
	Nullable, PrimaryKey bool
}

type DataNewTableImportResult struct {
	Created, CreationUncertain bool
	Saved                      *DataEditResult
}

type DataAnalysis struct {
	Rows        int64
	Columns     []DataColumnAnalysis
	CompletedAt time.Time
	Duration    time.Duration
}

type DataColumnAnalysis struct {
	Name, Kind, Collation   string
	Rows, Null, Distinct    int64
	Singletons              int64 // non-NULL value groups occurring exactly once
	Numeric, Zero           int64
	Min, Max, Mean          string
	Median, StdDeviation    string
	MaxFrequency, ModeCount int64
	Modes                   []DataAnalysisFrequency
	Distribution            []DataAnalysisFrequency
}

type DataAnalysisFrequency struct {
	Value     string
	Count     int64
	Truncated bool
	Null      bool
}

type FieldChunk struct {
	Text                           string
	Offset, NextOffset, TotalUnits int64
	Binary, Null, HasMore          bool
}

type DataFilterRule struct {
	Column string
	Mode   string // equal, not_equal, contains, not_contains, like, not_like, less, greater, is_null, is_not_null
	Value  string
}

type DataSortRule struct {
	Column     string
	Descending bool
}

type DataCursor struct {
	RuleSignature                         string
	Database, Table                       string
	FilterColumn, FilterMode, FilterValue string
	SortColumn                            string
	SortDescending                        bool
	Columns                               []string
	Descending                            []bool
	Values                                [][]byte
}

type RowRef struct {
	TypedValues []Cell
	Object      ObjectRef

	Database, Table string
	Columns         []string
	Values          [][]byte
}

type DataOptions struct {
	Condition      *FilterExpression[DataFilterRule]
	Object         ObjectRef
	Offset         int64
	Limit          int
	FilterColumn   string
	FilterValue    string
	FilterMode     string // "" | "equal" | "contains"
	SortColumn     string
	SortDescending bool
	UseKeyset      bool
	After          *DataCursor
	Filters        []DataFilterRule
	Sorts          []DataSortRule
}

type DataValue struct {
	Kind CellKind

	Text                  string
	Bytes                 []byte
	Null, Default, Binary bool
}

type RowSnapshot struct {
	Mode     string // fingerprints or values
	Original []Cell

	Ref          RowRef
	Columns      []string
	Fingerprints []*string // nil denotes SQL NULL, otherwise SHA256 of server bytes
	Metadata     string
}

type DataRow struct {
	Columns  []StructureColumn
	Values   map[string]DataValue
	Snapshot *RowSnapshot
}

type DataEdit struct {
	Kind     string // insert | update | delete
	Original *RowSnapshot
	Values   map[string]DataValue
}

type DataEditOutcome struct {
	Kind            string
	Original, Saved *RowSnapshot
	GeneratedID     int64
	PreviewColumns  []string
	Previews        []DataCell // saved server values in ColumnMetadata order, bounded like page previews
}

type DataEditResult struct {
	Applied              int
	CommitConfirmed      bool
	Incomplete, Verified bool
	Saved                []Edit

	Outcomes        []DataEditOutcome
	CommitUncertain bool
}

type DataVerification struct {
	Recovered *DataEditResult
	Status    string // applied | not_applied | indeterminate
	States    []string
}

type DataCell struct {
	Value *Cell // Authoritative typed preview; Text/Null retain the legacy presentation.

	Text      string
	Null      bool
	Truncated bool
}

type DataPage struct {
	Object   ObjectRef
	Editable *bool

	Database, Table string
	Columns         []string
	ColumnKinds     []string          // original server DATA_TYPE per displayed column
	ColumnMetadata  []StructureColumn // full server field attributes, in Columns order
	Rows            [][]DataCell
	Offset          int64
	Limit           int
	HasMore         bool
	StableOrder     bool // true when a complete non-null unique index orders rows.
	KeysetAvailable bool
	NextCursor      *DataCursor
	RowRefs         []*RowRef
	RowSnapshots    []*RowSnapshot
	// QuerySQL is the executed parameterized statement, without bound values.
	QuerySQL    string
	CompletedAt time.Time
	Duration    time.Duration
}

type TableInfo struct {
	Database, Name, Kind, Engine, RowFormat, Collation, Comment, CreateOptions     string
	CreatedAt, ModifiedAt, CheckedAt                                               string
	EstimatedRows, AutoIncrement, DataLength, IndexLength, MaxDataLength, DataFree *int64
}

type DBObject struct {
	HasIndexes, HasTriggers *bool
	RootPage                *int64
	Ref                     ObjectRef

	Name          string
	Kind          string
	Engine        string
	Comment       string
	AutoIncrement *int64 // nil if the server has no next AUTO_INCREMENT value
	ModifiedAt    string // server UPDATE_TIME; empty if unknown
	DataLength    *int64 // server DATA_LENGTH in bytes; nil if unavailable
	RowsEstimate  *int64 // TABLE_ROWS is an estimate for engines such as InnoDB
	Definer       string
	Parameters    string
	ReturnType    string
	SQLSecurity   string
	CreatedAt     string
	Status        string
	Schedule      string
	Starts        string
	Ends          string
	OnCompletion  string
	Definition    string        // only populated when server privileges permit
	Sequence      *SequenceInfo // immutable sequence settings, never its current value
}

type SequenceInfo struct {
	DefinitionFromServer              bool
	DataType                          string
	Start, Increment, Min, Max, Cache int64
	Cycle                             bool
}

type TableStructure struct {
	Object      ObjectRef
	Definitions []Definition
	Definition  string
	Editable    bool

	Database, Name, Kind, Engine, Comment, Collation string
	EstimatedRows                                    *int64 // TABLE_ROWS is approximate for InnoDB and NULL for some views.
	Columns                                          []StructureColumn
	Indexes                                          []StructureIndex
	ForeignKeys                                      []StructureForeignKey
}

type StructureColumn struct {
	Generated bool
	ValueKind CellKind

	Name, Type, DataType, Key, Extra, Comment          string
	Position                                           int64
	Nullable                                           bool
	Default                                            *string // nil distinguishes SQL NULL from empty string.
	CharacterMaxLength, NumericPrecision, NumericScale *int64
	DatetimePrecision                                  *int64
	CharacterSet, Collation                            *string // nil for types without charset.
}

type StructureIndexColumn struct {
	Name, Direction string
	Position        int64
	SubPart         *int64
}

type StructureIndex struct {
	Name, Type string
	Unique     bool
	Columns    []StructureIndexColumn
}

type StructureForeignKeyColumn struct {
	Name, ReferencedName string
	Position             int64
}

type StructureForeignKey struct {
	Name, ReferencedDatabase, ReferencedTable, OnUpdate, OnDelete string
	Columns                                                       []StructureForeignKeyColumn
}
