package dataworkspace

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestOriginalPagePermissionsPreserveDefaultsAndCannotWidenRuntime(t *testing.T) {
	s := New(Dependencies{})
	d := s.Open(1, 1, "original", "records")
	defer s.CloseAll()
	names := []string{"id", "note", "qty"}
	d.page = &contract.DataPage{Columns: names}
	for _, name := range names {
		d.columns = append(d.columns, ui.TableColumn{ID: name, Title: name})
	}
	if !slices.Equal(s.filterColumns(d, names), names) || !s.sortable(d, "note") {
		t.Fatal("nil permissions changed existing providers")
	}
	d.boundRuntime = &Runtime{FilterColumns: []string{"id", "qty"}, SortColumns: []string{"id"}}
	d.page.FilterColumns = []string{"id", "note"}
	d.page.SortColumns = []string{"id", "note"}
	if !slices.Equal(s.filterColumns(d, names), []string{"id"}) || s.sortable(d, "note") {
		t.Fatal("page widened original runtime capabilities")
	}
	if dataRuleProblem(d, contract.DataFilterRule{Column: "note", Mode: "equal"}) == "" || dataRuleProblem(d, contract.DataFilterRule{Column: "qty", Mode: "equal"}) == "" {
		t.Fatal("a forbidden rule became active")
	}
	d.boundRuntime = &Runtime{}
	d.page.FilterColumns = []string{}
	d.page.SortColumns = []string{}
	if len(s.filterColumns(d, names)) != 0 || s.sortable(d, "id") {
		t.Fatal("empty original permissions became unrestricted")
	}
	// Publication owns its permission slices and preserves empty-vs-absent.
	original := []string{"id"}
	owned := appendOptionalColumns(original)
	original[0] = "note"
	if owned[0] != "id" || appendOptionalColumns([]string{}) == nil || appendOptionalColumns(nil) != nil {
		t.Fatal("publication lost permission ownership")
	}
}
