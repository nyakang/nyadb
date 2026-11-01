package objectworkspace

type contextEntryScope struct {
	profile         int64
	generation      uint64
	database, table string
}

func (s *State) contextEntryValid(original *contextEntryScope, profile int64, database, table string) bool {
	w := s.sessionFor(profile)
	b := s.currentBrowser()
	return original != nil && *original == (contextEntryScope{profile, w.Generation, database, table}) && w.Connected && s.session().ProfileID == profile && b.ProfileID == profile && b.Database == database
}

// Capability checks are shared by the browser and its host's toolbar.
func (s *State) CanAccounts(profile int64) bool {
	if s.deps.AdministrationFor != nil && s.deps.AdministrationFor(profile) == nil {
		return false
	}
	return s.sessionFor(profile).Connected && s.capabilities(profile).DesignTables && s.deps.AccountLoader != nil && s.deps.AccountExecutor != nil && (s.accounts == nil || !s.accounts.busy)
}
func (s *State) CanBackup(profile int64) bool {
	return s.sessionFor(profile).Connected && s.capabilities(profile).Backup && s.backupRuntime != nil && s.backupRuntime() != nil && (s.backup == nil || !s.backup.busy)
}
