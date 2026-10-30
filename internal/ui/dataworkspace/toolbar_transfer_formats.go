package dataworkspace

import (
	"archive/zip"
	"bytes"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
	xhtml "golang.org/x/net/html"
)

type mysqlDataAdditionalEncoder interface {
	Row([]mysqlsvc.MySQLDataValue) error
	Close() error
}

func mysqlDataTransferFormatChoices(kind string) []string {
	choices := []string{"TXT", "CSV", "TSV", "JSON", "XML", "HTML", "XLS", "XLSX", "DBF", "Parquet"}
	if kind == "export" {
		choices = append(choices, "SQL")
	} else {
		choices = append(choices, "Access", "ODBC", "Paradox")
	}
	return choices
}

type mysqlDataHTMLEncoder struct{ w io.Writer }

func newMySQLDataHTMLEncoder(w io.Writer, fields []string) (*mysqlDataHTMLEncoder, error) {
	return newMySQLDataHTMLEncoderEncoding(w, fields, "UTF-8")
}
func newMySQLDataHTMLEncoderEncoding(w io.Writer, fields []string, encoding string) (*mysqlDataHTMLEncoder, error) {
	if _, err := io.WriteString(w, "<!doctype html>\n<html><head><meta charset=\""+html.EscapeString(encoding)+"\"><title>NyaDB export</title><style>table{border-collapse:collapse}th,td{border:1px solid #ccc;padding:4px 8px;white-space:pre-wrap}td[data-nyadb-kind=null]{color:#777}</style></head><body><table><thead><tr>"); err != nil {
		return nil, err
	}
	for _, field := range fields {
		if _, err := io.WriteString(w, "<th>"+mysqlDataHTMLText(field)+"</th>"); err != nil {
			return nil, err
		}
	}
	_, err := io.WriteString(w, "</tr></thead><tbody>\n")
	return &mysqlDataHTMLEncoder{w: w}, err
}

func (e *mysqlDataHTMLEncoder) Row(values []mysqlsvc.MySQLDataValue) error {
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
		if _, err := io.WriteString(e.w, "<td data-nyadb-kind=\""+kind+"\">"+mysqlDataHTMLText(text)+"</td>"); err != nil {
			return err
		}
	}
	_, err := io.WriteString(e.w, "</tr>\n")
	return err
}

func (e *mysqlDataHTMLEncoder) Close() error {
	_, err := io.WriteString(e.w, "</tbody></table></body></html>\n")
	return err
}

func mysqlDataHTMLText(text string) string {
	return strings.ReplaceAll(html.EscapeString(text), "\r", "&#13;")
}

// HTML is parsed as inert markup. Only the first table's cells are read; no
// scripts, URL requests, forms or nested tables are evaluated.
func mysqlDataParseHTMLImport(data []byte) (*mysqlDataImportInput, error) {
	// Bound DOM growth and traversal depth independently of the byte limit.
	// A compact file can otherwise contain millions of empty elements.
	tokens := xhtml.NewTokenizer(bytes.NewReader(data))
	depth, count := 0, 0
	for {
		typ := tokens.Next()
		if typ == xhtml.ErrorToken {
			if tokens.Err() != io.EOF {
				return nil, tokens.Err()
			}
			break
		}
		count++
		if count > 262144 {
			return nil, mysqlsvc.ErrMySQLDataEditLimit
		}
		if typ == xhtml.StartTagToken {
			name, _ := tokens.TagName()
			switch string(name) {
			case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
			default:
				depth++
			}
		} else if typ == xhtml.EndTagToken {
			depth = max(0, depth-1)
		}
		if depth > 256 {
			return nil, mysqlsvc.ErrMySQLDataEditLimit
		}
	}
	doc, err := xhtml.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var table *xhtml.Node
	var find func(*xhtml.Node)
	find = func(n *xhtml.Node) {
		if table != nil {
			return
		}
		if n.Type == xhtml.ElementNode && n.Data == "table" {
			table = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	if table == nil {
		return nil, errors.New("HTML contains no table")
	}
	input := &mysqlDataImportInput{}
	var rows []*xhtml.Node
	var collect func(*xhtml.Node)
	collect = func(n *xhtml.Node) {
		if n != table && n.Type == xhtml.ElementNode && n.Data == "table" {
			return
		}
		if n.Type == xhtml.ElementNode && n.Data == "tr" {
			rows = append(rows, n)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	collect(table)
	if len(rows) > mysqlsvc.MaxMySQLDataImportRows+1 {
		return nil, mysqlsvc.ErrMySQLDataEditLimit
	}
	for i, row := range rows {
		var values []mysqlsvc.MySQLDataValue
		for c := row.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != xhtml.ElementNode || c.Data != "td" && c.Data != "th" {
				continue
			}
			kind := "text"
			for _, attr := range c.Attr {
				if (attr.Key == "colspan" || attr.Key == "rowspan") && attr.Val != "1" {
					return nil, errors.New("HTML merged cells are not supported")
				}
				if attr.Key == "data-nyadb-kind" {
					kind = attr.Val
				}
			}
			var text strings.Builder
			var read func(*xhtml.Node)
			read = func(n *xhtml.Node) {
				if n.Type == xhtml.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "table") {
					return
				}
				if n.Type == xhtml.TextNode {
					text.WriteString(n.Data)
				}
				if n.Type == xhtml.ElementNode && n.Data == "br" {
					text.WriteByte('\n')
				}
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					read(child)
				}
			}
			read(c)
			value := mysqlsvc.MySQLDataValue{Text: text.String()}
			switch kind {
			case "null":
				value = mysqlsvc.MySQLDataValue{Null: true}
			case "binary":
				if !strings.HasPrefix(value.Text, "0x") {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				b, err := hex.DecodeString(value.Text[2:])
				if err != nil {
					return nil, err
				}
				value = mysqlsvc.MySQLDataValue{Binary: true, Bytes: b}
			case "text":
			default:
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			values = append(values, value)
		}
		if i == 0 {
			for _, value := range values {
				input.Fields = append(input.Fields, value.Text)
			}
		} else {
			if len(values) != len(input.Fields) {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			input.Rows = append(input.Rows, values)
		}
	}
	return input, nil
}

const mysqlDataSpreadsheetNS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
const mysqlDataXLSXNullFormat = `"NyaDB NULL"`
const mysqlDataXLSXBinaryFormat = `"NyaDB binary"`

var mysqlDataXLSXEscape = regexp.MustCompile(`_x[0-9a-fA-F]{4}_`)

func mysqlDataXLSXEncodeText(text string) string {
	return mysqlDataXLSXEscape.ReplaceAllStringFunc(text, func(match string) string { return "_x005F_" + match[1:] })
}

func mysqlDataXLSXDecodeText(text string) string {
	var out strings.Builder
	for len(text) > 0 {
		loc := mysqlDataXLSXEscape.FindStringIndex(text)
		if loc == nil {
			out.WriteString(text)
			break
		}
		out.WriteString(text[:loc[0]])
		code, _ := strconv.ParseUint(text[loc[0]+2:loc[1]-1], 16, 16)
		text = text[loc[1]:]
		r := rune(code)
		if r >= 0xd800 && r <= 0xdbff && strings.HasPrefix(text, "_x") {
			if next := mysqlDataXLSXEscape.FindStringIndex(text); next != nil && next[0] == 0 {
				second, _ := strconv.ParseUint(text[2:next[1]-1], 16, 16)
				if second >= 0xdc00 && second <= 0xdfff {
					r = utf16.DecodeRune(r, rune(second))
					text = text[next[1]:]
				}
			}
		}
		out.WriteRune(r)
	}
	return out.String()
}

type mysqlDataXLSXEncoder struct {
	zip       *zip.Writer
	worksheet io.Writer
	fields    int
	row       int
	closed    bool
}

// Database values are inline strings: Excel must never round DECIMAL/BIGINT or
// execute a database string as a formula. Private styles retain NULL and binary
// semantics, while the workbook remains standard SpreadsheetML.
func newMySQLDataXLSXEncoder(w io.Writer, fields []string) (*mysqlDataXLSXEncoder, error) {
	if len(fields) == 0 || len(fields) > 128 {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	e := &mysqlDataXLSXEncoder{zip: zip.NewWriter(w), fields: len(fields)}
	files := []struct{ name, content string }{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<workbook xmlns="` + mysqlDataSpreadsheetNS + `" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`},
		{"xl/styles.xml", `<styleSheet xmlns="` + mysqlDataSpreadsheetNS + `"><numFmts count="2"><numFmt numFmtId="164" formatCode="&quot;NyaDB NULL&quot;"/><numFmt numFmtId="165" formatCode="&quot;NyaDB binary&quot;"/></numFmts><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="3"><xf numFmtId="49" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/><xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`},
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
	if _, err = io.WriteString(e.worksheet, xml.Header+`<worksheet xmlns="`+mysqlDataSpreadsheetNS+`"><sheetData>`); err != nil {
		return nil, err
	}
	values := make([]mysqlsvc.MySQLDataValue, len(fields))
	for i, field := range fields {
		values[i].Text = field
	}
	if err = e.Row(values); err != nil {
		return nil, err
	}
	return e, nil
}

func mysqlDataXLSXColumn(index int) string {
	var result string
	for index++; index > 0; index = (index - 1) / 26 {
		result = string(rune('A'+(index-1)%26)) + result
	}
	return result
}

func (e *mysqlDataXLSXEncoder) Row(values []mysqlsvc.MySQLDataValue) error {
	if e.closed || len(values) != e.fields || e.row >= 1048576 {
		return mysqlsvc.ErrMySQLDataEditLimit
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
		if !mysqlDataXMLTextValid(texts[i]) || len(utf16.Encode([]rune(texts[i]))) > 32767 {
			return errors.New(locales.Text("XLSX 单元格最大 32767 字符；超限会失败并保留原文件。"))
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
		if _, err := fmt.Fprintf(e.worksheet, `<c r="%s%d" t="inlineStr" s="%d"><is><t xml:space="preserve">`, mysqlDataXLSXColumn(i), e.row, style); err != nil {
			return err
		}
		if err := xml.EscapeText(e.worksheet, []byte(mysqlDataXLSXEncodeText(texts[i]))); err != nil {
			return err
		}
		if _, err := io.WriteString(e.worksheet, `</t></is></c>`); err != nil {
			return err
		}
	}
	_, err := io.WriteString(e.worksheet, `</row>`)
	return err
}

func (e *mysqlDataXLSXEncoder) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	if _, err := io.WriteString(e.worksheet, `</sheetData></worksheet>`); err != nil {
		return err
	}
	return e.zip.Close()
}

type mysqlDataXLSXRichText struct {
	Text string `xml:"t"`
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

func (t mysqlDataXLSXRichText) value() string {
	if len(t.Runs) == 0 {
		return mysqlDataXLSXDecodeText(t.Text)
	}
	var out strings.Builder
	for _, r := range t.Runs {
		out.WriteString(r.Text)
	}
	return mysqlDataXLSXDecodeText(out.String())
}

type mysqlDataXLSXCell struct {
	Ref     string                `xml:"r,attr"`
	Type    string                `xml:"t,attr"`
	Style   int                   `xml:"s,attr"`
	Formula *string               `xml:"f"`
	Value   string                `xml:"v"`
	Inline  mysqlDataXLSXRichText `xml:"is"`
}

func mysqlDataXLSXCellIndex(ref string) (int, int, error) {
	column, at := 0, 0
	for at < len(ref) && ref[at] >= 'A' && ref[at] <= 'Z' {
		column = column*26 + int(ref[at]-'A') + 1
		at++
		if column > 128 {
			return 0, 0, mysqlsvc.ErrMySQLDataEditLimit
		}
	}
	row, err := strconv.Atoi(ref[at:])
	if err != nil || column == 0 || row <= 0 || row > 1048576 {
		return 0, 0, mysqlsvc.ErrMySQLDataPage
	}
	return column - 1, row, nil
}

func mysqlDataParseXLSXImport(data []byte) (*mysqlDataImportInput, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	if len(z.File) > 4096 {
		return nil, mysqlsvc.ErrMySQLDataEditLimit
	}
	files := make(map[string]*zip.File, len(z.File))
	var uncompressed uint64
	for _, f := range z.File {
		name := f.Name
		if f.FileInfo().IsDir() {
			name = strings.TrimSuffix(name, "/")
		}
		if name != path.Clean(name) || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.HasPrefix(name, "../") || strings.Contains(name, ":") || files[name] != nil {
			return nil, errors.New("XLSX contains unsafe or duplicate ZIP paths")
		}
		if f.UncompressedSize64 > 32<<20 {
			return nil, mysqlsvc.ErrMySQLDataEditLimit
		}
		uncompressed += f.UncompressedSize64
		if uncompressed > 64<<20 {
			return nil, mysqlsvc.ErrMySQLDataEditLimit
		}
		files[name] = f
	}
	read := func(name string, target any, optional bool) error {
		f := files[name]
		if f == nil {
			if optional {
				return nil
			}
			return fmt.Errorf("XLSX missing %s", name)
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		defer r.Close()
		dec := xml.NewDecoder(io.LimitReader(r, 32<<20+1))
		return dec.Decode(target)
	}
	// Reject external resources anywhere in the package, without accessing them.
	for name := range files {
		if !strings.HasSuffix(name, ".rels") {
			continue
		}
		var rels struct {
			Items []struct {
				ID     string `xml:"Id,attr"`
				Target string `xml:"Target,attr"`
				Mode   string `xml:"TargetMode,attr"`
			} `xml:"Relationship"`
		}
		if err := read(name, &rels, false); err != nil {
			return nil, err
		}
		for _, rel := range rels.Items {
			if rel.Mode == "External" {
				return nil, errors.New("XLSX external relationships are not supported")
			}
		}
	}
	var workbook struct {
		Properties struct {
			Date1904 bool `xml:"date1904,attr"`
		} `xml:"workbookPr"`
		Sheets []struct {
			ID string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err = read("xl/workbook.xml", &workbook, false); err != nil {
		return nil, err
	}
	if len(workbook.Sheets) == 0 {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	var rels struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
			Type   string `xml:"Type,attr"`
		} `xml:"Relationship"`
	}
	if err = read("xl/_rels/workbook.xml.rels", &rels, false); err != nil {
		return nil, err
	}
	sheet := ""
	for _, rel := range rels.Items {
		if rel.ID == workbook.Sheets[0].ID && strings.HasSuffix(rel.Type, "/worksheet") {
			if strings.HasPrefix(rel.Target, "/") {
				sheet = strings.TrimPrefix(rel.Target, "/")
			} else {
				sheet = path.Join("xl", rel.Target)
			}
		}
	}
	if !strings.HasPrefix(sheet, "xl/") || files[sheet] == nil {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	var styles struct {
		Formats []struct {
			ID   int    `xml:"numFmtId,attr"`
			Code string `xml:"formatCode,attr"`
		} `xml:"numFmts>numFmt"`
		Cells []struct {
			ID int `xml:"numFmtId,attr"`
		} `xml:"cellXfs>xf"`
	}
	if err = read("xl/styles.xml", &styles, true); err != nil {
		return nil, err
	}
	formatCodes := map[int]string{}
	for _, f := range styles.Formats {
		formatCodes[f.ID] = f.Code
	}
	var shared struct {
		Items []mysqlDataXLSXRichText `xml:"si"`
	}
	if err = read("xl/sharedStrings.xml", &shared, true); err != nil {
		return nil, err
	}
	if len(shared.Items) > mysqlsvc.MaxMySQLDataImportRows*128+128 {
		return nil, mysqlsvc.ErrMySQLDataEditLimit
	}
	f, err := files[sheet].Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := xml.NewDecoder(io.LimitReader(f, 32<<20+1))
	input := &mysqlDataImportInput{}
	lastRow, total := 0, 0
	for {
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if _, ok := token.(xml.Directive); ok {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "row" {
			continue
		}
		var row struct {
			Index int                 `xml:"r,attr"`
			Cells []mysqlDataXLSXCell `xml:"c"`
		}
		if err = dec.DecodeElement(&row, &start); err != nil {
			return nil, err
		}
		if row.Index == 0 {
			row.Index = lastRow + 1
		}
		if row.Index <= lastRow || row.Index > 1048576 || len(row.Cells) > 128 {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		lastRow = row.Index
		if len(row.Cells) == 0 {
			continue
		}
		values := make([]mysqlsvc.MySQLDataValue, 128)
		for i := range values {
			values[i].Null = true
		}
		seen, maxColumn := map[int]bool{}, 0
		for index, cell := range row.Cells {
			column, cellRow := index, row.Index
			if cell.Ref != "" {
				column, cellRow, err = mysqlDataXLSXCellIndex(cell.Ref)
				if err != nil {
					return nil, err
				}
			}
			if cellRow != row.Index || seen[column] || cell.Style < 0 {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			seen[column], maxColumn = true, max(maxColumn, column+1)
			if cell.Formula != nil {
				return nil, errors.New("XLSX formula cells are not supported; export their values first")
			}
			value := mysqlsvc.MySQLDataValue{}
			switch cell.Type {
			case "inlineStr":
				value.Text = cell.Inline.value()
			case "s":
				at, err := strconv.Atoi(cell.Value)
				if err != nil || at < 0 || at >= len(shared.Items) {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				value.Text = shared.Items[at].value()
			case "str", "d":
				value.Text = cell.Value
			case "b":
				if cell.Value != "0" && cell.Value != "1" {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				value.Text = cell.Value
			case "", "n":
				if cell.Value == "" {
					value.Null = true
				} else if !mysqlDataImportNumber.MatchString(cell.Value) {
					return nil, mysqlsvc.ErrMySQLDataPage
				} else {
					value.Text = cell.Value
				}
			default:
				return nil, errors.New("XLSX unsupported or error cell")
			}
			if cell.Style < len(styles.Cells) {
				formatID := styles.Cells[cell.Style].ID
				code := formatCodes[formatID]
				if code == mysqlDataXLSXNullFormat && value.Text == "" {
					value = mysqlsvc.MySQLDataValue{Null: true}
				} else if code == mysqlDataXLSXBinaryFormat {
					if !strings.HasPrefix(value.Text, "0x") {
						return nil, mysqlsvc.ErrMySQLDataPage
					}
					b, err := hex.DecodeString(value.Text[2:])
					if err != nil {
						return nil, err
					}
					value = mysqlsvc.MySQLDataValue{Binary: true, Bytes: b}
				} else if !value.Null && (cell.Type == "n" || cell.Type == "") && mysqlDataXLSXDateFormat(formatID, code) {
					value.Text, err = mysqlDataXLSXDate(cell.Value, workbook.Properties.Date1904)
					if err != nil {
						return nil, err
					}
				}
			} else if cell.Style != 0 {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			if !utf8.ValidString(value.Text) || len(utf16.Encode([]rune(value.Text))) > 32767 {
				return nil, mysqlsvc.ErrMySQLDataEditLimit
			}
			total += len(value.Text) + len(value.Bytes)
			if total > mysqlsvc.MaxMySQLDataImportBytes {
				return nil, mysqlsvc.ErrMySQLDataEditLimit
			}
			values[column] = value
		}
		if len(input.Fields) == 0 {
			for _, v := range values[:maxColumn] {
				if v.Null || v.Binary {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				input.Fields = append(input.Fields, v.Text)
			}
		} else {
			if maxColumn > len(input.Fields) {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			input.Rows = append(input.Rows, values[:len(input.Fields)])
			if len(input.Rows) > mysqlsvc.MaxMySQLDataImportRows {
				return nil, mysqlsvc.ErrMySQLDataEditLimit
			}
		}
	}
	return input, nil
}

func mysqlDataXLSXDateFormat(id int, code string) bool {
	if id >= 14 && id <= 22 || id >= 45 && id <= 47 {
		return true
	}
	// Quoted literals and escaped text are not date tokens.
	quoted, escaped := false, false
	for _, r := range strings.ToLower(code) {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			continue
		}
		if !quoted && (r == 'y' || r == 'd' || r == 'h' || r == 's') {
			return true
		}
	}
	return false
}

func mysqlDataXLSXDate(raw string, date1904 bool) (string, error) {
	serial, err := strconv.ParseFloat(raw, 64)
	if err != nil || serial < 0 || serial > 2958465 {
		return "", mysqlsvc.ErrMySQLDataPage
	}
	days := int(serial)
	if !date1904 && days == 60 {
		return "", errors.New("Excel's fictional 1900-02-29 cannot be imported")
	}
	base := time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)
	if date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	} else if days > 60 {
		days--
	}
	nanos := int64((serial-float64(int(serial)))*float64(24*time.Hour) + 0.5)
	value := base.AddDate(0, 0, days).Add(time.Duration(nanos)).Round(time.Microsecond)
	if nanos == 0 {
		return value.Format("2006-01-02"), nil
	}
	return value.Format("2006-01-02 15:04:05.999999"), nil
}
