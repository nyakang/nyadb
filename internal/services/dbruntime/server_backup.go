package dbruntime

import "github.com/nyakang/nyadb/internal/db/contract"

func (d *taskDocument) SupportsServerBackup(action string) bool {
	if d.check() != nil {
		return false
	}
	p, ok := d.raw.(contract.ServerBackupLease)
	return ok && p.SupportsServerBackup(action)
}
func (d *taskDocument) PrepareServerBackup(action string) (contract.TaskMutationPlan, error) {
	if err := d.check(); err != nil {
		return contract.TaskMutationPlan{}, err
	}
	p, ok := d.raw.(contract.ServerBackupLease)
	if !ok {
		return contract.TaskMutationPlan{}, contract.ErrUnsupported
	}
	return p.PrepareServerBackup(action)
}
