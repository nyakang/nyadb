package dataworkspace

import (
	"encoding/json"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"testing"
)

func TestSavedTableProfileSnapshotOwnsRulesAndRejectsWrongObject(t *testing.T) {
	s := New(Dependencies{})
	d := layoutTestTab()
	memoryLayoutPreferences(s, map[string]string{})
	d.object = contract.ObjectRef{ProfileID: d.profileID, Catalog: d.database, Name: d.table, Kind: "table"}
	d.filterColumn, d.filterMode, d.filterValue = "label", "equal", "original"
	if e := s.saveDataProfile(d, "Focused"); e != nil {
		t.Fatal(e)
	}
	options, e := s.SavedProfiles(d.object)
	if e != nil || len(options) != 1 {
		t.Fatalf("profiles: %v %v", options, e)
	}
	d.tools.profiles[0].Filters[0].Value = "changed"
	var captured savedProfile
	if e = json.Unmarshal([]byte(options[0].Value), &captured); e != nil || captured.Profile.Filters[0].Value != "original" {
		t.Fatal("snapshot shared mutable filters")
	}
	wrong := *d
	wrong.object.Name = "different"
	s.RequestProfile(ui.Services{}, &wrong, options[0].Value)
	if wrong.ruleError == "" || wrong.hostAction != "" {
		t.Fatal("wrong object accepted saved configuration")
	}
}
