package contract

import (
	"errors"
	"strings"
)

var (
	ErrDataConflict        = errors.New("record changed or disappeared; reload before saving")
	ErrDataReadOnly        = errors.New("table has no complete non-null unique key or is not transactional")
	ErrDataEditLimit       = errors.New("complete record exceeds the 1 MiB editor limit")
	ErrDataCommitUncertain = errors.New("commit acknowledgement failed; verify before retrying")
	ErrDataPage            = errors.New("invalid MySQL data page")
	ErrDataTransactionLost = errors.New("MySQL data transaction lost; refresh the table before starting another transaction")
	ErrSQLSessionLost      = errors.New("MySQL SQL tab session lost; open a new query tab")
	ErrSQLSessionLimit     = errors.New("MySQL SQL tab session limit reached")
)

const (
	DataDefaultPageSize     = 100
	DataMaxPageSize         = 200
	DataEditMaxBytes        = 1 << 20
	DataExportFieldMaxBytes = 1 << 20
	DataExportRowMaxBytes   = 16 << 20
	DataMaxFilterRules      = 6
	DataMaxSortRules        = 4
	MaxDataImportRows       = 10000
	MaxDataImportBytes      = 16 << 20
)

func DataColumnGenerated(col StructureColumn) bool {
	extra := strings.ToUpper(col.Extra)
	return col.Generated || strings.Contains(extra, "VIRTUAL GENERATED") || strings.Contains(extra, "STORED GENERATED")
}
