package dataworkspace

import "github.com/egoist/mygo/ui"

// RequestAction retains the original data document while an initial metadata
// read finishes. A later profile generation cannot open an old table's wizard.
func (s *State) RequestAction(services ui.Services, d *Document, action string) {
	if d == nil || d.closed {
		return
	}
	w := s.session()
	if !w.Connected || w.ProfileID != d.profileID {
		return
	}
	if action != "import" && action != "export" && action != "profile" {
		return
	}
	if s.GuardTransactionLeave(d, func(services ui.Services) { s.RequestAction(services, d, action) }) || s.GuardLeave(d, func(services ui.Services) { s.RequestAction(services, d, action) }) {
		return
	}
	d.hostAction, d.hostActionGeneration = action, w.Generation
	if d.page == nil && !d.importOnly && !d.loading {
		s.Load(services, d, false)
	}
	s.consumeHostActions()
}
func (s *State) consumeHostActions() {
	w := s.session()
	for _, d := range s.documents {
		if d.hostAction == "" || d.closed || d.profileID != w.ProfileID {
			continue
		}
		if !w.Connected || w.Generation != d.hostActionGeneration || d.err != "" {
			d.hostAction = ""
			continue
		}
		if d.loading || d.page == nil && !d.importOnly {
			continue
		}
		action := d.hostAction
		d.hostAction = ""
		switch action {
		case "import":
			s.openDataImport(d)
		case "export":
			s.openDataExport(d)
		case "profile":
			s.loadDataProfiles(d)
			d.tools.profileOpen = true
			d.tools.profileSave = false
			d.tools.profileSearch = ""
			if len(d.tools.profiles) > 0 {
				d.tools.profileChoice = d.tools.profiles[0].Name
				d.tools.profileName = d.tools.profileChoice
			}
		}
	}
}

// OpenImport owns an import-only document with no fabricated table or rows.
// Source inference and new-table creation use the existing import executor.
func (s *State) OpenImport(id uint64, profile int64, database string) *Document {
	if id == 0 || profile <= 0 || database == "" {
		return nil
	}
	if old := s.documents[id]; old != nil {
		s.Close(old)
	}
	d := &Document{id: id, profileID: profile, database: database, importOnly: true, objectKind: "import", selectedRow: -1, hiddenColumns: map[string]bool{}}
	s.documents[id] = d
	s.openDataImport(d)
	return d
}
