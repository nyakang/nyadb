// Package tabular contains shared typed export encoders, independent of UI and drivers.
package tabular

import (
	"archive/zip"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"html"
	"io"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const spreadsheetNS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"

type HTML struct{ w io.Writer }

func NewHTML(w io.Writer, fields []string) (*HTML, error) {
	return NewHTMLEncoding(w, fields, "UTF-8")
}
func NewHTMLEncoding(w io.Writer, fields []string, encoding string) (*HTML, error) {
	if _, err := io.WriteString(w, "<!doctype html>\n<html><head><meta charset=\""+html.EscapeString(encoding)+"\"><title>NyaDB export</title><style>table{border-collapse:collapse}th,td{border:1px solid #ccc;padding:4px 8px;white-space:pre-wrap}td[data-nyadb-kind=null]{color:#777}</style></head><body><table><thead><tr>"); err != nil {
		return nil, err
	}
	for _, field := range fields {
		if _, err := io.WriteString(w, "<th>"+HTMLText(field)+"</th>"); err != nil {
			return nil, err
		}
	}
	_, err := io.WriteString(w, "</tr></thead><tbody>\n")
	return &HTML{w: w}, err
}

func (e *HTML) Row(values []contract.DataValue) error {
	if _, err := io.WriteString(e.w, "<tr>"); err != nil {
		return err
	}
	for _, value := range values {
		kind, text := "text", value.Text
		if value.Null {
			kind, text = "null", "NULL"
		} else if value.Binary {
			kind, text = "binary", "0x"+hex.EncodeToString(value.Bytes)
		}
		if _, err := io.WriteString(e.w, "<td data-nyadb-kind=\""+kind+"\">"+HTMLText(text)+"</td>"); err != nil {
			return err
		}
	}
	_, err := io.WriteString(e.w, "</tr>\n")
	return err
}

func (e *HTML) Close() error {
	_, err := io.WriteString(e.w, "</tbody></table></body></html>\n")
	return err
}

func HTMLText(text string) string {
	return strings.ReplaceAll(html.EscapeString(text), "\r", "&#13;")
}

type XLSX struct {
	zip       *zip.Writer
	worksheet io.Writer
	fields    int
	row       int
	closed    bool
}

// Database values are inline strings: Excel must never round DECIMAL/BIGINT or
// execute a database string as a formula. Private styles retain NULL and binary
// semantics, while the workbook remains standard SpreadsheetML.
func NewXLSX(w io.Writer, fields []string) (*XLSX, error) {
	if len(fields) == 0 || len(fields) > 128 {
		return nil, contract.ErrDataPage
	}
	e := &XLSX{zip: zip.NewWriter(w), fields: len(fields)}
	files := []struct{ name, content string }{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<workbook xmlns="` + spreadsheetNS + `" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`},
		{"xl/styles.xml", `<styleSheet xmlns="` + spreadsheetNS + `"><numFmts count="2"><numFmt numFmtId="164" formatCode="&quot;NyaDB NULL&quot;"/><numFmt numFmtId="165" formatCode="&quot;NyaDB binary&quot;"/></numFmts><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="3"><xf numFmtId="49" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/><xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`},
	}
	for _, f := range files {
		out, err := e.zip.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err = io.WriteString(out, xml.Header+f.content); err != nil {
			return nil, err
		}
	}
	var err error
	e.worksheet, err = e.zip.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		return nil, err
	}
	if _, err = io.WriteString(e.worksheet, xml.Header+`<worksheet xmlns="`+spreadsheetNS+`"><sheetData>`); err != nil {
		return nil, err
	}
	values := make([]contract.DataValue, len(fields))
	for i, field := range fields {
		values[i].Text = field
	}
	if err = e.Row(values); err != nil {
		return nil, err
	}
	return e, nil
}

func XLSXColumn(index int) string {
	var result string
	for index++; index > 0; index = (index - 1) / 26 {
		result = string(rune('A'+(index-1)%26)) + result
	}
	return result
}

func (e *XLSX) Row(values []contract.DataValue) error {
	if e.closed || len(values) != e.fields || e.row >= 1048576 {
		return contract.ErrDataEditLimit
	}
	// Excel limits string cells to 32767 UTF-16 code units. Reject unsupported
	// control characters before xml.EscapeText can replace them silently.
	texts := make([]string, len(values))
	for i, value := range values {
		texts[i] = value.Text
		if value.Null {
			texts[i] = ""
		} else if value.Binary {
			texts[i] = "0x" + hex.EncodeToString(value.Bytes)
		}
		if !XMLTextValid(texts[i]) || len(utf16.Encode([]rune(texts[i]))) > 32767 {
			return errors.New("XLSX 单元格最大 32767 字符；超限会失败并保留原文件。")
		}
	}
	e.row++
	if _, err := fmt.Fprintf(e.worksheet, `<row r="%d">`, e.row); err != nil {
		return err
	}
	for i, value := range values {
		style := 0
		if value.Null {
			style = 1
		} else if value.Binary {
			style = 2
		}
		if _, err := fmt.Fprintf(e.worksheet, `<c r="%s%d" t="inlineStr" s="%d"><is><t xml:space="preserve">`, XLSXColumn(i), e.row, style); err != nil {
			return err
		}
		if err := xml.EscapeText(e.worksheet, []byte(XLSXEncodeText(texts[i]))); err != nil {
			return err
		}
		if _, err := io.WriteString(e.worksheet, `</t></is></c>`); err != nil {
			return err
		}
	}
	_, err := io.WriteString(e.worksheet, `</row>`)
	return err
}

func (e *XLSX) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	if _, err := io.WriteString(e.worksheet, `</sheetData></worksheet>`); err != nil {
		return err
	}
	return e.zip.Close()
}

var xlsxEscape = regexp.MustCompile(`_x[0-9a-fA-F]{4}_`)

func XLSXEncodeText(text string) string {
	return xlsxEscape.ReplaceAllStringFunc(text, func(match string) string { return "_x005F_" + match[1:] })
}

func XMLTextValid(text string) bool {
	if !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if !(r == 9 || r == 10 || r == 13 || r >= 0x20 && r <= 0xd7ff || r >= 0xe000 && r <= 0xfffd || r >= 0x10000 && r <= 0x10ffff) {
			return false
		}
	}
	return true
}

// Names outside this conservative Unicode NCName subset use FIELD/name, so
// qualified names, spaces and punctuation in database field names remain exact.
