package dataworkspace

import (
	"encoding/json"
	"errors"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

// SavedProfile is an immutable, qualified presentation snapshot for host menus.
type SavedProfile struct{ Name, Value string }
type savedProfile struct {
	Object  contract.ObjectRef
	Profile mysqlDataTableProfile
}

func (s *State) SavedProfiles(o contract.ObjectRef) ([]SavedProfile, error) {
	ns := o.Schema
	if ns == "" {
		ns = o.Catalog
	}
	d := &Document{object: o, profileID: o.ProfileID, database: ns, table: o.Name}
	s.loadDataProfiles(d)
	if d.tools.profilesErr != "" {
		return nil, errors.New(d.tools.profilesErr)
	}
	var out []SavedProfile
	for _, p := range d.tools.profiles {
		raw, e := json.Marshal(savedProfile{o, p})
		if e != nil {
			return nil, e
		}
		out = append(out, SavedProfile{p.Name, string(raw)})
	}
	return out, nil
}
func (s *State) RequestProfile(services ui.Services, d *Document, value string) {
	if d == nil || d.closed {
		return
	}
	var owned savedProfile
	if json.Unmarshal([]byte(value), &owned) != nil || owned.Object != d.object || !validMySQLDataProfile(owned.Profile) {
		d.ruleError = locales.Text("表配置文件格式无效")
		return
	}
	d.hostProfile = value
	s.RequestAction(services, d, "saved_profile")
}
