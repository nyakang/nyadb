package mysql

import (
	"context"
	"database/sql"
	"errors"
)

const MySQLDataEditMaxBytes = 1 << 20

var (
	ErrMySQLDataConflict        = errors.New("record changed or disappeared; reload before saving")
	ErrMySQLDataReadOnly        = errors.New("table has no complete non-null unique key or is not transactional")
	ErrMySQLDataEditLimit       = errors.New("complete record exceeds the 1 MiB editor limit")
	ErrMySQLDataCommitUncertain = errors.New("commit acknowledgement failed; verify before retrying")
)

// Values preserve exact decimal/integer spelling. DEFAULT and NULL are separate
// from an empty string. Binary values never travel through a text conversion.
type MySQLDataValue struct {
	Text                  string
	Bytes                 []byte
	Null, Default, Binary bool
}
type MySQLRowSnapshot struct {
	Ref          MySQLRowRef
	Columns      []string
	Fingerprints []*string // nil denotes SQL NULL, otherwise SHA256 of server bytes
	Metadata     string
}
type MySQLDataRow struct {
	Columns  []MySQLStructureColumn
	Values   map[string]MySQLDataValue
	Snapshot *MySQLRowSnapshot
}
type MySQLDataEdit struct {
	Kind     string // insert | update | delete
	Original *MySQLRowSnapshot
	Values   map[string]MySQLDataValue
}
type MySQLDataEditOutcome struct {
	Kind            string
	Original, Saved *MySQLRowSnapshot
	GeneratedID     int64
	PreviewColumns  []string
	Previews        []MySQLDataCell // saved server values in ColumnMetadata order, bounded like page previews
}
type MySQLDataEditResult struct {
	Outcomes        []MySQLDataEditOutcome
	CommitUncertain bool
}
type MySQLDataVerification struct {
	Status string // applied | not_applied | indeterminate
	States []string
}

type mysqlEditQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
