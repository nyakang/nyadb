package shell

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func (s *State) SetBackupFiles(files objectworkspace.BackupFiles) {
	s.objectWorkspace.SetBackupServices(func() objectworkspace.BackupRuntime {
		if s.sessions.Manager == nil {
			return nil
		}
		return s.sessions.Manager
	}, files)
}
func (s *State) objectWorkspaceBackup(services ui.Services, id int64, db, name, operation string) {
	s.objectWorkspace.BeginBackup(services, id, db, name, operation)
}
