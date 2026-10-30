package dataworkspace

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// Both shared strings and their lookup index live on disk. Worksheet rows are
// read one at a time from ZIP; neither the compressed file, expanded worksheet,
// SST contents nor the complete record set must fit in memory.
type mysqlDataDiskStrings struct {
	file, index *os.File
	count       int64
}

func newMySQLDataDiskStrings() (*mysqlDataDiskStrings, error) {
	f, err := os.CreateTemp("", "nyadb-transfer-strings-*")
	if err != nil {
		return nil, err
	}
	index, err := os.CreateTemp("", "nyadb-transfer-string-index-*")
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return &mysqlDataDiskStrings{file: f, index: index}, nil
}
func (s *mysqlDataDiskStrings) append(text string) error {
	offset, err := s.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	h := make([]byte, 8)
	binary.LittleEndian.PutUint64(h, uint64(offset))
	if _, err = s.index.Write(h); err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(h, uint32(len(text)))
	if _, err = s.file.Write(h[:4]); err != nil {
		return err
	}
	if _, err = io.WriteString(s.file, text); err != nil {
		return err
	}
	s.count++
	return nil
}
func (s *mysqlDataDiskStrings) text(index int64) (string, error) {
	if index < 0 || index >= s.count {
		return "", mysqlsvc.ErrMySQLDataPage
	}
	h := make([]byte, 8)
	if _, err := s.index.ReadAt(h, index*8); err != nil {
		return "", err
	}
	offset := int64(binary.LittleEndian.Uint64(h))
	if _, err := s.file.ReadAt(h[:4], offset); err != nil {
		return "", err
	}
	size := int(binary.LittleEndian.Uint32(h))
	if size > mysqlsvc.MySQLDataExportFieldMaxBytes {
		return "", mysqlsvc.ErrMySQLDataEditLimit
	}
	data := make([]byte, size)
	_, err := s.file.ReadAt(data, offset+4)
	return string(data), err
}
func (s *mysqlDataDiskStrings) close() error {
	if s == nil || s.file == nil {
		return nil
	}
	err := errors.Join(s.file.Close(), s.index.Close(), os.Remove(s.file.Name()), os.Remove(s.index.Name()))
	s.file, s.index = nil, nil
	return err
}

type mysqlDataXLSXRows struct {
	decoder      *xml.Decoder
	window       *mysqlDataRecordWindow
	sheet        io.ReadCloser
	shared       *mysqlDataDiskStrings
	fields       []string
	options      mysqlDataSourceOptions
	date1904     bool
	formats      map[int]string
	styles       []int
	last         int
	pending      []mysqlsvc.MySQLDataValue
	pendingIndex int
	closed       bool
}

func mysqlDataOpenXLSXRows(ctx context.Context, input io.ReaderAt, size int64, options mysqlDataSourceOptions) (r *mysqlDataXLSXRows, err error) {
	z, err := zip.NewReader(input, size)
	if err != nil {
		return nil, err
	}
	if len(z.File) > 4096 {
		return nil, mysqlsvc.ErrMySQLDataEditLimit
	}
	files := map[string]*zip.File{}
	for _, f := range z.File {
		name := f.Name
		if f.FileInfo().IsDir() {
			name = strings.TrimSuffix(name, "/")
		}
		if name != path.Clean(name) || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.HasPrefix(name, "../") || strings.Contains(name, ":") || files[name] != nil {
			return nil, errors.New("XLSX contains unsafe or duplicate ZIP paths")
		}
		files[name] = f
	}
	r = &mysqlDataXLSXRows{options: options, formats: map[int]string{}}
	owned := r
	defer func() {
		if err != nil {
			owned.Close()
		}
	}()
	read := func(name string, target any, optional bool) error {
		f := files[name]
		if f == nil {
			if optional {
				return nil
			}
			return fmt.Errorf("XLSX missing %s", name)
		}
		stream, err := f.Open()
		if err != nil {
			return err
		}
		defer stream.Close()
		window := &mysqlDataRecordWindow{reader: stream, ctx: ctx, left: 32 << 20}
		dec := xml.NewDecoder(window)
		if err = dec.Decode(target); err != nil {
			return err
		}
		var extra any
		if err = dec.Decode(&extra); err != io.EOF {
			return mysqlsvc.ErrMySQLDataPage
		}
		return nil
	}
	for name := range files {
		if !strings.HasSuffix(name, ".rels") {
			continue
		}
		var rels struct {
			Items []struct {
				Mode   string `xml:"TargetMode,attr"`
				Target string `xml:"Target,attr"`
			} `xml:"Relationship"`
		}
		if err = read(name, &rels, false); err != nil {
			return nil, err
		}
		for _, rel := range rels.Items {
			if rel.Mode == "External" {
				return nil, errors.New("XLSX external relationships are not supported")
			}
		}
	}
	var book struct {
		Properties struct {
			Date1904 bool `xml:"date1904,attr"`
		} `xml:"workbookPr"`
		Sheets []struct {
			ID string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err = read("xl/workbook.xml", &book, false); err != nil {
		return nil, err
	}
	r.date1904 = book.Properties.Date1904
	if options.Sheet < 0 || options.Sheet >= len(book.Sheets) {
		return nil, errors.New("XLSX worksheet index is not present")
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
	sheetName := ""
	for _, rel := range rels.Items {
		if rel.ID == book.Sheets[options.Sheet].ID && strings.HasSuffix(rel.Type, "/worksheet") {
			if strings.HasPrefix(rel.Target, "/") {
				sheetName = strings.TrimPrefix(rel.Target, "/")
			} else {
				sheetName = path.Join("xl", rel.Target)
			}
		}
	}
	if !strings.HasPrefix(sheetName, "xl/") || files[sheetName] == nil {
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
	for _, f := range styles.Formats {
		r.formats[f.ID] = f.Code
	}
	for _, cell := range styles.Cells {
		r.styles = append(r.styles, cell.ID)
	}
	if f := files["xl/sharedStrings.xml"]; f != nil {
		r.shared, err = newMySQLDataDiskStrings()
		if err != nil {
			return nil, err
		}
		stream, e := f.Open()
		if e != nil {
			return nil, e
		}
		window := &mysqlDataRecordWindow{reader: stream, ctx: ctx, left: 8 << 20}
		dec := xml.NewDecoder(window)
		for {
			window.left = 8 << 20
			token, e := dec.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				stream.Close()
				return nil, e
			}
			if _, ok := token.(xml.Directive); ok {
				stream.Close()
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			if start, ok := token.(xml.StartElement); ok && start.Name.Local == "si" {
				var text mysqlDataXLSXRichText
				if e = dec.DecodeElement(&text, &start); e != nil {
					stream.Close()
					return nil, e
				}
				value := text.value()
				if len(utf16.Encode([]rune(value))) > 32767 {
					stream.Close()
					return nil, mysqlsvc.ErrMySQLDataEditLimit
				}
				if e = r.shared.append(value); e != nil {
					stream.Close()
					return nil, e
				}
			}
		}
		if e = stream.Close(); e != nil {
			return nil, e
		}
	}
	r.sheet, err = files[sheetName].Open()
	if err != nil {
		return nil, err
	}
	r.window = &mysqlDataRecordWindow{reader: r.sheet, ctx: ctx, left: 40 << 20}
	r.decoder = xml.NewDecoder(r.window)
	for {
		index, row, e := r.raw(ctx)
		if e != nil {
			return nil, e
		}
		if options.HeaderRow == 0 {
			for i := range row {
				r.fields = append(r.fields, fmt.Sprintf("Column%d", i+1))
			}
			r.pending, r.pendingIndex = row, index
			break
		}
		if int64(index) < options.HeaderRow {
			continue
		}
		if int64(index) != options.HeaderRow {
			return nil, errors.New("XLSX selected header row is absent")
		}
		for _, v := range row {
			if v.Null || v.Binary || v.Text == "" {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			r.fields = append(r.fields, v.Text)
		}
		break
	}
	if err = mysqlDataRecordValuesValid(r.fields, make([]mysqlsvc.MySQLDataValue, len(r.fields))); err != nil {
		return nil, err
	}
	return r, nil
}
func (r *mysqlDataXLSXRows) Fields() []string { return slices.Clone(r.fields) }
func (r *mysqlDataXLSXRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	var err error
	if r.sheet != nil {
		err = r.sheet.Close()
		r.sheet = nil
	}
	err = errors.Join(err, r.shared.close())
	r.decoder = nil
	r.window = nil
	r.pending = nil
	return err
}
func (r *mysqlDataXLSXRows) raw(ctx context.Context) (int, []mysqlsvc.MySQLDataValue, error) {
	r.window.ctx = ctx
	r.window.left = 40 << 20
	for {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		token, err := r.decoder.Token()
		if err != nil {
			return 0, nil, err
		}
		if _, ok := token.(xml.Directive); ok {
			return 0, nil, mysqlsvc.ErrMySQLDataPage
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "row" {
			continue
		}
		var row struct {
			Index int                 `xml:"r,attr"`
			Cells []mysqlDataXLSXCell `xml:"c"`
		}
		if err = r.decoder.DecodeElement(&row, &start); err != nil {
			return 0, nil, err
		}
		if row.Index == 0 {
			row.Index = r.last + 1
		}
		if row.Index <= r.last || row.Index > 1048576 || len(row.Cells) > 128 {
			return 0, nil, mysqlsvc.ErrMySQLDataPage
		}
		r.last = row.Index
		if len(row.Cells) == 0 {
			continue
		}
		values := make([]mysqlsvc.MySQLDataValue, 128)
		for i := range values {
			values[i].Null = true
		}
		seen := map[int]bool{}
		maxColumn := 0
		for index, cell := range row.Cells {
			column, cellRow := index, row.Index
			if cell.Ref != "" {
				column, cellRow, err = mysqlDataXLSXCellIndex(cell.Ref)
				if err != nil {
					return 0, nil, err
				}
			}
			if cellRow != row.Index || column >= 128 || seen[column] || cell.Style < 0 {
				return 0, nil, mysqlsvc.ErrMySQLDataPage
			}
			seen[column] = true
			maxColumn = max(maxColumn, column+1)
			if cell.Formula != nil {
				return 0, nil, errors.New("XLSX formula cells require a value-only export")
			}
			value := mysqlsvc.MySQLDataValue{}
			switch cell.Type {
			case "inlineStr":
				value.Text = cell.Inline.value()
			case "s":
				n, e := strconv.ParseInt(cell.Value, 10, 64)
				if e != nil || r.shared == nil {
					return 0, nil, mysqlsvc.ErrMySQLDataPage
				}
				value.Text, err = r.shared.text(n)
				if err != nil {
					return 0, nil, err
				}
			case "str", "d":
				value.Text = cell.Value
			case "b":
				if cell.Value != "0" && cell.Value != "1" {
					return 0, nil, mysqlsvc.ErrMySQLDataPage
				}
				value.Text = cell.Value
			case "", "n":
				if cell.Value == "" {
					value.Null = true
				} else {
					if !mysqlDataImportNumber.MatchString(cell.Value) {
						return 0, nil, mysqlsvc.ErrMySQLDataPage
					}
					value.Text = cell.Value
				}
			default:
				return 0, nil, errors.New("XLSX unsupported/error cell")
			}
			if cell.Style < len(r.styles) {
				id := r.styles[cell.Style]
				code := r.formats[id]
				if code == mysqlDataXLSXNullFormat && value.Text == "" {
					value = mysqlsvc.MySQLDataValue{Null: true}
				} else if code == mysqlDataXLSXBinaryFormat {
					if !strings.HasPrefix(value.Text, "0x") {
						return 0, nil, mysqlsvc.ErrMySQLDataPage
					}
					b, e := hex.DecodeString(value.Text[2:])
					if e != nil {
						return 0, nil, e
					}
					value = mysqlsvc.MySQLDataValue{Binary: true, Bytes: b}
				} else if !value.Null && (cell.Type == "n" || cell.Type == "") && mysqlDataXLSXDateFormat(id, code) {
					value.Text, err = mysqlDataXLSXDate(cell.Value, r.date1904)
					if err != nil {
						return 0, nil, err
					}
				}
			} else if cell.Style != 0 {
				return 0, nil, mysqlsvc.ErrMySQLDataPage
			}
			if !utf8.ValidString(value.Text) || len(utf16.Encode([]rune(value.Text))) > 32767 {
				return 0, nil, mysqlsvc.ErrMySQLDataEditLimit
			}
			values[column] = value
		}
		return row.Index, values[:maxColumn], nil
	}
}
func (r *mysqlDataXLSXRows) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.closed {
			return nil, io.EOF
		}
		index, row := r.pendingIndex, r.pending
		r.pending = nil
		if row == nil {
			var err error
			index, row, err = r.raw(ctx)
			if err != nil {
				return nil, err
			}
		}
		if int64(index) < max(int64(1), r.options.FirstRow) {
			continue
		}
		if r.options.LastRow > 0 && int64(index) > r.options.LastRow {
			return nil, io.EOF
		}
		if len(row) > len(r.fields) {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		for len(row) < len(r.fields) {
			row = append(row, mysqlsvc.MySQLDataValue{Null: true})
		}
		if err := mysqlDataRecordValuesValid(r.fields, row); err != nil {
			return nil, err
		}
		return row, nil
	}
}
