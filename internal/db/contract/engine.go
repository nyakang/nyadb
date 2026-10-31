package contract

import "context"

// Table carries public metadata only. Editable is conservative: views, nullable
// unique keys and generated-only keys never grant writable row identity.
type Definition struct{ Name, Kind, SQL string }
type Table struct {
	Definitions []Definition
	Object      ObjectRef
	Columns     []Field
	Key         []string
	Editable    bool
	Definition  string
}
type Field struct {
	Column
	Generated bool
	Default   *string
	Primary   bool
}
type Page struct {
	Truncation []string
	Table      Table
	Rows       [][]Cell
	Identities []RowIdentity
	Offset     int64
	More       bool
}
type Object struct{ Ref ObjectRef }
type Transaction interface {
	Page(context.Context, PageRequest) (Page, error)
	Apply(context.Context, ObjectRef, []Edit) (CommitResult, error)
	Commit(context.Context) (CommitResult, error)
	Rollback(context.Context) error
	Close()
}
type StreamSink interface {
	BeginResult(int, []Column) error
	WriteRow(int, []Cell) error
	EndResult(int, int64) error
}

// Session methods never reconnect or replay a command. A revoked session must
// be explicitly connected again by its host.
type Verification struct {
	Matched bool
	States  []string
}
type Session interface {
	Verify(context.Context, ObjectRef, []Edit) (Verification, error)
	ProfileID() int64
	Generation() uint64
	Capabilities() Capabilities
	Catalog() string
	Schemas(context.Context) ([]string, error)
	Objects(context.Context, string) ([]Object, error)
	Structure(context.Context, ObjectRef) (Table, error)
	Page(context.Context, PageRequest) (Page, error)
	Begin(context.Context) (Transaction, error)
	Reserve(context.Context) (SQLLease, error)
	Close() error
}
type SQLLease interface {
	SetSchema(context.Context, string) error
	Execute(context.Context, string) (SQLResult, error)
	Stream(context.Context, string, StreamSink) (SQLResult, error)
	Close() error
}
