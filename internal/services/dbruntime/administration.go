package dbruntime

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/services/backups"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"io"
)

type administration struct {
	mysqlsvc.Administration
	id         int64
	generation uint64
	session    *session
}

func (a *administration) SessionGeneration(id int64) uint64 {
	if id != a.id || (a.session != nil && !a.session.alive()) || a.Administration.SessionGeneration(id) == 0 {
		return 0
	}
	return a.generation
}
func (a *administration) check(id int64, generation uint64) error {
	if id != a.id || generation != a.generation || a.SessionGeneration(id) == 0 {
		return contract.ErrRevoked
	}
	return nil
}
func (a *administration) context(ctx context.Context, id int64) (context.Context, error) {
	generation := mysqlsvc.ExpectedMySQLSessionGeneration(ctx)
	if generation == 0 {
		generation = a.generation
	}
	if err := a.check(id, generation); err != nil {
		return ctx, err
	}
	return mysqlsvc.WithMySQLSessionGeneration(ctx, a.Administration.SessionGeneration(id)), nil
}
func (a *administration) Accounts(ctx context.Context, id int64) ([]mysqlsvc.MySQLAccount, error) {
	ctx, e := a.context(ctx, id)
	if e != nil {
		return nil, e
	}
	return a.Administration.Accounts(ctx, id)
}
func (a *administration) AccountGrants(ctx context.Context, id int64, account mysqlsvc.MySQLAccount) ([]string, error) {
	ctx, e := a.context(ctx, id)
	if e != nil {
		return nil, e
	}
	return a.Administration.AccountGrants(ctx, id, account)
}
func (a *administration) MaintainTable(ctx context.Context, id int64, db, table, operation string) ([]mysqlsvc.MaintenanceMessage, error) {
	ctx, e := a.context(ctx, id)
	if e != nil {
		return nil, e
	}
	return a.Administration.MaintainTable(ctx, id, db, table, operation)
}
func (a *administration) ChangeTable(ctx context.Context, id int64, change mysqlsvc.MySQLTableChange) error {
	ctx, e := a.context(ctx, id)
	if e != nil {
		return e
	}
	return a.Administration.ChangeTable(ctx, id, change)
}
func (a *administration) TableStructure(ctx context.Context, id int64, db, table string) (*mysqlsvc.MySQLTableStructure, error) {
	ctx, e := a.context(ctx, id)
	if e != nil {
		return nil, e
	}
	return a.Administration.TableStructure(ctx, id, db, table)
}
func (a *administration) ChangeAccounts(ctx context.Context, id int64, generation uint64, c []mysqlsvc.AccountChange) mysqlsvc.DDLOutcome {
	ctx, ectx := a.context(ctx, id)
	if ectx != nil {
		e := ectx
		return mysqlsvc.DDLOutcome{Error: e}
	}

	if e := a.check(id, generation); e != nil {
		return mysqlsvc.DDLOutcome{Error: e}
	}
	return a.Administration.ChangeAccounts(ctx, id, a.Administration.SessionGeneration(id), c)
}
func (a *administration) ApplyObjectBatch(ctx context.Context, id int64, generation uint64, v []mysqlsvc.ObjectBatchItem) []mysqlsvc.ObjectBatchResult {
	ctx, ectx := a.context(ctx, id)
	if ectx != nil {
		return nil
	}

	if a.check(id, generation) != nil {
		return nil
	}
	return a.Administration.ApplyObjectBatch(ctx, id, a.Administration.SessionGeneration(id), v)
}
func (a *administration) ObjectDefinition(ctx context.Context, id int64, ref mysqlsvc.ObjectIdentity) (mysqlsvc.ObjectDefinition, error) {
	ctx, ectx := a.context(ctx, id)
	if ectx != nil {
		e := ectx
		return mysqlsvc.ObjectDefinition{}, e
	}

	v, e := a.Administration.ObjectDefinition(ctx, id, ref)
	v.Generation = a.generation
	return v, e
}
func (a *administration) ApplyObject(ctx context.Context, id int64, p mysqlsvc.ObjectPlan) mysqlsvc.ObjectOutcome {
	ctx, ectx := a.context(ctx, id)
	if ectx != nil {
		e := ectx
		return mysqlsvc.ObjectOutcome{DDLOutcome: mysqlsvc.DDLOutcome{Error: e}}
	}

	if e := a.check(id, p.Original.Generation); e != nil {
		return mysqlsvc.ObjectOutcome{DDLOutcome: mysqlsvc.DDLOutcome{Error: e}}
	}
	p.Original.Generation = a.Administration.SessionGeneration(id)
	return a.Administration.ApplyObject(ctx, id, p)
}
func (a *administration) TableDesign(ctx context.Context, id int64, db, t string) (mysqlddl.Snapshot, error) {
	ctx, ectx := a.context(ctx, id)
	if ectx != nil {
		e := ectx
		return mysqlddl.Snapshot{}, e
	}

	v, e := a.Administration.TableDesign(ctx, id, db, t)
	v.RuntimeGeneration = a.generation
	return v, e
}
func (a *administration) ApplyTableDesign(ctx context.Context, id int64, p mysqlddl.Plan) mysqlsvc.DDLOutcome {
	ctx, ectx := a.context(ctx, id)
	if ectx != nil {
		e := ectx
		return mysqlsvc.DDLOutcome{Error: e}
	}

	if e := a.check(id, p.Original.RuntimeGeneration); e != nil {
		return mysqlsvc.DDLOutcome{Error: e}
	}
	p.Original.RuntimeGeneration = a.Administration.SessionGeneration(id)
	return a.Administration.ApplyTableDesign(ctx, id, p)
}
func (a *administration) BackupMySQL(ctx context.Context, id int64, r mysqlsvc.BackupRequest, w io.Writer) (backups.Manifest, error) {
	ctx, ectx := a.context(ctx, id)
	if ectx != nil {
		e := ectx
		return backups.Manifest{}, e
	}

	if e := a.check(id, r.Generation); e != nil {
		return backups.Manifest{}, e
	}
	r.Generation = a.Administration.SessionGeneration(id)
	return a.Administration.BackupMySQL(ctx, id, r, w)
}
func (a *administration) DumpMySQL(ctx context.Context, id int64, r mysqlsvc.BackupRequest, w io.Writer) error {
	ctx, ectx := a.context(ctx, id)
	if ectx != nil {
		e := ectx
		return e
	}

	if e := a.check(id, r.Generation); e != nil {
		return e
	}
	r.Generation = a.Administration.SessionGeneration(id)
	return a.Administration.DumpMySQL(ctx, id, r, w)
}
func (a *administration) PlanRestore(ctx context.Context, id int64, g uint64, db, policy string, r *backups.Reader) (mysqlsvc.RestorePlan, error) {
	ctx, ectx := a.context(ctx, id)
	if ectx != nil {
		e := ectx
		return mysqlsvc.RestorePlan{}, e
	}

	if e := a.check(id, g); e != nil {
		return mysqlsvc.RestorePlan{}, e
	}
	v, e := a.Administration.PlanRestore(ctx, id, a.Administration.SessionGeneration(id), db, policy, r)
	v.Generation = a.generation
	return v, e
}
func (a *administration) RestoreMySQL(ctx context.Context, p mysqlsvc.RestorePlan) mysqlsvc.DDLOutcome {
	ctx, ectx := a.context(ctx, a.id)
	if ectx != nil {
		e := ectx
		return mysqlsvc.DDLOutcome{Error: e}
	}

	if e := a.check(a.id, p.Generation); e != nil {
		return mysqlsvc.DDLOutcome{Error: e}
	}
	p.Generation = a.Administration.SessionGeneration(a.id)
	return a.Administration.RestoreMySQL(ctx, p)
}
