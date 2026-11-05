package tabular

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"github.com/nyakang/nyadb/internal/db/contract"
	"io"
	"strings"
	"testing"
)

func TestCachedExportsPreserveExactValuesNullBinaryAndInertText(t *testing.T) {
	set := contract.ResultSet{Columns: []contract.Column{{Name: "exact"}, {Name: "null"}, {Name: "binary"}, {Name: "formula"}}, Rows: [][]contract.Cell{{{Kind: contract.Decimal, Text: "9007199254740993.123456789"}, {Kind: contract.Null}, {Kind: contract.Binary, Bytes: []byte{0, 255}}, {Kind: contract.Text, Text: "=1+1<script>&"}}}, Truncation: []string{"rows"}}
	raw, e := EncodeAdditional(set, "XLSX")
	if e != nil {
		t.Fatal(e)
	}
	z, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		t.Fatal(e)
	}
	sheet := ""
	for _, f := range z.File {
		if f.Name == "xl/worksheets/sheet1.xml" {
			r, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			b, e := io.ReadAll(r)
			r.Close()
			if e != nil {
				t.Fatal(e)
			}
			sheet = string(b)
		}
	}
	if !strings.Contains(sheet, "9007199254740993.123456789") || !strings.Contains(sheet, "0x00ff") || !strings.Contains(sheet, `s="1"`) || strings.Contains(sheet, "<f>") || !strings.Contains(sheet, "&lt;script&gt;") {
		t.Fatal("XLSX changed or executed values", sheet)
	}
	raw, e = EncodeAdditional(set, "HTML")
	if e != nil || strings.Contains(string(raw), "<script>") || !strings.Contains(string(raw), "9007199254740993.123456789") {
		t.Fatal("HTML changed or activated values", e)
	}
	raw, e = EncodeAdditional(set, "XML")
	if e != nil {
		t.Fatal(e)
	}
	d := xml.NewDecoder(bytes.NewReader(raw))
	for {
		_, e = d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	if !strings.Contains(string(raw), `kind="decimal"`) || !strings.Contains(string(raw), "<reason>rows</reason>") || !strings.Contains(string(raw), `kind="null"`) {
		t.Fatal("XML lost typed/truncation metadata")
	}
	set.Rows[0][0].Text = "bad\x00value"
	if _, e = EncodeAdditional(set, "XLSX"); e == nil {
		t.Fatal("XML-invalid value was silently changed")
	}
}
