package dataworkspace

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestDataTransferEmptyAsNullPreservesSourceBytesWhitespaceAndNullability(t *testing.T) {
	columns := append(transferColumns()[1:], mysqlsvc.MySQLStructureColumn{Name: "amount", DataType: "decimal", Nullable: true})
	input := &mysqlDataImportInput{Fields: []string{"note", "payload", "amount"}, Rows: [][]mysqlsvc.MySQLDataValue{
		{{Text: ""}, {Binary: true, Bytes: []byte{}}, {Text: ""}},
		{{Text: " \t"}, {Null: true}, {Text: "0.00"}},
	}}
	if _, err := mysqlDataImportEdits(input, input.Fields, columns); err == nil {
		t.Fatal("default import silently coerced empty numeric text")
	}
	edits, err := mysqlDataImportEditsWithOptions(input, input.Fields, columns, true)
	if err != nil {
		t.Fatal(err)
	}
	if !edits[0].Values["note"].Null || !edits[0].Values["amount"].Null || !edits[0].Values["payload"].Binary || edits[0].Values["payload"].Null || edits[1].Values["note"].Text != " \t" {
		t.Fatal("empty conversion changed bytes or whitespace", edits)
	}
	if input.Rows[0][0].Null || input.Rows[0][2].Null {
		t.Fatal("choice rewrote the parsed input")
	}
	columns[0].Nullable = false
	if _, err := mysqlDataImportEditsWithOptions(input, input.Fields, columns, true); err == nil {
		t.Fatal("conversion bypassed NOT NULL validation")
	}
}

func TestDataTransferNativeEmptyChoiceRestoresPreviewAndShowsBinaryBytes(t *testing.T) {
	s, d, _ := transferTestDocument()
	defer s.Close(d)
	d.page.Columns = []string{"note", "payload"}
	d.page.ColumnMetadata = transferColumns()[1:]
	d.transfer.Format = "XML"
	d.transfer.Input = &mysqlDataImportInput{Fields: d.page.Columns, Rows: [][]mysqlsvc.MySQLDataValue{{{Text: ""}, {Binary: true, Bytes: []byte{0, 255}}}}}
	d.transfer.Mapping = []string{"note", "payload"}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.transferModals(c, d) }, 900, 900)
	if d.transfer.EmptyAsNull || !tt.HasText("1  note= · payload=0x00ff") {
		t.Fatal("default empty text or binary preview changed")
	}
	if err := tt.Click("使用 NULL 取代空字符串"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !d.transfer.EmptyAsNull || !tt.HasText("1  note=NULL · payload=0x00ff") {
		t.Fatal("preview does not reflect explicit conversion")
	}
	if err := tt.Click("使用 NULL 取代空字符串"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.transfer.EmptyAsNull || d.transfer.Input.Rows[0][0].Null || !tt.HasText("1  note= · payload=0x00ff") {
		t.Fatal("disabling conversion did not restore original source preview")
	}
}

func TestDataTransferImportWorkerOwnsConvertedEmptyChoice(t *testing.T) {
	s, d, _ := transferTestDocument()
	defer s.Close(d)
	d.page.ColumnMetadata = transferColumns()[1:2]
	d.transfer.Input = &mysqlDataImportInput{Fields: []string{"note"}, Rows: [][]mysqlsvc.MySQLDataValue{{{Text: ""}}}}
	d.transfer.Mapping = []string{"note"}
	d.transfer.EmptyAsNull = true
	started, release := make(chan struct{}), make(chan struct{})
	received := make(chan []mysqlsvc.MySQLDataEdit, 1)
	s.Saver = func(_ context.Context, _ int64, _, _ string, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		close(started)
		<-release
		received <- edits
		return nil, errors.New("fixture stops before database write")
	}
	s.startDataImport(ui.Services{}, d)
	<-started
	d.transfer.EmptyAsNull = false
	d.transfer.Input.Rows[0][0].Text = "changed later"
	close(release)
	edits := <-received
	if !reflect.DeepEqual(edits[0].Values["note"], mysqlsvc.MySQLDataValue{Null: true}) {
		t.Fatal("worker observed later UI choice or source mutations", edits)
	}
	waitDataTransfer(t, s, d)
}
