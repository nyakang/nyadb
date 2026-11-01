package mysql

import (
	"context"
	"database/sql"
	"github.com/nyakang/nyadb/internal/db/contract"
)

const MySQLDataEditMaxBytes = 1 << 20

var (
	ErrMySQLDataConflict        = contract.ErrDataConflict
	ErrMySQLDataReadOnly        = contract.ErrDataReadOnly
	ErrMySQLDataEditLimit       = contract.ErrDataEditLimit
	ErrMySQLDataCommitUncertain = contract.ErrDataCommitUncertain
)

// Values preserve exact decimal/integer spelling. DEFAULT and NULL are separate
// from an empty string. Binary values never travel through a text conversion.
type MySQLDataValue = contract.DataValue
type MySQLRowSnapshot = contract.RowSnapshot
type MySQLDataRow = contract.DataRow
type MySQLDataEdit = contract.DataEdit
type MySQLDataEditOutcome = contract.DataEditOutcome
type MySQLDataEditResult = contract.DataEditResult
type MySQLDataVerification = contract.DataVerification

type mysqlEditQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
