package dataworkspace

import (
	"encoding/json"
	"github.com/egoist/mygo/ui"
)

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
	if d.graphTransferOnly && !s.canUseSession(d) {
		return
	}
	if action != "import" && action != "export" && action != "profile" && action != "saved_profile" {
		return
	}
	if s.GuardTransactionLeave(d, func(services ui.Services) { s.RequestAction(services, d, action) }) || s.GuardLeave(d, func(services ui.Services) { s.RequestAction(services, d, action) }) {
		return
	}
	d.hostAction, d.hostActionGeneration = action, w.Generation
	if d.page == nil && !d.importOnly && !d.graphTransferOnly && !d.loading {
		s.Load(services, d, false)
	}
	s.consumeHostActions(services)
}
func (s *State) consumeHostActions(available ...ui.Services) {
	services := ui.Services{}
	if len(available) > 0 {
		services = available[0]
	}
	w := s.session()
	for _, d := range s.documents {
		if d.hostAction == "" || d.closed || d.profileID != w.ProfileID {
			continue
		}
		if !w.Connected || w.Generation != d.hostActionGeneration || d.err != "" {
			d.hostAction = ""
			d.hostProfile = ""
			continue
		}
		if d.loading || d.page == nil && !d.importOnly && !d.graphTransferOnly {
			continue
		}
		action := d.hostAction
		d.hostAction = ""
		switch action {
		case "saved_profile":
			value := d.hostProfile
			d.hostProfile = ""
			if s.GuardTransactionLeave(d, func(next ui.Services) { s.RequestProfile(next, d, value) }) {
				continue
			}
			var owned savedProfile
			if json.Unmarshal([]byte(value), &owned) == nil && owned.Object == d.object {
				s.applyDataProfile(services, d, owned.Profile)
			}
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
	d := &Document{importDialect: s.runtimeFor(&Document{profileID: profile}).ImportDialect, id: id, profileID: profile, database: database, importOnly: true, objectKind: "import", selectedRow: -1, hiddenColumns: map[string]bool{}}
	s.documents[id] = d
	s.openDataImport(d)
	return d
}
