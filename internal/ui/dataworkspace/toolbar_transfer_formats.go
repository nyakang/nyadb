package dataworkspace

import (
	"archive/zip"
	"bytes"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/tabular"
	xhtml "golang.org/x/net/html"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

type mysqlDataAdditionalEncoder interface {
	Row([]contract.DataValue) error
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

type mysqlDataHTMLEncoder = tabular.HTML

func newMySQLDataHTMLEncoder(w io.Writer, fields []string) (*mysqlDataHTMLEncoder, error) {
	return tabular.NewHTML(w, fields)
}
func newMySQLDataHTMLEncoderEncoding(w io.Writer, fields []string, encoding string) (*mysqlDataHTMLEncoder, error) {
	return tabular.NewHTMLEncoding(w, fields, encoding)
}
func mysqlDataHTMLText(text string) string { return tabular.HTMLText(text) }

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
			return nil, contract.ErrDataEditLimit
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
			return nil, contract.ErrDataEditLimit
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
	if len(rows) > contract.MaxDataImportRows+1 {
		return nil, contract.ErrDataEditLimit
	}
	for i, row := range rows {
		var values []contract.DataValue
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
			value := contract.DataValue{Text: text.String()}
			switch kind {
			case "null":
				value = contract.DataValue{Null: true}
			case "binary":
				if !strings.HasPrefix(value.Text, "0x") {
					return nil, contract.ErrDataPage
				}
				b, err := hex.DecodeString(value.Text[2:])
				if err != nil {
					return nil, err
				}
				value = contract.DataValue{Binary: true, Bytes: b}
			case "text":
			default:
				return nil, contract.ErrDataPage
			}
			values = append(values, value)
		}
		if i == 0 {
			for _, value := range values {
				input.Fields = append(input.Fields, value.Text)
			}
		} else {
			if len(values) != len(input.Fields) {
				return nil, contract.ErrDataPage
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

type mysqlDataXLSXEncoder = tabular.XLSX

func newMySQLDataXLSXEncoder(w io.Writer, fields []string) (*mysqlDataXLSXEncoder, error) {
	return tabular.NewXLSX(w, fields)
}
func mysqlDataXLSXColumn(index int) string { return tabular.XLSXColumn(index) }

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
			return 0, 0, contract.ErrDataEditLimit
		}
	}
	row, err := strconv.Atoi(ref[at:])
	if err != nil || column == 0 || row <= 0 || row > 1048576 {
		return 0, 0, contract.ErrDataPage
	}
	return column - 1, row, nil
}

func mysqlDataParseXLSXImport(data []byte) (*mysqlDataImportInput, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	if len(z.File) > 4096 {
		return nil, contract.ErrDataEditLimit
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
			return nil, contract.ErrDataEditLimit
		}
		uncompressed += f.UncompressedSize64
		if uncompressed > 64<<20 {
			return nil, contract.ErrDataEditLimit
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
		return nil, contract.ErrDataPage
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
		return nil, contract.ErrDataPage
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
	if len(shared.Items) > contract.MaxDataImportRows*128+128 {
		return nil, contract.ErrDataEditLimit
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
			return nil, contract.ErrDataPage
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
			return nil, contract.ErrDataPage
		}
		lastRow = row.Index
		if len(row.Cells) == 0 {
			continue
		}
		values := make([]contract.DataValue, 128)
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
				return nil, contract.ErrDataPage
			}
			seen[column], maxColumn = true, max(maxColumn, column+1)
			if cell.Formula != nil {
				return nil, errors.New("XLSX formula cells are not supported; export their values first")
			}
			value := contract.DataValue{}
			switch cell.Type {
			case "inlineStr":
				value.Text = cell.Inline.value()
			case "s":
				at, err := strconv.Atoi(cell.Value)
				if err != nil || at < 0 || at >= len(shared.Items) {
					return nil, contract.ErrDataPage
				}
				value.Text = shared.Items[at].value()
			case "str", "d":
				value.Text = cell.Value
			case "b":
				if cell.Value != "0" && cell.Value != "1" {
					return nil, contract.ErrDataPage
				}
				value.Text = cell.Value
			case "", "n":
				if cell.Value == "" {
					value.Null = true
				} else if !mysqlDataImportNumber.MatchString(cell.Value) {
					return nil, contract.ErrDataPage
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
					value = contract.DataValue{Null: true}
				} else if code == mysqlDataXLSXBinaryFormat {
					if !strings.HasPrefix(value.Text, "0x") {
						return nil, contract.ErrDataPage
					}
					b, err := hex.DecodeString(value.Text[2:])
					if err != nil {
						return nil, err
					}
					value = contract.DataValue{Binary: true, Bytes: b}
				} else if !value.Null && (cell.Type == "n" || cell.Type == "") && mysqlDataXLSXDateFormat(formatID, code) {
					value.Text, err = mysqlDataXLSXDate(cell.Value, workbook.Properties.Date1904)
					if err != nil {
						return nil, err
					}
				}
			} else if cell.Style != 0 {
				return nil, contract.ErrDataPage
			}
			if !utf8.ValidString(value.Text) || len(utf16.Encode([]rune(value.Text))) > 32767 {
				return nil, contract.ErrDataEditLimit
			}
			total += len(value.Text) + len(value.Bytes)
			if total > contract.MaxDataImportBytes {
				return nil, contract.ErrDataEditLimit
			}
			values[column] = value
		}
		if len(input.Fields) == 0 {
			for _, v := range values[:maxColumn] {
				if v.Null || v.Binary {
					return nil, contract.ErrDataPage
				}
				input.Fields = append(input.Fields, v.Text)
			}
		} else {
			if maxColumn > len(input.Fields) {
				return nil, contract.ErrDataPage
			}
			input.Rows = append(input.Rows, values[:len(input.Fields)])
			if len(input.Rows) > contract.MaxDataImportRows {
				return nil, contract.ErrDataEditLimit
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
		return "", contract.ErrDataPage
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
