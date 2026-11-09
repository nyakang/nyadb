package dataworkspace

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func TestDataOmittedInsertLocaleChangesPreserveExplicitValuesAndDraftEvidence(t *testing.T) {
	s, d, _ := mutationWorkspace(t)
	d.boundRuntime.InsertOmittedValues = true
	d.page.ColumnMetadata[0].Generated = true
	s.addMySQLDataRecord(ui.Services{}, d)
	s.finishMySQLCellEdit(d, false)
	s.setMySQLDataCell(ui.Services{}, d, 3, 2, contract.DataValue{Kind: contract.Text, Text: "未设置"})
	before := captureDraft(d)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"en", "zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, form := range []bool{false, true} {
			d.recordFormMode = form
			tt := ui.NewTester(func(c *ui.Context) { s.View(c, d) }, 1280, 800)
			tt.Frame()
			if !tt.HasText(locales.Text("提交后生成")) || !tt.HasText(locales.Text("未设置")) || !tt.HasText("未设置") {
				t.Fatal("insert placeholders or explicit property changed language incorrectly", language, form)
			}
		}
		if !reflect.DeepEqual(before, captureDraft(d)) {
			t.Fatal("display locale changed original draft/page evidence")
		}
	}
}

func TestDataOmittedInsertKeepsGeneratedAndAbsentFieldsOutOfRequest(t *testing.T) {
	s, d, _ := mutationWorkspace(t)
	d.boundRuntime.InsertOmittedValues = true
	d.boundRuntime.EditValueKinds = []contract.CellKind{contract.Null, contract.Text, contract.Integer, contract.Binary}
	d.page.ColumnMetadata[0].Generated = true
	d.page.ColumnMetadata[1].ValueKind = contract.Integer
	d.page.ColumnMetadata[1].DataType = "bigint"
	s.addMySQLDataRecord(ui.Services{}, d)
	draft := mysqlDataDraftForRow(d, 3)
	if draft == nil || len(draft.Values) != 0 || d.page.Rows[3][0].Text != locales.Text("提交后生成") || d.page.Rows[3][1].Text != locales.Text("未设置") || mysqlDataCanEdit(d, 3, 0) {
		t.Fatal("insert invented SQL defaults or writable generated fields")
	}
	if d.edit.Editor == nil || d.edit.Editor.Kind != typedKindLabel(contract.Integer) {
		t.Fatal("absent scalar did not select its native editor kind")
	}
	d.edit.Editor.Text = "9223372036854775807"
	s.finishMySQLCellEdit(d, true)
	value := draft.Values["note"]
	if value.Default || value.Null || value.Kind != contract.Integer || value.Text != "9223372036854775807" || len(draft.Values) != 1 {
		t.Fatal("typed input became NULL/default or included missing fields")
	}
	s.setMySQLDataCell(ui.Services{}, d, 3, 2, contract.DataValue{Kind: contract.Text, Text: ""})
	if value, ok := draft.Values["extra"]; !ok || value.Null || value.Default || value.Kind != contract.Text || value.Text != "" {
		t.Fatal("explicit empty string became omitted")
	}
	s.setMySQLDataCell(ui.Services{}, d, 3, 2, contract.DataValue{Null: true, Kind: contract.Null})
	if value := draft.Values["extra"]; !value.Null || value.Default {
		t.Fatal("explicit NULL became omitted/default")
	}
}
func TestDataOmittedPropertylessInsertNeedsNoEditableColumn(t *testing.T) {
	s, d, _ := mutationWorkspace(t)
	d.boundRuntime.InsertOmittedValues = true
	for i := range d.page.ColumnMetadata {
		d.page.ColumnMetadata[i].Generated = true
	}
	s.addMySQLDataRecord(ui.Services{}, d)
	if d.edit.Editor != nil || mysqlDataDraftForRow(d, 3) == nil || len(mysqlDataDraftForRow(d, 3).Values) != 0 || !s.CanSave(d) {
		t.Fatal("propertyless node cannot be staged without a writable field")
	}
}

func TestDataRetiredNodeDocumentCannotStageNewEntityMutations(t *testing.T) {
	s, d, _ := mutationWorkspace(t)
	d.boundRuntime.InsertOmittedValues = true
	d.readRetired = true
	rows := len(d.page.Rows)
	s.addMySQLDataRecord(ui.Services{}, d)
	s.deleteMySQLDataRecords(ui.Services{}, d, 0)
	s.EditCell(ui.Services{}, d, 0, "note", contract.Cell{Kind: contract.Text, Text: "unsafe retired edit"})
	if len(d.page.Rows) != rows || d.HasDraft() || d.deleteChoice != nil || mysqlDataCanEdit(d, 0, 1) {
		t.Fatal("retired original range admitted entity/property editing")
	}
}
