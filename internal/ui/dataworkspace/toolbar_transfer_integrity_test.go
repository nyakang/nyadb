package dataworkspace

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestDataTransferCustomTextQualifierHeaderlessAndRanges(t *testing.T) {
	o := mysqlDataSourceDefaults("TXT")
	o.Delimiter = "|"
	o.Quote = "'"
	o.HeaderRow = 0
	o.FirstRow = 2
	o.LastRow = 3
	r, err := mysqlDataOpenDelimitedRows(strings.NewReader("skip|row\n'line\n界'|'it''s'\n\\N|\nextra|row\n"), o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Fields(), []string{"field_1", "field_2"}) {
		t.Fatal(r.Fields())
	}
	first, err := r.Next(context.Background())
	if err != nil || first[0].Text != "line\n界" || first[1].Text != "it's" {
		t.Fatal(first, err)
	}
	second, err := r.Next(context.Background())
	if err != nil || !second[0].Null || second[1].Text != "" || second[1].Null {
		t.Fatal(second, err)
	}
	if _, err = r.Next(context.Background()); err != io.EOF {
		t.Fatal(err)
	}
}
func TestDataTransferConfiguredTextEncodingRoundTripAndRejectsUnexpressible(t *testing.T) {
	for _, encoding := range []string{"UTF-8", "UTF-16LE", "UTF-16BE", "GB18030"} {
		t.Run(encoding, func(t *testing.T) {
			o := mysqlDataSourceDefaults("CSV")
			o.Encoding = encoding
			o.Delimiter = ";"
			o.Quote = "'"
			o.Newline = "CRLF"
			var out bytes.Buffer
			e, err := newMySQLDataConfiguredEncoder(context.Background(), &out, "CSV", "db", "table", []string{"value"}, nil, o)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range [][]mysqlsvc.MySQLDataValue{{{Text: "界;'\r\n"}}, {{Null: true}}, {{Text: ""}}} {
				if err = e.Row(row); err != nil {
					t.Fatal(err)
				}
			}
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			r, err := mysqlDataOpenDelimitedRows(&out, o)
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.Next(context.Background())
			if err != nil || got[0].Text != "界;'\r\n" {
				t.Fatal(got, err)
			}
			got, err = r.Next(context.Background())
			if err != nil || !got[0].Null {
				t.Fatal(got, err)
			}
			got, err = r.Next(context.Background())
			if err != nil || got[0].Null || got[0].Text != "" {
				t.Fatal(got, err)
			}
		})
	}
	o := mysqlDataSourceDefaults("CSV")
	o.Encoding = "Windows-1252"
	var out bytes.Buffer
	e, err := newMySQLDataConfiguredEncoder(context.Background(), &out, "CSV", "d", "t", []string{"field"}, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Row([]mysqlsvc.MySQLDataValue{{Text: "界"}}); err == nil {
		err = e.Close()
	}
	if err == nil {
		t.Fatal("silently replaced an unmappable character")
	}
}
func TestDataTransferTargetIdentityAndSourceOptionsRevokePreparedReview(t *testing.T) {
	s, d, _ := transferTestDocument()
	defer s.Close(d)
	key := mysqlDataPreparationKey(d)
	d.transfer.TargetTable = "other"
	if key == mysqlDataPreparationKey(d) {
		t.Fatal("target identity absent from review key")
	}
	if _, _, _, err := mysqlDataImportMapping(d); err == nil {
		t.Fatal("reused current table metadata for unread target")
	}
	d.transfer.TargetColumns = []mysqlsvc.MySQLStructureColumn{{Name: "other_id", DataType: "int", Key: "PRI"}}
	mysqlDataResetTargetMapping(d)
	if d.transfer.Mapping[0] != "" {
		t.Fatal("kept invalid old field mapping")
	}
	d.transfer.TargetTable = ""
	d.transfer.TargetColumns = nil
	mysqlDataResetTargetMapping(d)
	if d.transfer.Mapping[0] != "id" {
		t.Fatal("failed to recover original table metadata")
	}
	o := mysqlDataSourceDefaults("CSV")
	parsed := mysqlDataSourceKey("CSV", o)
	o.DecimalSeparator = ","
	if parsed != mysqlDataSourceKey("CSV", o) {
		t.Fatal("conversion revoked source parsing")
	}
	o.Delimiter = ";"
	if parsed == mysqlDataSourceKey("CSV", o) {
		t.Fatal("parse option absent from source key")
	}
}
func TestDataTransferRejectsIncompleteCommitReceiptAndNeverReplays(t *testing.T) {
	s, d, _ := transferTestDocument()
	defer s.Close(d)
	spool, _, _, err := mysqlDataSpoolRows(context.Background(), &largeSourceRows{total: 2, value: "safe"})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.transfer.Assets.add(spool); err != nil {
		t.Fatal(err)
	}
	calls := 0
	importer := func(ctx context.Context, _ int64, _ string, _ string, r mysqlsvc.MySQLDataImportStreamRequest) (*mysqlsvc.MySQLDataImportStreamResult, error) {
		calls++
		if err := r.Record(mysqlsvc.MySQLDataImportRowResult{Row: 1, Kind: "insert", Status: "prepared"}); err != nil {
			return nil, err
		}
		return &mysqlsvc.MySQLDataImportStreamResult{Committed: true, Progress: mysqlsvc.MySQLDataImportProgress{Processed: 2, Prepared: 2}}, nil
	}
	s.executePreparedSources(ui.Services{}, d, importer, []mysqlDataPreparedSource{{spool: spool, database: d.database, table: d.table}})
	deadline := time.Now().Add(3 * time.Second)
	for d.transfer.Busy && time.Now().Before(deadline) {
		s.consumeDataTransfers(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if calls != 1 || !d.transfer.Uncertain || d.transfer.Completed || !d.transfer.Submitted {
		t.Fatal("incomplete journal treated as safe completion", calls, d.transfer)
	}
	s.startPreparedDataImport(ui.Services{}, d)
	if calls != 1 {
		t.Fatal("uncertain import replayed")
	}
	row, err := d.transfer.Journal.Row(0, false, true)
	if err != nil || row.Status != "uncertain" {
		t.Fatal(row, err)
	}
}
func TestDataTransferXMLAttributesRejectNullRatherThanDroppingSemantics(t *testing.T) {
	var out bytes.Buffer
	o := mysqlDataSourceDefaults("XML")
	o.XMLRoot = "rows"
	o.XMLRow = "row"
	o.XMLAttributes = true
	e, err := newMySQLDataConfiguredEncoder(context.Background(), &out, "XML", "d", "t", []string{"field"}, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Row([]mysqlsvc.MySQLDataValue{{Null: true}}); err == nil {
		t.Fatal("null silently changed to empty attribute")
	}
}
