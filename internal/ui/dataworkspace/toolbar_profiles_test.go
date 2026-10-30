package dataworkspace

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestDataProfilesPersistQualifiedSettingsWithoutRowsAndKeepLayoutCompatibility(t *testing.T) {
	s := New(Dependencies{})
	d := layoutTestTab()
	saved := map[string]string{}
	memoryLayoutPreferences(s, saved)
	s.restoreMySQLDataLayout(d)
	d.grid.Columns = ui.TableLayout{Order: []string{"label", "id"}, Widths: map[string]float32{"label": 280}}
	d.hiddenColumns = map[string]bool{"id": true}
	d.filterColumn, d.filterMode, d.filterValue = "label", "equal", "chosen"
	d.sortColumn = "id"
	d.limit = 50
	d.page.Rows[0][0].Text = "private row content"
	s.flushMySQLDataLayout(d)
	legacy := saved[mysqlDataLayoutKey(d)]
	if err := s.saveDataProfile(d, "Focused"); err != nil {
		t.Fatal(err)
	}
	if saved[mysqlDataLayoutKey(d)] != legacy || len(saved) != 2 ||
		strings.Contains(saved[mysqlDataProfilesKey(d)], "private row content") || d.tools.activeProfile != "Focused" {
		t.Fatal("named settings overwrote the layout format or retained result rows")
	}
	d.grid.Columns.Widths["label"] = 90
	d.filterValue = "changed"
	if d.tools.profiles[0].Layout.Widths["label"] != 280 || d.tools.profiles[0].Filters[0].Value != "chosen" {
		t.Fatal("saved settings shared mutable document state")
	}
	restarted := New(Dependencies{})
	memoryLayoutPreferences(restarted, saved)
	reopened := layoutTestTab()
	restarted.loadDataProfiles(reopened)
	if len(reopened.tools.profiles) != 1 || reopened.tools.profiles[0].Limit != 50 {
		t.Fatal("named settings did not survive a new state")
	}
	other := layoutTestTab()
	other.profileID++
	restarted.loadDataProfiles(other)
	if len(other.tools.profiles) != 0 {
		t.Fatal("named settings crossed the original profile identity")
	}
}

func TestDataProfilesGuardApplyOwnSettingsAndRejectRemovedRuleFields(t *testing.T) {
	s := New(Dependencies{})
	d := layoutTestTab()
	memoryLayoutPreferences(s, map[string]string{})
	d.filterColumn, d.filterMode, d.filterValue = "label", "equal", "chosen"
	if err := s.saveDataProfile(d, "Focused"); err != nil {
		t.Fatal(err)
	}
	p := d.tools.profiles[0]
	p.Layout.Widths = map[string]float32{"label": 271}
	d.offset = 100
	d.edit.Drafts = map[string]*mysqlRowDraft{"row": {Kind: "update", Values: map[string]mysqlsvc.MySQLDataValue{"label": {Text: "draft"}}}}
	page := d.page
	s.applyDataProfile(ui.Services{}, d, p)
	if !d.edit.LeaveOpen || d.page != page || d.offset != 100 || d.edit.LeaveNext == nil {
		t.Fatal("applying settings bypassed the unsaved-document guard")
	}
	p.Layout.Widths["label"] = 90
	p.Filters[0].Value = "mutated while save pending"
	next := d.edit.LeaveNext
	d.edit.Drafts, d.edit.LeaveNext, d.edit.LeaveOpen = nil, nil, false
	next(ui.Services{})
	if d.grid.Columns.Widths["label"] != 271 || len(d.filterRules) != 1 || d.filterRules[0].Value != "chosen" || d.offset != 0 {
		t.Fatal("guarded settings shared mutable profile maps or did not reset paging")
	}
	d = layoutTestTab()
	p.Filters = []mysqlsvc.MySQLDataFilterRule{{Column: "removed", Mode: "equal", Value: "x"}}
	before := d.grid.Columns
	s.applyDataProfile(ui.Services{}, d, p)
	if d.ruleError == "" || d.page == nil || !reflect.DeepEqual(d.grid.Columns, before) {
		t.Fatal("missing schema field partially applied settings")
	}
}

func TestDataProfilesWriteFailuresKeepActiveProfileAndStoredList(t *testing.T) {
	s := New(Dependencies{})
	d := layoutTestTab()
	memoryLayoutPreferences(s, map[string]string{})
	if err := s.saveDataProfile(d, "One"); err != nil {
		t.Fatal(err)
	}
	s.SetDataLayoutPreferences(nil, func(string, string) error { return errors.New("unavailable") })
	if s.renameDataProfile(d, "One", "Two") == nil || d.tools.activeProfile != "One" || d.tools.profiles[0].Name != "One" {
		t.Fatal("failed rename published state before persistence")
	}
	if s.deleteDataProfile(d, "One") == nil || len(d.tools.profiles) != 1 || d.tools.activeProfile != "One" {
		t.Fatal("failed deletion lost the active profile")
	}
	memoryLayoutPreferences(s, map[string]string{})
	if err := s.renameDataProfile(d, "One", "Two"); err != nil || d.tools.activeProfile != "Two" {
		t.Fatal("successful rename did not preserve the active association")
	}
	if err := s.deleteDataProfile(d, "Two"); err != nil || d.tools.activeProfile != "" || len(d.tools.profiles) != 0 {
		t.Fatal("deleted profile retained its active association")
	}
}

func TestDataProfilesRejectMalformedPreferencesAndBoundNamesAndCount(t *testing.T) {
	for _, value := range []string{`{"version":9}`, `{"version":1,"items":[{"name":"bad","layout":{"version":1},"limit":100,"filters":[{"Column":"id","Mode":"raw_sql"}]}]}`} {
		s := New(Dependencies{})
		d := layoutTestTab()
		memoryLayoutPreferences(s, map[string]string{mysqlDataProfilesKey(d): value})
		s.loadDataProfiles(d)
		if d.tools.profilesErr == "" || s.saveDataProfile(d, "Overwrite") == nil {
			t.Fatal("malformed stored settings could be silently overwritten")
		}
	}
	s := New(Dependencies{})
	d := layoutTestTab()
	memoryLayoutPreferences(s, map[string]string{})
	if s.saveDataProfile(d, strings.Repeat("名", 81)) == nil {
		t.Fatal("overlong profile name was accepted")
	}
	for i := range mysqlDataMaxProfiles {
		if err := s.saveDataProfile(d, strings.Repeat("x", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if s.saveDataProfile(d, "overflow") == nil {
		t.Fatal("unbounded profile collection")
	}
}
