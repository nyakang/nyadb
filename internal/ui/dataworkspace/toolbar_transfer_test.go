package dataworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func transferColumns() []mysqlsvc.MySQLStructureColumn {
	return []mysqlsvc.MySQLStructureColumn{{Name: "id", DataType: "bigint", Extra: "auto_increment"}, {Name: "note", DataType: "varchar", Nullable: true}, {Name: "payload", DataType: "blob", Nullable: true}}
}
func TestDataTransferDelimitedRoundTripPreservesNullBinaryAndQuotedValues(t *testing.T) {
	for _, format := range []string{"CSV", "TSV", "JSON"} {
		t.Run(format, func(t *testing.T) {
			fields := []string{"id", "note", "payload"}
			var out bytes.Buffer
			encoder, err := newMySQLDataTransferEncoder(&out, format, "db", "t", fields)
			if err != nil {
				t.Fatal(err)
			}
			rows := [][]mysqlsvc.MySQLDataValue{{{Text: "9007199254740993"}, {Text: "a,\"b\"\n第二行\t"}, {Binary: true, Bytes: []byte{0, 1, 255}}}, {{Text: "2"}, {Null: true}, {Null: true}}, {{Text: "3"}, {Text: `\N`}, {Binary: true, Bytes: []byte{}}}}
			for _, row := range rows {
				if err = encoder.Row(row); err != nil {
					t.Fatal(err)
				}
			}
			if err = encoder.Close(); err != nil {
				t.Fatal(err)
			}
			input, err := mysqlDataParseImport(bytes.NewReader(out.Bytes()), format)
			if err != nil {
				t.Fatal(err)
			}
			edits, err := mysqlDataImportEdits(input, input.Fields, transferColumns())
			if err != nil {
				t.Fatal(err)
			}
			for i, row := range rows {
				for j, field := range fields {
					got := edits[i].Values[field]
					if !reflect.DeepEqual(got, row[j]) && !(row[j].Binary && len(row[j].Bytes) == 0 && got.Binary && len(got.Bytes) == 0) {
						t.Fatalf("%s row %d: %+v != %+v", format, i, got, row[j])
					}
				}
			}
		})
	}
}
func TestDataTransferParserRejectsWholeFileInsteadOfTruncating(t *testing.T) {
	tests := []struct{ data, format string }{{"id,id\n1,2\n", "CSV"}, {"id,note\n1\n", "CSV"}, {"id\n" + strings.Repeat("1\n", mysqlsvc.MaxMySQLDataImportRows+1), "CSV"}, {"id\n" + strings.Repeat("x", mysqlsvc.MySQLDataEditMaxBytes+1), "CSV"}, {`[{"id":"1"},{"note":"2"}]`, "JSON"}, {`[{"id":{}}]`, "JSON"}, {`[{"id":"1","id":"2"}]`, "JSON"}, {`[{"id":"1"}] {}`, "JSON"}, {"id\n\xff\n", "CSV"}}
	for _, test := range tests {
		if _, err := mysqlDataParseImport(strings.NewReader(test.data), test.format); err == nil {
			t.Fatalf("accepted %s %.80q", test.format, test.data)
		}
	}
	input, err := mysqlDataParseImport(strings.NewReader("\ufeffid\r\n1\r\n"), "CSV")
	if err != nil || input.Fields[0] != "id" {
		t.Fatal(input, err)
	}
}
func TestDataTransferMappingRejectsUnsafeColumnsAndInvalidValues(t *testing.T) {
	columns := transferColumns()
	columns = append(columns, mysqlsvc.MySQLStructureColumn{Name: "generated", DataType: "int", Extra: "VIRTUAL GENERATED"})
	for _, test := range []struct {
		mapping []string
		row     []mysqlsvc.MySQLDataValue
	}{{[]string{"note", "note"}, []mysqlsvc.MySQLDataValue{{Text: "x"}, {Text: "y"}}}, {[]string{"generated"}, []mysqlsvc.MySQLDataValue{{Text: "1"}}}, {[]string{"id"}, []mysqlsvc.MySQLDataValue{{Null: true}}}, {[]string{"id"}, []mysqlsvc.MySQLDataValue{{Text: "1; DROP TABLE t"}}}, {[]string{"payload"}, []mysqlsvc.MySQLDataValue{{Text: "invalid"}}}} {
		fields := make([]string, len(test.mapping))
		if _, err := mysqlDataImportEdits(&mysqlDataImportInput{Fields: fields, Rows: [][]mysqlsvc.MySQLDataValue{test.row}}, test.mapping, columns); err == nil {
			t.Fatalf("accepted %+v", test)
		}
	}
}
func TestDataTransferAtomicFilePreservesExistingTargetOnErrorAndCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.csv")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("write failed")
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		err := mysqlDataAtomicFile(ctx, path, func(w io.Writer) error {
			_, _ = io.WriteString(w, "partial")
			if cancelled {
				cancel()
				return nil
			}
			return sentinel
		})
		cancel()
		if err == nil {
			t.Fatal("successful incomplete publish")
		}
		data, _ := os.ReadFile(path)
		if string(data) != "original" {
			t.Fatal("destroyed original file")
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatal("temporary file leaked")
	}
	if err := mysqlDataAtomicFile(context.Background(), path, func(w io.Writer) error { _, e := io.WriteString(w, "complete"); return e }); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "complete" {
		t.Fatal("replacement missing")
	}
}
func TestDataTransferExportSnapshotOwnsRowsAndRejectsTruncatedUnidentifiedRows(t *testing.T) {
	d := &Document{page: &mysqlsvc.MySQLDataPage{Columns: []string{"note"}, Rows: [][]mysqlsvc.MySQLDataCell{{{Text: "old"}}}}}
	values, _, _, err := mysqlDataExportSnapshot(d, []string{"note"}, false)
	if err != nil {
		t.Fatal(err)
	}
	d.page.Rows[0][0].Text = "changed"
	if values[0][0].Text != "old" {
		t.Fatal("export retained mutable page")
	}
	d.page.Rows[0][0].Truncated = true
	if _, _, _, err = mysqlDataExportSnapshot(d, []string{"note"}, false); err == nil {
		t.Fatal("exported truncated data without identity")
	}
}
func transferTestDocument() (*State, *Document, *Session) {
	session := &Session{ProfileID: 12, Generation: 2, Connected: true}
	s := New(Dependencies{Session: func() Session { return *session }})
	d := s.Open(1, 12, "db", "t")
	d.page = &mysqlsvc.MySQLDataPage{Columns: []string{"id"}, ColumnMetadata: transferColumns()[:1]}
	s.openDataImport(d)
	// These adapter tests construct already-reviewed legacy inputs directly.
	// The production stepped/spooled workflow has separate lifecycle tests.
	d.transfer.Wizard = false
	d.transfer.ContinueOnError = false
	d.transfer.Input = &mysqlDataImportInput{Fields: []string{"id"}, Rows: [][]mysqlsvc.MySQLDataValue{{{Text: "1"}}}}
	d.transfer.Mapping = []string{"id"}
	d.transfer.Confirmation = "t"
	return s, d, session
}
func TestDataTransferImportWaitsForCancelAcknowledgementAndNeverReplays(t *testing.T) {
	s, d, _ := transferTestDocument()
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	calls := 0
	s.Saver = func(ctx context.Context, _ int64, _, _ string, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		calls++
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil, context.Canceled
	}
	s.startDataImport(ui.Services{}, d)
	<-started
	s.stopDataTransfer(d)
	<-canceled
	s.consumeDataTransfers(ui.Services{})
	if !d.transfer.Busy || !d.transfer.Canceling {
		t.Fatal("cancellation settled before executor acknowledgement")
	}
	s.startDataImport(ui.Services{}, d)
	close(release)
	deadline := time.Now().Add(time.Second)
	for d.transfer.Busy && time.Now().Before(deadline) {
		s.consumeDataTransfers(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.transfer.Busy || !d.transfer.Submitted || calls != 1 {
		t.Fatal("cancel failed to settle or replayed")
	}
	s.startDataImport(ui.Services{}, d)
	if calls != 1 {
		t.Fatal("failed import replayed")
	}
}
func TestDataTransferParkedImportAndStaleGenerationStayBlocked(t *testing.T) {
	s, d, session := transferTestDocument()
	out := make(chan mysqlDataTransferResult, 1)
	d.transfer.Busy = true
	d.transfer.Submitted = true
	d.transfer.Results = out
	out <- mysqlDataTransferResult{Request: d.transfer.Request, Generation: 2, Profile: 12, Write: true, Rows: 1, Saved: &mysqlsvc.MySQLDataEditResult{Outcomes: []mysqlsvc.MySQLDataEditOutcome{{Kind: "insert"}}}}
	session.ProfileID = 13
	s.consumeDataTransfers(ui.Services{})
	if !d.transfer.Busy {
		t.Fatal("parked write applied")
	}
	session.ProfileID = 12
	session.Generation = 3
	s.consumeDataTransfers(ui.Services{})
	if d.transfer.Busy || !d.transfer.Uncertain || d.edit.Uncertain == nil {
		t.Fatal("stale write did not block replay")
	}
	canceled := false
	d.transfer.Cancel = func() { canceled = true }
	s.Close(d)
	s.Close(d)
	if !canceled || d.transfer.Results != nil || d.transfer.Input != nil {
		t.Fatal("close retained transfer worker or input")
	}
}
func TestDataTransferNativeWizardShowsMappingPreviewAndConfirmation(t *testing.T) {
	s, d, _ := transferTestDocument()
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.transferModals(c, d) }, 760, 650)
	if !tt.HasText("导入向导") || !tt.HasText("字段映射 · 1 行") || !tt.HasText("输入表名称以确认导入") || !tt.HasText("确认导入") {
		t.Fatal("native import wizard lacks review controls")
	}
}
func TestDataTransferSQLAndJSONKeepValuesOutOfSQLSyntax(t *testing.T) {
	value := mysqlsvc.MySQLDataValue{Text: "'); DROP TABLE t; --"}
	var out bytes.Buffer
	e, _ := newMySQLDataTransferEncoder(&out, "SQL", "d`b", "t", []string{"c`ol"})
	if err := e.Row([]mysqlsvc.MySQLDataValue{value}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), value.Text) || !strings.Contains(out.String(), "`d``b`.`t`") {
		t.Fatal("SQL syntax injection")
	}
	out.Reset()
	e, _ = newMySQLDataTransferEncoder(&out, "JSON", "db", "t", []string{"id"})
	_ = e.Row([]mysqlsvc.MySQLDataValue{{Text: "9007199254740993"}})
	_ = e.Close()
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || rows[0]["id"] != "9007199254740993" {
		t.Fatal("JSON lost integer spelling")
	}
}

func waitDataTransfer(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for d.transfer.Busy && time.Now().Before(deadline) {
		s.consumeDataTransfers(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.transfer.Busy {
		t.Fatal("transfer did not acknowledge completion")
	}
}
func TestDataTransferFullExportWorkerOwnsRulesAndStreamsEveryRow(t *testing.T) {
	s, d, _ := transferTestDocument()
	s.openDataExport(d)
	d.transfer.Scope = "all"
	path := filepath.Join(t.TempDir(), "all.json")
	d.transfer.Format = "JSON"
	d.transfer.SaveDialog = func(mygo.SaveDialogOptions) (string, error) { return path, nil }
	d.filterRules = []mysqlsvc.MySQLDataFilterRule{{Column: "id", Mode: "greater", Value: "0"}}
	started, release := make(chan struct{}), make(chan struct{})
	received := make(chan mysqlsvc.MySQLDataOptions, 1)
	s.Exporter = func(_ context.Context, _ int64, db, table string, options mysqlsvc.MySQLDataOptions, fields []string, emit func([]mysqlsvc.MySQLDataValue) error) error {
		close(started)
		<-release
		received <- options
		if db != "db" || table != "t" || !reflect.DeepEqual(fields, []string{"id"}) {
			t.Error("export target changed")
		}
		for i := 0; i < 301; i++ {
			if err := emit([]mysqlsvc.MySQLDataValue{{Text: fmt.Sprint(i)}}); err != nil {
				return err
			}
		}
		return nil
	}
	s.startDataExport(ui.Services{}, d)
	<-started
	d.filterRules[0].Value = "changed"
	d.transfer.Fields[0] = "changed"
	close(release)
	options := <-received
	if options.Filters[0].Value != "0" {
		t.Fatal("worker shared mutable filter rules")
	}
	waitDataTransfer(t, s, d)
	if d.transfer.Err != "" {
		t.Fatal(d.transfer.Err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]string
	if err = json.Unmarshal(data, &rows); err != nil || len(rows) != 301 {
		t.Fatal("full export silently stopped", len(rows), err)
	}
}
func TestDataTransferFileReadPreviewAndExplicitConfirmationPrecedeSingleSave(t *testing.T) {
	s, d, _ := transferTestDocument()
	s.openDataImport(d)
	// Exercise the compatible bounded-file/saver adapter. Production defaults
	// to continue-on-error and uses the independently tested streaming wizard.
	d.transfer.Wizard, d.transfer.ContinueOnError = false, false
	path := filepath.Join(t.TempDir(), "fixture.csv")
	if err := os.WriteFile(path, []byte("id\n9007199254740993\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d.transfer.OpenDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{path}, nil }
	saved := make(chan []mysqlsvc.MySQLDataEdit, 1)
	s.Saver = func(_ context.Context, _ int64, _, _ string, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		saved <- edits
		return &mysqlsvc.MySQLDataEditResult{Outcomes: []mysqlsvc.MySQLDataEditOutcome{{Kind: "insert"}}}, nil
	}
	s.chooseDataImportFile(ui.Services{}, d)
	waitDataTransfer(t, s, d)
	if d.transfer.Input == nil || d.transfer.Mapping[0] != "id" || d.transfer.Input.Rows[0][0].Text != "9007199254740993" {
		t.Fatal("preview did not preserve fixture")
	}
	s.startDataImport(ui.Services{}, d)
	select {
	case <-saved:
		t.Fatal("saved without explicit confirmation")
	default:
	}
	d.transfer.Confirmation = "t"
	s.startDataImport(ui.Services{}, d)
	var edits []mysqlsvc.MySQLDataEdit
	select {
	case edits = <-saved:
	case <-time.After(time.Second):
		t.Fatal("reviewed adapter did not start", d.transfer.Err)
	}
	if edits[0].Values["id"].Text != "9007199254740993" {
		t.Fatal("save values changed")
	}
	waitDataTransfer(t, s, d)
	if !d.transfer.Submitted || d.transfer.Open || d.transfer.Uncertain {
		t.Fatal("acknowledged import did not settle")
	}
}

func TestDataTransferUncertainImportLeaveHasCancelAndVerifiedCloseWithoutReplay(t *testing.T) {
	s, d, _ := transferTestDocument()
	defer s.Close(d)
	input := d.transfer.Input
	d.transfer.Uncertain, d.transfer.Submitted = true, true
	d.edit.Uncertain = &mysqlsvc.MySQLDataEditResult{CommitUncertain: true}
	calls := 0
	s.Saver = func(context.Context, int64, string, string, []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		calls++
		return nil, nil
	}
	continued := false
	continuation := func(ui.Services) { continued = true; s.Close(d) }
	if !s.GuardTransactionLeave(d, continuation) || !d.transfer.LeaveOpen || d.transfer.LeaveNext == nil {
		t.Fatal("uncertain import trapped close without a decision")
	}
	s.resolveDataTransferLeave(ui.Services{}, d, false)
	if continued || !d.transfer.Uncertain || d.transfer.Input != input || d.edit.Uncertain == nil || d.transfer.LeaveOpen {
		t.Fatal("cancel lost uncertain import state")
	}
	s.startDataImport(ui.Services{}, d)
	if calls != 0 {
		t.Fatal("uncertain import replayed after cancel")
	}
	if !s.GuardTransactionLeave(d, continuation) || !d.transfer.LeaveOpen {
		t.Fatal("second close lost verification decision")
	}
	s.resolveDataTransferLeave(ui.Services{}, d, true)
	if !continued || !d.closed || d.transfer.Input != nil || d.edit.Uncertain != nil || calls != 0 {
		t.Fatal("verified close retained uncertain input, replayed or failed to continue")
	}
}
