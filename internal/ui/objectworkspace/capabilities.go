package objectworkspace

import "github.com/nyakang/nyadb/internal/db/contract"

func (s *State) capabilities(id int64) contract.Capabilities {
	if s.deps.Capabilities != nil {
		return s.deps.Capabilities(id)
	}
	return contract.Capabilities{DesignTables: true, DesignViews: true, Objects: true, Maintenance: true, Accounts: true, Restore: true, Import: true, Backup: true, EditableTables: true}
}

func (s *State) canAdministration(id int64) bool {
	return s.capabilities(id).Objects && (s.deps.AdministrationFor == nil || s.deps.AdministrationFor(id) != nil)
}

func (s *State) operationAvailable(operation string, target contract.SchemaDesignTarget) bool {
	return s.deps.Availability == nil || s.deps.Availability(target.Object.ProfileID, operation, target).Enabled
}
