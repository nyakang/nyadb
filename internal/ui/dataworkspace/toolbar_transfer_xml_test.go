package dataworkspace

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestDataTransferXMLRoundTripPreservesNamesTextNullAndBinary(t *testing.T) {
	fields := []string{"字段", "empty", "null", "payload", "a <&\" name"}
	values := []mysqlsvc.MySQLDataValue{{Text: "9007199254740993.0100\r\n中文 & < > \" '\t"}, {Text: ""}, {Null: true}, {Binary: true, Bytes: []byte{0, 1, 255}}, {Text: `\N`}}
	var output bytes.Buffer
	encoder, err := newMySQLDataTransferEncoder(&output, "XML", "db", "t", fields)
	if err != nil {
		t.Fatal(err)
	}
	if err = encoder.Row(values); err != nil {
		t.Fatal(err)
	}
	if err = encoder.Close(); err != nil {
		t.Fatal(err)
	}
	input, err := mysqlDataParseImport(bytes.NewReader(output.Bytes()), "XML")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input.Fields, fields) || !reflect.DeepEqual(input.Rows[0], values) {
		t.Fatalf("XML changed values: %+v\n%s", input, output.String())
	}
	if !strings.Contains(output.String(), `xsi:nil="true"`) || !strings.Contains(output.String(), `encoding="base64"`) || !strings.Contains(output.String(), `<FIELD name=`) {
		t.Fatal("lossless XML markers missing")
	}
	// Empty bytes remain binary rather than becoming empty text or NULL.
	output.Reset()
	encoder, _ = newMySQLDataTransferEncoder(&output, "XML", "db", "t", []string{"payload"})
	_ = encoder.Row([]mysqlsvc.MySQLDataValue{{Binary: true}})
	_ = encoder.Close()
	input, err = mysqlDataParseImport(bytes.NewReader(output.Bytes()), "XML")
	if err != nil || !input.Rows[0][0].Binary || input.Rows[0][0].Null || len(input.Rows[0][0].Bytes) != 0 {
		t.Fatal(input, err)
	}
}
func TestDataTransferXMLObservedNavicatEmptyElementsStayEmptyText(t *testing.T) {
	// O-128: Navicat writes both SQL NULL and the empty string as <details/>.
	source := `<?xml version="1.0" standalone="yes"?><RECORDS><RECORD><id>3</id><note>中文</note><amount/><state>new</state><details/></RECORD><RECORD><id>4</id><note/><amount>0</amount><state>done</state><details/></RECORD></RECORDS>`
	input, err := mysqlDataParseImport(strings.NewReader(source), "XML")
	if err != nil {
		t.Fatal(err)
	}
	if input.Rows[0][2].Null || input.Rows[0][4].Null || input.Rows[1][1].Null || input.Rows[0][4].Text != "" {
		t.Fatal("ambiguous original empty element silently converted to NULL")
	}
}
func TestDataTransferXMLNamespacedNilAndAttributeRecords(t *testing.T) {
	source := `<RECORDS xmlns:n="http://www.w3.org/2001/XMLSchema-instance"><RECORD><note n:nil="true"/><payload encoding="hex">00ff</payload></RECORD><RECORD><payload encoding="base64">AQI=</payload><note>plain base64 AQI=</note></RECORD></RECORDS>`
	input, err := mysqlDataParseImport(strings.NewReader(source), "XML")
	if err != nil {
		t.Fatal(err)
	}
	if !input.Rows[0][0].Null || !reflect.DeepEqual(input.Rows[0][1].Bytes, []byte{0, 255}) || input.Rows[1][0].Text != "plain base64 AQI=" || !reflect.DeepEqual(input.Rows[1][1].Bytes, []byte{1, 2}) {
		t.Fatal(input)
	}
	source = `<RECORDS><RECORD id="1" note="a&amp;b&#xA;c"/><RECORD note="" id="2"/></RECORDS>`
	input, err = mysqlDataParseImport(strings.NewReader(source), "XML")
	if err != nil || input.Rows[0][1].Text != "a&b\nc" || input.Rows[1][1].Null {
		t.Fatal(input, err)
	}
}
func TestDataTransferXMLRejectsEntitiesNestedTreesInconsistentAndInvalidValues(t *testing.T) {
	tests := []string{
		`<!DOCTYPE RECORDS [<!ENTITY x SYSTEM "file:///etc/passwd">]><RECORDS><RECORD><note>&x;</note></RECORD></RECORDS>`,
		`<!DOCTYPE RECORDS [<!ENTITY x "value">]><RECORDS><RECORD><note>&x;</note></RECORD></RECORDS>`,
		`<RECORDS><RECORD><note><nested>value</nested></note></RECORD></RECORDS>`,
		`<RECORDS><RECORD><note>x</note><note>y</note></RECORD></RECORDS>`,
		`<RECORDS><RECORD><a>x</a></RECORD><RECORD><b>y</b></RECORD></RECORDS>`,
		`<RECORDS xmlns:xsi="urn:forged"><RECORD><note xsi:nil="true"/></RECORD></RECORDS>`,
		`<RECORDS xmlns:n="http://www.w3.org/2001/XMLSchema-instance"><RECORD><note n:nil="true">value</note></RECORD></RECORDS>`,
		`<RECORDS><RECORD><payload encoding="base64">!!!</payload></RECORD></RECORDS>`,
		`<RECORDS><RECORD><payload encoding="external">http://example.com/</payload></RECORD></RECORDS>`,
		`<RECORDS><RECORD id="1"><note>x</note></RECORD></RECORDS>`,
		`<RECORDS><?sql DELETE FROM t?><RECORD><note>x</note></RECORD></RECORDS>`,
		`<RECORDS><RECORD><note>&unknown;</note></RECORD></RECORDS>`,
		`<RECORDS><RECORD><note>x</note></RECORD></RECORDS><RECORDS/>`,
		`<RECORDS><RECORD><note>` + string([]byte{1}) + `</note></RECORD></RECORDS>`,
		`<?xml version="1.0" encoding="ISO-8859-1"?><RECORDS><RECORD><note>x</note></RECORD></RECORDS>`,
	}
	for _, source := range tests {
		if _, err := mysqlDataParseImport(strings.NewReader(source), "XML"); err == nil {
			t.Fatalf("accepted unsupported XML %.120q", source)
		}
	}
	tooMany := `<RECORDS>` + strings.Repeat(`<RECORD><id>1</id></RECORD>`, 201) + `</RECORDS>`
	if _, err := mysqlDataParseImport(strings.NewReader(tooMany), "XML"); err == nil {
		t.Fatal("silently truncated XML after 200 rows")
	}
	oversized := `<RECORDS><RECORD><note>` + strings.Repeat("a", mysqlsvc.MySQLDataEditMaxBytes) + `</note></RECORD></RECORDS>`
	if _, err := mysqlDataParseImport(strings.NewReader(oversized), "XML"); err == nil {
		t.Fatal("XML file limit was ignored")
	}
}
func TestDataTransferXMLBinaryMappingOwnsBytesAndRejectsTextTargets(t *testing.T) {
	source := `<RECORDS><RECORD><id>1</id><payload encoding="base64">AAH/</payload></RECORD></RECORDS>`
	input, err := mysqlDataParseImport(strings.NewReader(source), "XML")
	if err != nil {
		t.Fatal(err)
	}
	edits, err := mysqlDataImportEdits(input, []string{"id", "payload"}, transferColumns())
	if err != nil {
		t.Fatal(err)
	}
	input.Rows[0][1].Bytes[0] = 9
	if edits[0].Values["payload"].Bytes[0] != 0 {
		t.Fatal("save worker shared parsed XML bytes")
	}
	if _, err = mysqlDataImportEdits(input, []string{"id", "note"}, transferColumns()); err == nil {
		t.Fatal("binary XML silently converted to text")
	}
}
func TestDataTransferXMLExportErrorPreservesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rows.xml")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	err := mysqlDataAtomicFile(context.Background(), path, func(w io.Writer) error {
		e, err := newMySQLDataTransferEncoder(w, "XML", "db", "t", []string{"note"})
		if err != nil {
			return err
		}
		return e.Row([]mysqlsvc.MySQLDataValue{{Text: "before\x00after"}})
	})
	if err == nil {
		t.Fatal("XML silently replaced invalid characters")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Fatal("incomplete XML destroyed target")
	}
}
func TestDataTransferXMLNativeFormatEntryShowsLosslessPreviewHint(t *testing.T) {
	for _, kind := range []string{"import", "export"} {
		t.Run(kind, func(t *testing.T) {
			s, d, _ := transferTestDocument()
			s.openDataTransfer(d, kind)
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.transferModals(c, d) }, 760, 650)
			if err := tt.Click("CSV"); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click("XML"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if d.transfer.Format != "XML" || !tt.HasText("XML 使用 RECORDS/RECORD；空元素作为空字符串，xsi:nil 表示 NULL，仅显式 encoding=base64/hex 解码二进制。") {
				t.Fatal("XML native format or explicit empty-value hint missing")
			}
		})
	}
}
func TestDataTransferXMLFullExportStreamsBeyondImportPageLimit(t *testing.T) {
	s, d, _ := transferTestDocument()
	s.openDataExport(d)
	d.transfer.Scope = "all"
	d.transfer.Format = "XML"
	path := filepath.Join(t.TempDir(), "all.xml")
	d.transfer.SaveDialog = func(mygo.SaveDialogOptions) (string, error) { return path, nil }
	s.Exporter = func(_ context.Context, _ int64, _, _ string, _ mysqlsvc.MySQLDataOptions, _ []string, emit func([]mysqlsvc.MySQLDataValue) error) error {
		for i := 0; i < 301; i++ {
			if err := emit([]mysqlsvc.MySQLDataValue{{Text: fmt.Sprint(i)}}); err != nil {
				return err
			}
		}
		return nil
	}
	s.startDataExport(ui.Services{}, d)
	waitDataTransfer(t, s, d)
	if d.transfer.Err != "" {
		t.Fatal(d.transfer.Err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	count := 0
	for {
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "RECORD" {
			count++
		}
	}
	if count != 301 {
		t.Fatal("full XML export was truncated", count)
	}
}
