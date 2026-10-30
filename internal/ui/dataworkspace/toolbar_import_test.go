package dataworkspace

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestDataImportInferenceUsesAllRowsAndKeepsExactDecimalPrecision(t *testing.T) {
	input := &mysqlDataImportInput{Fields: []string{"id", "amount", "large", "note", "payload", "when", "date", "null"}}
	for range 6 {
		input.Rows = append(input.Rows, []mysqlsvc.MySQLDataValue{{Text: "1"}, {Text: "9007199254740993.2500"}, {Text: "18446744073709551615"}, {Text: "short"}, {Binary: true, Bytes: []byte{0, 255}}, {Text: "2026-10-30 12:34:56.123456"}, {Text: "2026-10-30"}, {Null: true}})
	}
	input.Rows[5][3].Text = strings.Repeat("中", 300)
	columns := mysqlDataInferImportColumns(input)
	for i, kind := range []string{"BIGINT", "DECIMAL(20,4)", "DECIMAL(20,0)", "TEXT", "LONGBLOB", "DATETIME(6)", "DATE", "VARCHAR(255)"} {
		if columns[i].Name != input.Fields[i] || columns[i].Type != kind || !columns[i].Nullable || columns[i].PrimaryKey {
			t.Fatalf("field %d: %+v", i, columns[i])
		}
	}
	for _, value := range []string{"1e1000", "0.0000000000000000000000000000001", "2026-10-30 12:34:56.123456789", "123456789012345678901234567890123456789012345678901234567890123456789"} {
		got := mysqlDataInferImportColumns(&mysqlDataImportInput{Fields: []string{"value"}, Rows: [][]mysqlsvc.MySQLDataValue{{{Text: value}}}})
		if got[0].Type != "VARCHAR(255)" {
			t.Fatal("inferred a lossy type", value, got)
		}
	}
}

func newTableImportTestDocument() (*State, *Document, *Session) {
	s, d, session := transferTestDocument()
	d.transfer.Target = "new"
	d.transfer.NewName = "new_table"
	d.transfer.Confirmation = "new_table"
	d.transfer.Input = &mysqlDataImportInput{Fields: []string{"id", "note", "amount"}, Rows: [][]mysqlsvc.MySQLDataValue{{{Text: "1"}, {Text: "中文"}, {Text: "12.50"}}}}
	d.transfer.Mapping = []string{"id", "", ""}
	d.transfer.NewColumns = mysqlDataInferImportColumns(d.transfer.Input)
	d.transfer.NewColumns[0].PrimaryKey = true
	d.transfer.NewColumns[0].Nullable = false
	return s, d, session
}

func TestDataImportNewTableMappingReviewsTypesNamesAndRequiredValues(t *testing.T) {
	s, d, _ := newTableImportTestDocument()
	defer s.Close(d)
	d.transfer.NewColumns[1].Name = "label"
	columns, edits, err := mysqlDataImportRequest(d)
	if err != nil || columns[1].Name != "label" || edits[0].Values["label"].Text != "中文" || edits[0].Values["amount"].Text != "12.50" {
		t.Fatal(columns, edits, err)
	}
	d.transfer.NewColumns[2].Type = "DECIMAL(3,0)"
	if _, _, err := mysqlDataImportRequest(d); err == nil {
		t.Fatal("preview allowed rounding away the source scale")
	}
	d.transfer.NewColumns[2].Name = ""
	columns, edits, err = mysqlDataImportRequest(d)
	if err != nil || len(columns) != 2 || len(edits[0].Values) != 2 {
		t.Fatal("ignored field still required", columns, edits, err)
	}
	d.transfer.NewColumns[1].Name = "ID"
	if _, _, err := mysqlDataImportRequest(d); err == nil {
		t.Fatal("case-insensitive duplicate target fields accepted")
	}
	d.transfer.NewColumns[1].Name = "label"
	d.transfer.NewName = strings.Repeat("x", 65)
	if _, _, err := mysqlDataImportRequest(d); err == nil {
		t.Fatal("invalid destination enabled create")
	}
}

func TestDataImportNativeNewTableWizardShowsReviewAndQualifiedCompletion(t *testing.T) {
	s, d, _ := newTableImportTestDocument()
	defer s.Close(d)
	var opened TableImportEvent
	s.deps.OpenImportedTable = func(_ ui.Services, e TableImportEvent) { opened = e }
	s.NewTableImporter = func(context.Context, int64, string, string, []mysqlsvc.MySQLDataImportColumn, []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataNewTableImportResult, error) {
		return &mysqlsvc.MySQLDataNewTableImportResult{Created: true, Saved: &mysqlsvc.MySQLDataEditResult{Outcomes: []mysqlsvc.MySQLDataEditOutcome{{Kind: "insert"}}}}, nil
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.transferModals(c, d) }, 1000, 1000)
	for _, label := range []string{"新建目标表", "新目标表名称", "id 的目标字段", "amount 的数据类型", "可空", "主键", "输入表名称以确认导入"} {
		if !tt.HasText(label) {
			t.Fatal("missing review control", label, tt.Texts())
		}
	}
	if err := tt.Click("确认导入"); err != nil {
		t.Fatal(err)
	}
	waitDataTransfer(t, s, d)
	tt.Frame()
	if !d.transfer.Completed || !d.transfer.Open || !tt.HasText("已创建 new_table 并导入 1 行") {
		t.Fatal("missing completion", d.transfer, tt.Texts())
	}
	if err := tt.Click("打开目标表"); err != nil {
		t.Fatal(err)
	}
	if opened.ProfileID != 12 || opened.Generation != 2 || opened.Database != "db" || opened.Table != "new_table" || !opened.Completed || d.table != "t" {
		t.Fatal("completion changed source or destination", opened)
	}
}

func TestDataImportNewTableWorkerOwnsDefinitionDestinationAndValues(t *testing.T) {
	s, d, _ := newTableImportTestDocument()
	defer s.Close(d)
	started, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var event TableImportEvent
	s.deps.TableImported = func(_ ui.Services, e TableImportEvent) { event = e }
	s.NewTableImporter = func(_ context.Context, id int64, db, table string, cols []mysqlsvc.MySQLDataImportColumn, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataNewTableImportResult, error) {
		close(started)
		<-release
		if id != 12 || db != "db" || table != "new_table" || cols[1].Name != "note" || edits[0].Values["note"].Text != "中文" {
			t.Error("worker observed later UI", id, db, table, cols, edits)
		}
		return &mysqlsvc.MySQLDataNewTableImportResult{Created: true, Saved: &mysqlsvc.MySQLDataEditResult{Outcomes: []mysqlsvc.MySQLDataEditOutcome{{Kind: "insert"}}}}, nil
	}
	s.startDataImport(ui.Services{}, d)
	<-started
	d.transfer.NewName = "later"
	d.transfer.NewColumns[1].Name = "later"
	d.transfer.Input.Rows[0][1].Text = "later"
	close(release)
	waitDataTransfer(t, s, d)
	if event.Table != "new_table" || d.transfer.ResultTable != "new_table" || d.page.Columns[0] != "id" || d.table != "t" {
		t.Fatal("result lost original ownership", event, d.transfer)
	}
}

func TestDataImportNewTableParkedAndUncertainResultNeverReplaysSource(t *testing.T) {
	for _, mode := range []string{"created-rows-failed", "creation-uncertain", "commit-uncertain", "stale"} {
		t.Run(mode, func(t *testing.T) {
			s, d, session := newTableImportTestDocument()
			defer s.Close(d)
			result := &mysqlsvc.MySQLDataNewTableImportResult{Created: true}
			if mode == "creation-uncertain" {
				result.Created = false
				result.CreationUncertain = true
			}
			if mode == "commit-uncertain" {
				result.Saved = &mysqlsvc.MySQLDataEditResult{CommitUncertain: true}
			}
			release := make(chan struct{})
			calls := 0
			s.NewTableImporter = func(context.Context, int64, string, string, []mysqlsvc.MySQLDataImportColumn, []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataNewTableImportResult, error) {
				calls++
				<-release
				return result, errors.New("fixture failure")
			}
			s.startDataImport(ui.Services{}, d)
			session.ProfileID = 13
			close(release)
			// Wait for the private buffered result while its original profile is parked.
			deadline := time.Now().Add(3 * time.Second)
			for len(d.transfer.Results) == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if len(d.transfer.Results) == 0 {
				t.Fatal("new-table worker did not return", d.transfer.Err)
			}
			s.consumeDataTransfers(ui.Services{})
			if !d.transfer.Busy {
				t.Fatal("parked result delivered")
			}
			session.ProfileID = 12
			if mode == "stale" {
				session.Generation++
			}
			waitDataTransfer(t, s, d)
			wantUncertain := mode != "created-rows-failed"
			if d.transfer.Uncertain != wantUncertain || d.edit.Uncertain != nil || !d.transfer.Submitted || d.table != "t" {
				t.Fatal("new target affected unrelated source", d.transfer, d.edit.Uncertain)
			}
			s.startDataImport(ui.Services{}, d)
			if calls != 1 {
				t.Fatal("new-table import replayed")
			}
			if wantUncertain && !s.GuardTransactionLeave(d, func(ui.Services) {}) {
				t.Fatal("uncertain new target bypassed leave decision")
			}
		})
	}
}

func TestDataImportCreationHistoryContainsDefinitionAndNoRowValues(t *testing.T) {
	s, d, _ := newTableImportTestDocument()
	defer s.Close(d)
	entry := mysqlDataImportCreationHistory("d`b", "new`table", d.transfer.NewColumns)
	if entry.Kind != "CREATE" || strings.Contains(entry.SQL, "中文") || !strings.Contains(entry.SQL, "`d``b`.`new``table`") || !strings.Contains(entry.SQL, "PRIMARY KEY (`id`)") {
		t.Fatal(entry)
	}
	if !reflect.DeepEqual(d.transfer.Input.Rows[0][1], mysqlsvc.MySQLDataValue{Text: "中文"}) {
		t.Fatal("history mutated source")
	}
}
