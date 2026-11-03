package objectworkspace

import "github.com/nyakang/nyadb/internal/db/contract"

func (s *State) capabilities(id int64) contract.Capabilities {
	if s.deps.Capabilities != nil {
		return s.deps.Capabilities(id)
	}
	return contract.Capabilities{DesignTables: true, Import: true, Backup: true, EditableTables: true}
}

func (s *State) canAdministration(id int64) bool {
	return s.capabilities(id).DesignTables && (s.deps.AdministrationFor == nil || s.deps.AdministrationFor(id) != nil)
}
