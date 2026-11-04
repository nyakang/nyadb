package objectworkspace

import "github.com/nyakang/nyadb/internal/db/contract"

type contextEntryScope struct {
	profile         int64
	generation      uint64
	database, table string
	target          contract.SchemaDesignTarget
}

func (s *State) contextTarget(profile int64, database, name string) contract.SchemaDesignTarget {
	if s.deps.ViewTarget != nil {
		return s.deps.ViewTarget(profile, database, name)
	}
	if s.deps.SchemaTarget != nil {
		return s.deps.SchemaTarget(profile, database, name)
	}
	return contract.SchemaDesignTarget{}
}
func (s *State) contextScope(profile int64, database, name string) contextEntryScope {
	return contextEntryScope{profile: profile, generation: s.sessionFor(profile).Generation, database: database, table: name, target: s.contextTarget(profile, database, name)}
}

func (s *State) contextEntryValid(original *contextEntryScope, profile int64, database, table string) bool {
	w := s.sessionFor(profile)
	b := s.currentBrowser()
	return original != nil && *original == s.contextScope(profile, database, table) && w.Connected && s.session().ProfileID == profile && b.ProfileID == profile && b.Database == database
}

// Capability checks are shared by the browser and its host's toolbar.
func (s *State) CanAccounts(profile int64) bool {
	if s.deps.AdministrationFor != nil && s.deps.AdministrationFor(profile) == nil {
		return false
	}
	return s.sessionFor(profile).Connected && s.capabilities(profile).Accounts && s.deps.AccountLoader != nil && s.deps.AccountExecutor != nil && (s.accounts == nil || !s.accounts.busy)
}
func (s *State) CanBackup(profile int64) bool {
	if s.deps.BackupsFor != nil && s.deps.BackupsFor(profile).Plan != nil {
		return s.sessionFor(profile).Connected && s.capabilities(profile).Backup && (s.portableBackup == nil || !s.portableBackup.busy)
	}
	return s.sessionFor(profile).Connected && s.capabilities(profile).Backup && s.backupRuntime != nil && s.backupRuntime() != nil && (s.backup == nil || !s.backup.busy)
}

func (s *State) CanRestore(profile int64) bool {
	if !s.sessionFor(profile).Connected || !s.capabilities(profile).Restore {
		return false
	}
	if s.deps.BackupsFor != nil && s.deps.BackupsFor(profile).Plan != nil {
		return s.portableBackup == nil || !s.portableBackup.busy
	}
	return s.backupRuntime != nil && s.backupRuntime() != nil && (s.backup == nil || !s.backup.busy)
}
