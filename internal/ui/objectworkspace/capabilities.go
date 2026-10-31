package objectworkspace

import "github.com/nyakang/nyadb/internal/db/contract"

func (s *State) capabilities(id int64) contract.Capabilities {
	if s.deps.Capabilities != nil {
		return s.deps.Capabilities(id)
	}
	return contract.Capabilities{DesignTables: true, Import: true, Backup: true, EditableTables: true}
}
