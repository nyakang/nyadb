package objectworkspace

import "github.com/nyakang/nyadb/internal/db/contract"

func serverBackupAction(action string) bool { return action == "dump" || action == "snapshot" }
func (s *State) CanCreateServerBackup(d *Document, action string) bool {
	if !s.taskReady(d) || d.object.Catalog != "" || d.tasks.management.uncertain || d.tasks.management.receipt.Raw != "" {
		return false
	}
	p, ok := d.tasks.lease.(contract.ServerBackupLease)
	m, managed := d.tasks.lease.(contract.TaskManagementLease)
	return ok && managed && m.SupportsTaskManagement() && p.SupportsServerBackup(action)
}
func (s *State) PrepareServerBackup(d *Document, action string) error {
	if !s.CanCreateServerBackup(d, action) {
		return contract.ErrUnsupported
	}
	p, err := d.tasks.lease.(contract.ServerBackupLease).PrepareServerBackup(action)
	if err != nil {
		d.err = err.Error()
		return err
	}
	d.tasks.management.plan = &p
	d.tasks.management.preview = true
	d.tasks.management.acknowledged = false
	d.err = ""
	return nil
}
func (s *State) canSubmitTaskPlan(d *Document) bool {
	if d == nil || d.tasks == nil || d.tasks.management.plan == nil {
		return false
	}
	if serverBackupAction(d.tasks.management.plan.Action) {
		return s.CanCreateServerBackup(d, d.tasks.management.plan.Action)
	}
	return s.CanManageTasks(d)
}
