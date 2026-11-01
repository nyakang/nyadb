package mysql

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/services/backups"
	"io"
)

// Administration is the optional MySQL management surface.
type Administration interface {
	SessionAdministration
	TableAdministration
	ObjectAdministration
	AccountAdministration
	BackupAdministration
}

type SessionAdministration interface {
	SessionGeneration(id int64) uint64
}

type TableAdministration interface {
	TableDesign(ctx context.Context, id int64, database, table string) (mysqlddl.Snapshot, error)
	ApplyTableDesign(ctx context.Context, id int64, plan mysqlddl.Plan) DDLOutcome
	ChangeTable(ctx context.Context, id int64, change MySQLTableChange) error
	TableStructure(ctx context.Context, id int64, database, table string) (*MySQLTableStructure, error)
	MaintainTable(ctx context.Context, id int64, database, table, operation string) ([]MaintenanceMessage, error)
}

type ObjectAdministration interface {
	ApplyObjectBatch(ctx context.Context, id int64, generation uint64, items []ObjectBatchItem) []ObjectBatchResult
	ObjectDefinition(ctx context.Context, id int64, ref ObjectIdentity) (ObjectDefinition, error)
	ApplyObject(ctx context.Context, id int64, p ObjectPlan) ObjectOutcome
}

type AccountAdministration interface {
	Accounts(ctx context.Context, id int64) ([]MySQLAccount, error)
	AccountGrants(ctx context.Context, id int64, a MySQLAccount) ([]string, error)
	ChangeAccounts(ctx context.Context, id int64, generation uint64, changes []AccountChange) DDLOutcome
}

type BackupAdministration interface {
	BackupMySQL(ctx context.Context, id int64, request BackupRequest, w io.Writer) (backups.Manifest, error)
	DumpMySQL(ctx context.Context, id int64, request BackupRequest, w io.Writer) error
	PlanRestore(ctx context.Context, id int64, generation uint64, database, policy string, r *backups.Reader) (RestorePlan, error)
	RestoreMySQL(ctx context.Context, plan RestorePlan) DDLOutcome
}
