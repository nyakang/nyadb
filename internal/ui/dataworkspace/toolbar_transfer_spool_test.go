package dataworkspace

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

type largeSourceRows struct {
	index, total int
	value        string
}

func (r *largeSourceRows) Fields() []string { return []string{"note"} }
func (r *largeSourceRows) Close() error     { return nil }
func (r *largeSourceRows) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.index >= r.total {
		return nil, io.EOF
	}
	r.index++
	return []mysqlsvc.MySQLDataValue{{Text: r.value}}, nil
}

func TestDataImportSpoolLargeInputHasBoundedPreviewAndReadsEveryRow(t *testing.T) {
	spool, preview, _, err := mysqlDataSpoolRows(context.Background(), &largeSourceRows{total: 10001, value: strings.Repeat("界", 800)})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	info, err := os.Stat(spool.path)
	if err != nil || info.Size() <= 16<<20 {
		t.Fatal("fixture does not cross old file cap", info, err)
	}
	if spool.rows != 10001 || len(preview.Rows) != 100 {
		t.Fatal("source capped or whole rows cached", spool.rows, len(preview.Rows))
	}
	reader, err := spool.open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	count := 0
	for {
		row, e := reader.Next(context.Background())
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if row[0].Text != strings.Repeat("界", 800) {
			t.Fatal("changed full field")
		}
		count++
	}
	if count != 10001 {
		t.Fatal(count)
	}
}

func TestDataImportSpoolRejectsMutationBeforeAndDuringRead(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "during"}[during], func(t *testing.T) {
			spool, _, _, err := mysqlDataSpoolRows(context.Background(), &largeSourceRows{total: 1024, value: strings.Repeat("x", 2048)})
			if err != nil {
				t.Fatal(err)
			}
			defer spool.Close()
			var reader *mysqlDataSpoolReader
			if during {
				reader, err = spool.open(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				if _, err = reader.Next(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			file, err := os.OpenFile(spool.path, os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = file.WriteAt([]byte("y"), 1<<20); err != nil {
				t.Fatal(err)
			}
			file.Close()
			if !during {
				if r, e := spool.open(context.Background()); e == nil {
					r.Close()
					t.Fatal("accepted changed review source")
				}
				return
			}
			for {
				_, err = reader.Next(context.Background())
				if err != nil {
					break
				}
			}
			if err == io.EOF {
				t.Fatal("mid-read mutation escaped final fingerprint")
			}
		})
	}
}

func TestDataImportClosedOwnerRevokesLatePreparedSpool(t *testing.T) {
	owner := &mysqlDataSpoolOwner{}
	owner.Close()
	spool, _, _, err := mysqlDataSpoolRows(context.Background(), &largeSourceRows{total: 1, value: "late"})
	if err != nil {
		t.Fatal(err)
	}
	if err = owner.add(spool); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = os.Stat(spool.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("closed document retained late file", err)
	}
}

func TestDataImportJournalIndexesAllRowsAndSettlesPerTarget(t *testing.T) {
	owner := &mysqlDataSpoolOwner{}
	defer owner.Close()
	journal, err := newMySQLDataImportJournal(owner, 10001)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10001; i++ {
		if err = journal.Record(mysqlsvc.MySQLDataImportRowResult{Row: i, Kind: "insert", Status: "prepared"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = journal.Flush(); err != nil {
		t.Fatal(err)
	}
	row, err := journal.Row(10000, true, false)
	if err != nil || row.Row != 10001 || row.Status != "committed" {
		t.Fatal(row, err)
	}
	batch := []mysqlDataBatchReceipt{{From: 0, To: 10, Result: &mysqlsvc.MySQLDataImportStreamResult{Committed: true}}, {From: 10, To: 10001, Result: &mysqlsvc.MySQLDataImportStreamResult{}}}
	committed, uncertain := mysqlDataBatchRowSettlement(batch, 10000, false)
	row, err = journal.Row(10000, committed, uncertain)
	if err != nil || row.Status != "rolled_back" {
		t.Fatal("later target rewrote original receipt", row, err)
	}
}

func TestDataImportNativeWizardKeepsFooterAtSmallSizeAndRequiresPreparation(t *testing.T) {
	s, d, _ := transferTestDocument()
	defer s.Close(d)
	s.openDataImport(d)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.transferModals(c, d) }, 900, 650)
	if !tt.HasText("导入向导 · 源与目标") || !tt.HasText("下一步") || !tt.HasText("取消") {
		t.Fatal(tt.Texts())
	}
	if err := tt.Click("下一步"); err == nil && d.transfer.Step != 0 {
		t.Fatal("unread input advanced")
	}
	d.transfer.Step = 4
	tt.Frame()
	if !tt.HasText("完整校验全部源记录") || !tt.HasText("确认导入") {
		t.Fatal(tt.Texts())
	}
	if err := tt.Click("确认导入"); err == nil && d.transfer.Submitted {
		t.Fatal("unprepared rows submitted")
	}
}

func TestDataImportMultipleSourcesOwnMappingAndTargetState(t *testing.T) {
	s, d, _ := transferTestDocument()
	defer s.Close(d)
	d.transfer.Wizard = true
	d.transfer.Sources = []mysqlDataWizardSource{{Name: "first"}}
	d.transfer.Options = defaultMySQLDataSourceOptions()
	d.transfer.Included = map[string]bool{"id": true}
	s.addWizardSource(d)
	if len(d.transfer.Sources) != 2 || d.transfer.Input != nil {
		t.Fatal("new source inherited rows")
	}
	d.transfer.Mapping = []string{"other"}
	d.transfer.TargetTable = "other_table"
	s.switchWizardSource(d, 0)
	if d.transfer.Mapping[0] != "id" || d.transfer.TargetTable != "" || !d.transfer.Included["id"] {
		t.Fatal("source switch changed original identity", d.transfer.Mapping, d.transfer.TargetTable)
	}
}

func TestDataTransferPresetNeverPersistsNativeCredentials(t *testing.T) {
	s, d, _ := transferTestDocument()
	defer s.Close(d)
	d.transfer.PresetName = "named"
	d.transfer.SourceConfig.ConnectionString = "PASSWORD=secret-value"
	saved := ""
	s.SetDataLayoutPreferences(func(string) (string, error) { return saved, nil }, func(_ string, value string) error { saved = value; return nil })
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.dataTransferPresetControls(c, d) }, 800, 400)
	if err := tt.Click("保存向导配置"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(saved, "secret-value") || strings.Contains(saved, "ConnectionString") || !strings.Contains(saved, "named") {
		t.Fatal("persisted transient secret or missing config")
	}
}
