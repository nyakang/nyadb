package dataworkspace

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestDataTransferHTMLAndXLSXPreserveExactValues(t *testing.T) {
	fields := []string{"id", "description", "payload", "amount"}
	rows := [][]mysqlsvc.MySQLDataValue{
		{{Text: "9007199254740993"}, {Text: "=HYPERLINK(\"https://example.org\")\n<script>&中文_ x_\r\n_x0000_"}, {Binary: true, Bytes: []byte{0, 255, 128}}, {Text: "9007199254740993.1234567890"}},
		{{Text: "2"}, {Text: ""}, {Binary: true}, {Text: "-0.0000000001"}},
		{{Text: "3"}, {Null: true}, {Null: true}, {Null: true}},
	}
	for _, format := range []string{"XLSX", "HTML"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			e, err := newMySQLDataTransferEncoder(&out, format, "db", "t", fields)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if err := e.Row(row); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			input, err := mysqlDataParseImport(bytes.NewReader(out.Bytes()), format)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(input.Fields, fields) || len(input.Rows) != len(rows) {
				t.Fatal(input)
			}
			for i, row := range rows {
				for j, value := range row {
					got := input.Rows[i][j]
					if got.Text != value.Text || got.Null != value.Null || got.Binary != value.Binary || !bytes.Equal(got.Bytes, value.Bytes) {
						t.Fatalf("row %d column %d: %+v != %+v", i, j, got, value)
					}
				}
			}
			if format == "HTML" && strings.Contains(out.String(), "<script>") {
				t.Fatal("unescaped executable markup")
			}
			if format == "XLSX" {
				z, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range z.File {
					if f.Name == "xl/worksheets/sheet1.xml" {
						r, _ := f.Open()
						data, _ := io.ReadAll(r)
						r.Close()
						if strings.Contains(string(data), "<f>") || !strings.Contains(string(data), "9007199254740993.1234567890") {
							t.Fatal("formula or rounded numeric value")
						}
					}
				}
			}
		})
	}
}

func spreadsheetPackage(t *testing.T, overrides map[string]string) []byte {
	t.Helper()
	files := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="` + mysqlDataSpreadsheetNS + `" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="rId7"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId7" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/first.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>id</t></si><si><r><t>no</t></r><r><t>te</t></r></si><si><t>amount</t></si><si><t>at</t></si><si><t>中文_x000D_内容</t></si></sst>`,
		"xl/styles.xml":              `<styleSheet><cellXfs><xf numFmtId="0"/><xf numFmtId="14"/></cellXfs></styleSheet>`,
		"xl/worksheets/first.xml":    `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c><c r="D1" t="s"><v>3</v></c></row><row r="2"><c r="A2"><v>9007199254740993</v></c><c r="B2" t="s"><v>4</v></c><c r="C2"><v>1234567890.12345</v></c><c r="D2" s="1"><v>46325</v></c></row><row r="3"><c r="A3" t="b"><v>1</v></c><c r="C3"><v>1e-10</v></c></row></sheetData></worksheet>`,
	}
	for name, contents := range overrides {
		files[name] = contents
	}
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for name, contents := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestDataTransferXLSXReadsExternalSharedStringsSparseNumbersAndDates(t *testing.T) {
	input, err := mysqlDataParseImport(bytes.NewReader(spreadsheetPackage(t, nil)), "XLSX")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input.Fields, []string{"id", "note", "amount", "at"}) {
		t.Fatal(input.Fields)
	}
	if input.Rows[0][0].Text != "9007199254740993" || input.Rows[0][1].Text != "中文\r内容" || input.Rows[0][2].Text != "1234567890.12345" || input.Rows[0][3].Text != "2026-10-30" {
		t.Fatal(input.Rows[0])
	}
	if input.Rows[1][0].Text != "1" || !input.Rows[1][1].Null || input.Rows[1][2].Text != "1e-10" || !input.Rows[1][3].Null {
		t.Fatal(input.Rows[1])
	}
}

func TestDataTransferXLSXRejectsUnsafeOrAmbiguousPackages(t *testing.T) {
	for name, override := range map[string]map[string]string{
		"external":          {"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId7" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="https://example.org/file" TargetMode="External"/></Relationships>`},
		"formula":           {"xl/worksheets/first.xml": `<worksheet><sheetData><row r="1"><c t="inlineStr"><is><t>id</t></is></c></row><row r="2"><c r="A2"><f>1+1</f><v>2</v></c></row></sheetData></worksheet>`},
		"duplicate cell":    {"xl/worksheets/first.xml": `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>id</t></is></c><c r="A1" t="inlineStr"><is><t>note</t></is></c></row></sheetData></worksheet>`},
		"wide":              {"xl/worksheets/first.xml": `<worksheet><sheetData><row r="1"><c r="XFD1" t="inlineStr"><is><t>id</t></is></c></row></sheetData></worksheet>`},
		"bad shared string": {"xl/worksheets/first.xml": `<worksheet><sheetData><row r="1"><c t="s"><v>9999999999</v></c></row></sheetData></worksheet>`},
		"error":             {"xl/worksheets/first.xml": `<worksheet><sheetData><row r="1"><c t="e"><v>#VALUE!</v></c></row></sheetData></worksheet>`},
		"path traversal":    {"../evil": "data"},
		"compressed bomb":   {"xl/sharedStrings.xml": strings.Repeat("x", 33<<20)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := mysqlDataParseImport(bytes.NewReader(spreadsheetPackage(t, override)), "XLSX"); err == nil {
				t.Fatal("accepted invalid spreadsheet")
			}
		})
	}
}

func TestDataTransferXLSXExportLimitPreservesExistingFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "values.xlsx")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	err := mysqlDataAtomicFile(context.Background(), file, func(w io.Writer) error {
		e, err := newMySQLDataTransferEncoder(w, "XLSX", "db", "t", []string{"long"})
		if err != nil {
			return err
		}
		if err := e.Row([]mysqlsvc.MySQLDataValue{{Text: strings.Repeat("😀", 16384)}}); err != nil {
			return err
		}
		return e.Close()
	})
	if err == nil {
		t.Fatal("accepted >32767 UTF16 units")
	}
	data, _ := os.ReadFile(file)
	if string(data) != "original" {
		t.Fatal("replaced target after incomplete spreadsheet")
	}
	var out bytes.Buffer
	e, err := newMySQLDataTransferEncoder(&out, "XLSX", "db", "t", []string{"id"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Row([]mysqlsvc.MySQLDataValue{{Text: "\x00"}}); err == nil {
		t.Fatal("silently changed XML control character")
	}
}

func TestDataTransferLargerInputLimitsAreIndependentOfEditingPage(t *testing.T) {
	for _, format := range []string{"CSV", "XML", "XLSX", "HTML"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			e, err := newMySQLDataTransferEncoder(&out, format, "db", "t", []string{"id"})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 1000; i++ {
				if err := e.Row([]mysqlsvc.MySQLDataValue{{Text: "1"}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			input, err := mysqlDataParseImport(bytes.NewReader(out.Bytes()), format)
			if err != nil || len(input.Rows) != 1000 {
				t.Fatal(input, err)
			}
		})
	}
	_, err := mysqlDataParseImport(strings.NewReader(strings.Repeat("x", mysqlsvc.MaxMySQLDataImportBytes+1)), "CSV")
	if !errors.Is(err, mysqlsvc.ErrMySQLDataEditLimit) {
		t.Fatal("file limit missing", err)
	}
}

func TestDataTransferHTMLReadsOnlyInertUnmergedFirstTable(t *testing.T) {
	input, err := mysqlDataParseImport(strings.NewReader(`<html><script>throw 1</script><table><tr><th>id</th><th>note</th></tr><tr><td>1</td><td><b>中文</b><br>next<script>ignored</script></td></tr></table><table><tr><td>ignored</td></tr></table></html>`), "HTML")
	if err != nil || len(input.Rows) != 1 || input.Rows[0][1].Text != "中文\nnext" {
		t.Fatal(input, err)
	}
	for _, data := range []string{`<table><tr><th colspan="2">id</th></tr><tr><td>1</td></tr></table>`, `<table><tr><th>id</th></tr><tr><td>1</td><td>2</td></tr></table>`, `<table><tr><th>id</th></tr><tr><td data-nyadb-kind="binary">bad</td></tr></table>`} {
		if _, err := mysqlDataParseImport(strings.NewReader(data), "HTML"); err == nil {
			t.Fatal("accepted malformed table")
		}
	}
}
