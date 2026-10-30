package dataworkspace

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/parquet-go/parquet-go"
)

// Write a worksheet independently of the NyaDB encoder, which previously had
// a total-file limit. ZIP Store makes both the source and expanded sheet exceed
// that old limit, rather than relying on a small compressed repetitive file.
func mysqlDataTestXLSX(t *testing.T, file *os.File, sheets []func(io.Writer), extra map[string]string) {
	t.Helper()
	z := zip.NewWriter(file)
	write := func(name, value string) {
		w, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(w, value); err != nil {
			t.Fatal(err)
		}
	}
	var book, rel strings.Builder
	book.WriteString(`<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	rel.WriteString(`<Relationships>`)
	for i, sheet := range sheets {
		fmt.Fprintf(&book, `<sheet name="Data%d" sheetId="%d" r:id="rId%d"/>`, i+1, i+1, i+1)
		fmt.Fprintf(&rel, `<Relationship Id="rId%d" Target="worksheets/sheet%d.xml" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet"/>`, i+1, i+1)
		w, err := z.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		sheet(w)
	}
	book.WriteString(`</sheets></workbook>`)
	rel.WriteString(`</Relationships>`)
	write("xl/workbook.xml", book.String())
	write("xl/_rels/workbook.xml.rels", rel.String())
	for name, value := range extra {
		write(name, value)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
}

func mysqlDataTestOpenRows(t *testing.T, ctx context.Context, format string, f *os.File, options mysqlDataSourceOptions) mysqlDataImportRowReader {
	t.Helper()
	var reader mysqlDataImportRowReader
	var err error
	switch format {
	case "JSON":
		reader, err = mysqlDataOpenJSONRows(ctx, f, options)
	case "XML":
		reader, err = mysqlDataOpenXMLRows(ctx, f, options)
	case "HTML":
		reader, err = mysqlDataOpenHTMLRows(ctx, f, options)
	case "XLSX":
		info, e := f.Stat()
		if e != nil {
			t.Fatal(e)
		}
		reader, err = mysqlDataOpenXLSXRows(ctx, f, info.Size(), options)
	default:
		t.Fatal(format)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	})
	return reader
}

func TestDataTransferStreamingFormatsReadEntireLargeFile(t *testing.T) {
	const count = 11001
	note := strings.Repeat("line ", 308) + "中文"
	for _, format := range []string{"JSON", "XML", "HTML", "XLSX"} {
		t.Run(format, func(t *testing.T) {
			f, err := os.Create(filepath.Join(t.TempDir(), "large."+strings.ToLower(format)))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if format == "XLSX" {
				mysqlDataTestXLSX(t, f, []func(io.Writer){func(w io.Writer) {
					io.WriteString(w, `<worksheet><sheetData><row r="1"><c t="inlineStr"><is><t>id</t></is></c><c t="inlineStr"><is><t>note</t></is></c></row>`)
					for i := 1; i <= count; i++ {
						fmt.Fprintf(w, `<row r="%d"><c><v>%d</v></c><c t="inlineStr"><is><t>%s</t></is></c></row>`, i+1, i, note)
					}
					io.WriteString(w, `</sheetData></worksheet>`)
				}}, nil)
			} else {
				switch format {
				case "JSON":
					io.WriteString(f, "[")
				case "XML":
					io.WriteString(f, `<RECORDS>`)
				case "HTML":
					io.WriteString(f, `<html><table><tr><th>id</th><th>note</th></tr>`)
				}
				for i := 1; i <= count; i++ {
					switch format {
					case "JSON":
						if i > 1 {
							io.WriteString(f, ",")
						}
						fmt.Fprintf(f, `{"id":%d,"note":%q}`, i, note)
					case "XML":
						fmt.Fprintf(f, `<RECORD><id>%d</id><note>%s</note></RECORD>`, i, note)
					case "HTML":
						fmt.Fprintf(f, `<tr><td>%d</td><td>%s</td></tr>`, i, note)
					}
				}
				switch format {
				case "JSON":
					io.WriteString(f, "]")
				case "XML":
					io.WriteString(f, `</RECORDS>`)
				case "HTML":
					io.WriteString(f, `</table></html>`)
				}
			}
			info, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() <= 16<<20 {
				t.Fatalf("fixture is only %d bytes", info.Size())
			}
			if _, err = f.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			o := mysqlDataSourceDefaults(format)
			if format == "HTML" {
				o.HeaderRow, o.FirstRow = 1, 2
			}
			r := mysqlDataTestOpenRows(t, context.Background(), format, f, o)
			if !reflect.DeepEqual(r.Fields(), []string{"id", "note"}) {
				t.Fatal(r.Fields())
			}
			var at1000 uint64
			for i := 1; i <= count; i++ {
				row, err := r.Next(context.Background())
				if err != nil {
					t.Fatalf("row %d: %v", i, err)
				}
				if row[0].Text != strconv.Itoa(i) || row[1].Text != note {
					t.Fatalf("row %d changed", i)
				}
				if i == 1000 || i == count {
					runtime.GC()
					var mem runtime.MemStats
					runtime.ReadMemStats(&mem)
					if i == 1000 {
						at1000 = mem.HeapAlloc
					} else {
						delta := int64(mem.HeapAlloc) - int64(at1000)
						t.Logf("source bytes=%d rows=%d retained heap growth from 1000 rows=%d bytes", info.Size(), i, delta)
						if delta > 8<<20 {
							t.Fatalf("reader retains input rows: heap grew %d bytes", delta)
						}
					}
				}
			}
			if _, err = r.Next(context.Background()); err != io.EOF {
				t.Fatal(err)
			}
		})
	}
}

func TestDataTransferStreamingJSONPreservesNumbersRejectsLossAndHonorsRanges(t *testing.T) {
	options := mysqlDataSourceDefaults("JSON")
	options.FirstRow, options.LastRow = 2, 2
	r, err := mysqlDataOpenJSONRows(context.Background(), strings.NewReader(`[{"a":null,"b":""},{"a":9007199254740993.1234567890,"b":"\ud83d\ude42"},{"a":0,"b":"last"}]`), options)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	row, err := r.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if row[0].Text != "9007199254740993.1234567890" || row[1].Text != "🙂" {
		t.Fatal(row)
	}
	if _, err = r.Next(context.Background()); err != io.EOF {
		t.Fatal(err)
	}
	for _, input := range []string{`[{"a":"\ud800"}]`, `[{"a":"\udc00"}]`, `[{"a":"` + string([]byte{0xff}) + `"}]`, `[{"a":1,"a":2}]`, `[{"a":{"nested":1}}]`} {
		if r, err := mysqlDataOpenJSONRows(context.Background(), strings.NewReader(input), mysqlDataSourceDefaults("JSON")); err == nil {
			r.Close()
			t.Fatal("lossy/ambiguous source accepted", input)
		}
	}
	r, err = mysqlDataOpenJSONRows(context.Background(), strings.NewReader(`[{"a":1},{"b":2}]`), mysqlDataSourceDefaults("JSON"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.Next(context.Background())
	if _, err = r.Next(context.Background()); err == nil {
		t.Fatal("changed field set accepted")
	}
}

func TestDataTransferStreamingXMLSettingsPreserveKindsAndRanges(t *testing.T) {
	o := mysqlDataSourceDefaults("XML")
	o.XMLRoot, o.XMLRow, o.FirstRow, o.LastRow = "data", "item", 2, 3
	input := `<data xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><item><a>skip</a><b>0</b></item><item><b encoding="hex">00ff</b><a xsi:nil="true"/></item><item><a></a><b encoding="hex"></b></item></data>`
	r, err := mysqlDataOpenXMLRows(context.Background(), strings.NewReader(input), o)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	row, err := r.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !row[0].Null || !row[1].Binary || !bytes.Equal(row[1].Bytes, []byte{0, 255}) {
		t.Fatal(row)
	}
	row, err = r.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if row[0].Null || row[0].Text != "" || !row[1].Binary || len(row[1].Bytes) != 0 {
		t.Fatal(row)
	}
	if _, err = r.Next(context.Background()); err != io.EOF {
		t.Fatal(err)
	}
	o.XMLAttributes = true
	o.FirstRow, o.LastRow = 1, 0
	r2, err := mysqlDataOpenXMLRows(context.Background(), strings.NewReader(`<data><item a="中文" b="9007199254740993"/></data>`), o)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	row, err = r2.Next(context.Background())
	if err != nil || row[0].Text != "中文" || row[1].Text != "9007199254740993" {
		t.Fatal(row, err)
	}
}

func TestDataTransferStreamingHTMLSettingsIgnoreActiveContentAndNestedTables(t *testing.T) {
	o := mysqlDataSourceDefaults("HTML")
	o.HeaderRow, o.FirstRow, o.LastRow = 2, 3, 4
	input := `<table><tr><td>skip</td><td>skip</td></tr><tr><th>a</th><th>b</th></tr><tr><td>line<br>two<script>bad()</script><table><tr><td>nested</td></tr></table>end</td><td data-nyadb-kind="binary">0x00ff</td></tr><tr><td data-nyadb-kind="null">NULL</td><td></td></tr></table><table><tr><td>ignored</td></tr></table>`
	r, err := mysqlDataOpenHTMLRows(context.Background(), strings.NewReader(input), o)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	row, err := r.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if row[0].Text != "line\ntwoend" || !bytes.Equal(row[1].Bytes, []byte{0, 255}) {
		t.Fatal(row)
	}
	row, err = r.Next(context.Background())
	if err != nil || !row[0].Null || row[1].Null || row[1].Text != "" {
		t.Fatal(row, err)
	}
	if _, err = r.Next(context.Background()); err != io.EOF {
		t.Fatal(err)
	}
	o.HeaderRow, o.FirstRow, o.LastRow = 0, 1, 0
	r2, err := mysqlDataOpenHTMLRows(context.Background(), strings.NewReader(`<table><tr><td>first</td></tr><tr><td>second</td></tr></table>`), o)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if !reflect.DeepEqual(r2.Fields(), []string{"Column1"}) {
		t.Fatal(r2.Fields())
	}
	for _, want := range []string{"first", "second"} {
		row, err = r2.Next(context.Background())
		if err != nil || row[0].Text != want {
			t.Fatal(row, err)
		}
	}
}

func TestDataTransferStreamingXLSXSheetsSharedStringsSparseCellsAndCleanup(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "sheets.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	first := func(w io.Writer) {
		io.WriteString(w, `<worksheet><sheetData><row r="1"><c t="inlineStr"><is><t>wrong</t></is></c></row></sheetData></worksheet>`)
	}
	second := func(w io.Writer) {
		io.WriteString(w, `<worksheet><sheetData><row r="2"><c r="A2" t="s"><v>0</v></c><c r="B2" t="s"><v>1</v></c><c r="C2" t="s"><v>2</v></c></row><row r="3"><c r="A3" t="s"><v>3</v></c><c r="C3" s="1"><v>45292</v></c></row><row r="5"><c r="A5" t="inlineStr"><is><t>last</t></is></c><c r="B5" t="b"><v>1</v></c></row></sheetData></worksheet>`)
	}
	mysqlDataTestXLSX(t, f, []func(io.Writer){first, second}, map[string]string{
		"xl/sharedStrings.xml": `<sst><si><t>a</t></si><si><t>b</t></si><si><t>c</t></si><si><r><t>中文</t></r><r><t xml:space="preserve"> tail </t></r></si></sst>`,
		"xl/styles.xml":        `<styleSheet><cellXfs><xf numFmtId="0"/><xf numFmtId="14"/></cellXfs></styleSheet>`,
	})
	o := mysqlDataSourceDefaults("XLSX")
	o.Sheet, o.HeaderRow, o.FirstRow, o.LastRow = 1, 2, 3, 5
	info, _ := f.Stat()
	r, err := mysqlDataOpenXLSXRows(context.Background(), f, info.Size(), o)
	if err != nil {
		t.Fatal(err)
	}
	stringsName, indexName := r.shared.file.Name(), r.shared.index.Name()
	row, err := r.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if row[0].Text != "中文 tail " || !row[1].Null || row[2].Text != "2024-01-01" {
		t.Fatal(row)
	}
	row, err = r.Next(context.Background())
	if err != nil || row[0].Text != "last" || row[1].Text != "1" || !row[2].Null {
		t.Fatal(row, err)
	}
	if _, err = r.Next(context.Background()); err != io.EOF {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{stringsName, indexName} {
		if _, err = os.Stat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("temporary shared strings leaked", err)
		}
	}
	o.HeaderRow, o.FirstRow, o.LastRow = 0, 1, 2
	r, err = mysqlDataOpenXLSXRows(context.Background(), f, info.Size(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !reflect.DeepEqual(r.Fields(), []string{"Column1", "Column2", "Column3"}) {
		t.Fatal(r.Fields())
	}
	row, err = r.Next(context.Background())
	if err != nil || row[0].Text != "a" {
		t.Fatal(row, err)
	}
	if _, err = r.Next(context.Background()); err != io.EOF {
		t.Fatal(err)
	}
}

func TestDataTransferStreamingFormatsCancelAndRejectOversizeFields(t *testing.T) {
	for _, format := range []string{"JSON", "XML", "HTML"} {
		t.Run(format, func(t *testing.T) {
			options := mysqlDataSourceDefaults(format)
			if format == "HTML" {
				options.HeaderRow, options.FirstRow = 1, 2
			}
			var good, bad string
			long := strings.Repeat("a", mysqlsvc.MySQLDataExportFieldMaxBytes+1)
			switch format {
			case "JSON":
				good = `[{"a":"first"},{"a":"second"}]`
				bad = `[{"a":"` + long + `"}]`
			case "XML":
				good = `<RECORDS><RECORD><a>first</a></RECORD><RECORD><a>second</a></RECORD></RECORDS>`
				bad = `<RECORDS><RECORD><a>` + long + `</a></RECORD></RECORDS>`
			case "HTML":
				good = `<table><tr><th>a</th></tr><tr><td>first</td></tr><tr><td>second</td></tr></table>`
				bad = `<table><tr><th>a</th></tr><tr><td>` + long + `</td></tr></table>`
			}
			open := func(input string) (mysqlDataImportRowReader, error) {
				switch format {
				case "JSON":
					return mysqlDataOpenJSONRows(context.Background(), strings.NewReader(input), options)
				case "XML":
					return mysqlDataOpenXMLRows(context.Background(), strings.NewReader(input), options)
				default:
					return mysqlDataOpenHTMLRows(context.Background(), strings.NewReader(input), options)
				}
			}
			r, err := open(good)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err = r.Next(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if row, err := r.Next(context.Background()); err != nil || row[0].Text != "first" {
				t.Fatal("cancel consumed a row", row, err)
			}
			badReader, err := open(bad)
			if err == nil {
				defer badReader.Close()
				_, err = badReader.Next(context.Background())
			}
			if !errors.Is(err, mysqlsvc.ErrMySQLDataEditLimit) {
				t.Fatal("oversized field accepted or wrong error", err)
			}
		})
	}
}

func TestDataTransferStreamingXLSSettingsAndDiskStrings(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "transfer_formats", "external-settings.xls"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, _ := f.Stat()
	o := mysqlDataSourceDefaults("XLS")
	o.Sheet, o.HeaderRow, o.FirstRow, o.LastRow = 1, 2, 3, 3
	r, err := mysqlDataOpenXLSRowsOptions(context.Background(), f, info.Size(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Fields(), []string{"id", "note"}) {
		t.Fatal(r.Fields())
	}
	names := []string{r.sst.strings.file.Name(), r.sst.strings.index.Name()}
	row, err := r.Next(context.Background())
	if err != nil || row[0].Text != "9007199254740993" || row[1].Text != "中文\nline" {
		t.Fatal(row, err)
	}
	if _, err = r.Next(context.Background()); err != io.EOF {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err = os.Stat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("SST leaked", err)
		}
	}
	o.HeaderRow, o.FirstRow, o.LastRow = 0, 1, 0
	r, err = mysqlDataOpenXLSRowsOptions(context.Background(), f, info.Size(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !reflect.DeepEqual(r.Fields(), []string{"Column1"}) {
		t.Fatal(r.Fields())
	}
	row, err = r.Next(context.Background())
	if err != nil || row[0].Text != "discard" {
		t.Fatal(row, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = mysqlDataOpenXLSRowsOptions(ctx, f, info.Size(), o); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDataTransferStreamingXLSHeaderlessRetainsBinaryKinds(t *testing.T) {
	var out bytes.Buffer
	e, err := newMySQLDataXLSEncoder(&out, []string{"note", "payload"})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range [][]mysqlsvc.MySQLDataValue{{{Text: ""}, {Binary: true, Bytes: []byte{0, 255}}}, {{Null: true}, {Binary: true, Bytes: []byte{}}}} {
		if err = e.Row(row); err != nil {
			e.Abort()
			t.Fatal(err)
		}
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	o := mysqlDataSourceDefaults("XLS")
	o.HeaderRow, o.FirstRow = 0, 1
	r, err := mysqlDataOpenXLSRowsOptions(context.Background(), bytes.NewReader(out.Bytes()), int64(out.Len()), o)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	row, err := r.Next(context.Background())
	if err != nil || row[0].Text != "note" {
		t.Fatal(row, err)
	}
	row, err = r.Next(context.Background())
	if err != nil || row[0].Null || row[0].Text != "" || !row[1].Binary || !bytes.Equal(row[1].Bytes, []byte{0, 255}) {
		t.Fatal(row, err)
	}
	row, err = r.Next(context.Background())
	if err != nil || !row[0].Null || !row[1].Binary || len(row[1].Bytes) != 0 {
		t.Fatal(row, err)
	}
}

func TestDataTransferStreamingXLSReadLargeSST(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "large.xls"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	e, err := newMySQLDataXLSEncoder(f, []string{"note"})
	if err != nil {
		t.Fatal(err)
	}
	const count = 11001
	note := strings.Repeat("line ", 310) + "中文"
	for i := 0; i < count; i++ {
		if err = e.Row([]mysqlsvc.MySQLDataValue{{Text: strconv.Itoa(i) + note}}); err != nil {
			e.Abort()
			t.Fatal(err)
		}
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	info, _ := f.Stat()
	if info.Size() <= 16<<20 {
		t.Fatal("fixture below old total limit", info.Size())
	}
	r, err := mysqlDataOpenXLSRowsOptions(context.Background(), f, info.Size(), mysqlDataSourceDefaults("XLS"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < count; i++ {
		row, err := r.Next(context.Background())
		if err != nil || row[0].Text != strconv.Itoa(i)+note {
			t.Fatal(i, row, err)
		}
	}
	if _, err = r.Next(context.Background()); err != io.EOF {
		t.Fatal(err)
	}
	t.Logf("BIFF8 input=%d bytes rows=%d; SST text and index retained on disk", info.Size(), count)
}

func TestDataTransferStreamingXLSXRejectsUnsafeAndLossySources(t *testing.T) {
	for _, test := range []struct {
		name, content string
		extra         map[string]string
	}{
		{"formula", `<row r="1"><c t="inlineStr"><is><t>a</t></is></c></row><row r="2"><c><f>1+1</f><v>2</v></c></row>`, nil},
		{"duplicate cell", `<row r="1"><c t="inlineStr"><is><t>a</t></is></c></row><row r="2"><c r="A2"><v>1</v></c><c r="A2"><v>2</v></c></row>`, nil},
		{"path", `<row r="1"><c t="inlineStr"><is><t>a</t></is></c></row>`, map[string]string{"../escape.xml": "<bad/>"}},
		{"external", `<row r="1"><c t="inlineStr"><is><t>a</t></is></c></row>`, map[string]string{"xl/_rels/sheet.xml.rels": `<Relationships><Relationship TargetMode="External" Target="https://example.invalid"/></Relationships>`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, err := os.Create(filepath.Join(t.TempDir(), "invalid.xlsx"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			mysqlDataTestXLSX(t, f, []func(io.Writer){func(w io.Writer) { io.WriteString(w, `<worksheet><sheetData>`+test.content+`</sheetData></worksheet>`) }}, test.extra)
			info, _ := f.Stat()
			r, err := mysqlDataOpenXLSXRows(context.Background(), f, info.Size(), mysqlDataSourceDefaults("XLSX"))
			if err == nil {
				defer r.Close()
				_, err = r.Next(context.Background())
			}
			if err == nil {
				t.Fatal("unsafe/lossy input accepted")
			}
		})
	}
}

func TestDataTransferStreamingHTMLAndXLSXPreserveExportKinds(t *testing.T) {
	fields := []string{"note", "payload", "amount"}
	want := [][]mysqlsvc.MySQLDataValue{
		{{Text: "中文\r\n=1+1 trailing "}, {Binary: true, Bytes: []byte{0, 255, 128}}, {Text: "9007199254740993.1234567890"}},
		{{Text: ""}, {Binary: true, Bytes: []byte{}}, {Null: true}},
		{{Null: true}, {Null: true}, {Text: "-0.0000000001"}},
	}
	for _, format := range []string{"HTML", "XLSX"} {
		t.Run(format, func(t *testing.T) {
			f, err := os.Create(filepath.Join(t.TempDir(), "roundtrip."+strings.ToLower(format)))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			e, err := newMySQLDataTransferEncoder(f, format, "db", "t", fields)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range want {
				if err = e.Row(row); err != nil {
					t.Fatal(err)
				}
			}
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = f.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			r := mysqlDataTestOpenRows(t, context.Background(), format, f, mysqlDataSourceDefaults(format))
			for i, row := range want {
				got, err := r.Next(context.Background())
				if err != nil {
					t.Fatal(i, err)
				}
				for j, v := range row {
					if got[j].Text != v.Text || got[j].Null != v.Null || got[j].Binary != v.Binary || !bytes.Equal(got[j].Bytes, v.Bytes) {
						t.Fatalf("%d/%d: %#v != %#v", i, j, got[j], v)
					}
				}
			}
			if _, err = r.Next(context.Background()); err != io.EOF {
				t.Fatal(err)
			}
		})
	}
}

func TestDataTransferXLSRejectsUnpairedUnicode(t *testing.T) {
	for _, raw := range [][]byte{{0x00, 0xd8}, {0x00, 0xdc}} {
		if _, err := mysqlDataBIFFPlainString(raw, 1, true); err == nil {
			t.Fatal("invalid UTF-16 silently replaced")
		}
	}
	if text, err := mysqlDataBIFFPlainString([]byte{0x3d, 0xd8, 0x42, 0xde}, 2, true); err != nil || text != "🙂" {
		t.Fatal(text, err)
	}
}

type mysqlDataCodecFailWriter struct{ err error }

func (w mysqlDataCodecFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestDataTransferParquetLargeValuesUseBoundedRowGroupsAndCleanCanceledPages(t *testing.T) {
	staging := t.TempDir()
	t.Setenv("TMPDIR", staging)
	f, err := os.Create(filepath.Join(t.TempDir(), "wide.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	e, err := newMySQLDataParquetEncoder(f, []string{"payload"})
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{0, 255, 128, 32}, mysqlsvc.MySQLDataExportFieldMaxBytes/4)
	const count = 35
	for i := 0; i < count; i++ {
		if err = e.Row([]mysqlsvc.MySQLDataValue{{Binary: true, Bytes: data}}); err != nil {
			e.Abort()
			t.Fatal(err)
		}
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	info, _ := f.Stat()
	file, err := parquet.OpenFile(f, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	if len(file.RowGroups()) < 3 {
		t.Fatal("large values were retained in one row group", len(file.RowGroups()))
	}
	for _, group := range file.RowGroups() {
		if group.NumRows() > 16 {
			t.Fatal("row group exceeds value byte budget", group.NumRows())
		}
	}
	r, err := mysqlDataOpenParquetRows(f, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < count; i++ {
		row, err := r.Next(context.Background())
		if err != nil || !bytes.Equal(row[0].Bytes, data) {
			t.Fatal(i, err)
		}
	}
	if _, err = r.Next(context.Background()); err != io.EOF {
		t.Fatal(err)
	}
	for _, cancel := range []bool{false, true} {
		failure := errors.New("synthetic disk write failure")
		var out io.Writer = mysqlDataCodecFailWriter{failure}
		ctx := context.Background()
		if cancel {
			var stop context.CancelFunc
			ctx, stop = context.WithCancel(ctx)
			stop()
			out = io.Discard
		}
		e, err := newMySQLDataParquetEncoder(out, []string{"payload"})
		if err != nil {
			t.Fatal(err)
		}
		if err = e.Row([]mysqlsvc.MySQLDataValue{{Binary: true, Bytes: data}}); err != nil {
			t.Fatal(err)
		}
		err = e.CloseContext(ctx)
		if cancel && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if !cancel && !errors.Is(err, failure) {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("codec retained temporary files/pages", entries)
	}
}

func TestDataTransferDBFAndParquetReadEntireLargeFile(t *testing.T) {
	const count = 11001
	for _, format := range []string{"DBF", "Parquet"} {
		t.Run(format, func(t *testing.T) {
			f, err := os.Create(filepath.Join(t.TempDir(), "large."+strings.ToLower(format)))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			fields := []string{"id", "a", "b", "c", "d", "e", "f", "g", "h"}
			e, err := newMySQLDataTransferEncoder(f, format, "db", "t", fields)
			if err != nil {
				t.Fatal(err)
			}
			note := strings.Repeat("中文", 33) + " tail "
			for i := 0; i < count; i++ {
				row := make([]mysqlsvc.MySQLDataValue, len(fields))
				row[0].Text = strconv.Itoa(i)
				for j := 1; j < len(row); j++ {
					row[j].Text = note
				}
				if err = e.Row(row); err != nil {
					t.Fatal(i, err)
				}
			}
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			info, _ := f.Stat()
			if info.Size() <= 16<<20 {
				t.Fatal("fixture below old total limit", info.Size())
			}
			if _, err = f.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			var r mysqlDataImportRowReader
			if format == "DBF" {
				r, err = mysqlDataOpenDBFRows(f)
			} else {
				r, err = mysqlDataOpenParquetRows(f, info.Size())
			}
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			for i := 0; i < count; i++ {
				row, err := r.Next(context.Background())
				if err != nil || row[0].Text != strconv.Itoa(i) {
					t.Fatal(i, err)
				}
				for j := 1; j < len(row); j++ {
					if row[j].Text != note {
						t.Fatal(i, j, "text changed")
					}
				}
			}
			if _, err = r.Next(context.Background()); err != io.EOF {
				t.Fatal(err)
			}
			t.Logf("%s input=%d bytes rows=%d", format, info.Size(), count)
		})
	}
}
