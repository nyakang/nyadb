package dataworkspace

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// A decoder may read ahead by one small buffer, but it cannot allocate from an
// unbounded single record. The window resets per row, not per input file.
type mysqlDataRecordWindow struct {
	reader io.Reader
	ctx    context.Context
	left   int64
}

func (r *mysqlDataRecordWindow) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.left <= 0 {
		return 0, mysqlsvc.ErrMySQLDataEditLimit
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n, err := r.reader.Read(p)
	r.left -= int64(n)
	return n, err
}
func mysqlDataRecordValuesValid(fields []string, row []mysqlsvc.MySQLDataValue) error {
	if len(fields) == 0 || len(fields) > 128 || len(row) != len(fields) {
		return mysqlsvc.ErrMySQLDataPage
	}
	total := 0
	seen := map[string]bool{}
	for i, f := range fields {
		if f == "" || seen[f] {
			return mysqlsvc.ErrMySQLDataPage
		}
		seen[f] = true
		size := len(row[i].Text) + len(row[i].Bytes)
		total += size
		if size > mysqlsvc.MySQLDataExportFieldMaxBytes || total > mysqlsvc.MySQLDataExportRowMaxBytes {
			return mysqlsvc.ErrMySQLDataEditLimit
		}
	}
	return nil
}

type mysqlDataJSONRows struct {
	decoder       *json.Decoder
	window        *mysqlDataRecordWindow
	fields        []string
	pending       []mysqlsvc.MySQLDataValue
	options       mysqlDataSourceOptions
	index         int64
	ended, closed bool
}

func mysqlDataOpenJSONRows(ctx context.Context, input io.Reader, options mysqlDataSourceOptions) (*mysqlDataJSONRows, error) {
	encoded, err := mysqlDataEncodedReader(input, options.Encoding)
	if err != nil {
		return nil, err
	}
	window := &mysqlDataRecordWindow{reader: encoded, ctx: ctx, left: 40 << 20}
	dec := json.NewDecoder(window)
	dec.UseNumber()
	token, err := dec.Token()
	if err != nil || token != json.Delim('[') {
		return nil, errors.New("JSON import requires an array of flat objects")
	}
	r := &mysqlDataJSONRows{decoder: dec, window: window, options: options}
	row, err := r.raw(ctx)
	if err != nil {
		return nil, err
	}
	r.pending = row
	return r, nil
}
func (r *mysqlDataJSONRows) Fields() []string { return slices.Clone(r.fields) }
func (r *mysqlDataJSONRows) Close() error {
	r.closed = true
	r.pending = nil
	r.decoder = nil
	r.window = nil
	return nil
}
func (r *mysqlDataJSONRows) raw(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.window.ctx = ctx
	r.window.left = 40 << 20
	if !r.decoder.More() {
		token, err := r.decoder.Token()
		if err != nil || token != json.Delim(']') {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		var extra any
		if err = r.decoder.Decode(&extra); err != io.EOF {
			return nil, errors.New("JSON contains data after its array")
		}
		r.ended = true
		return nil, io.EOF
	}
	var raw json.RawMessage
	if err := r.decoder.Decode(&raw); err != nil {
		return nil, err
	}
	if len(raw) > 40<<20 {
		return nil, mysqlsvc.ErrMySQLDataEditLimit
	}
	if !mysqlDataJSONUnicodeValid(raw) {
		return nil, errors.New("JSON contains invalid Unicode; select the correct encoding")
	}
	wrapped := make([]byte, 0, len(raw)+2)
	wrapped = append(wrapped, '[')
	wrapped = append(wrapped, raw...)
	wrapped = append(wrapped, ']')
	if err := mysqlDataValidateJSONImportKeys(wrapped); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var object map[string]any
	if err := dec.Decode(&object); err != nil || len(object) == 0 {
		return nil, errors.New("JSON rows must be nonempty flat objects")
	}
	if len(r.fields) == 0 {
		for name := range object {
			r.fields = append(r.fields, name)
		}
		slices.Sort(r.fields)
	}
	if len(object) != len(r.fields) {
		return nil, errors.New("JSON rows must contain identical fields")
	}
	row := make([]mysqlsvc.MySQLDataValue, len(r.fields))
	for i, f := range r.fields {
		v, ok := object[f]
		if !ok {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		switch value := v.(type) {
		case nil:
			row[i].Null = true
		case string:
			row[i].Text = value
		case json.Number:
			row[i].Text = string(value)
		case bool:
			if value {
				row[i].Text = "1"
			} else {
				row[i].Text = "0"
			}
		default:
			return nil, errors.New("JSON nested values require an explicit text mapping")
		}
	}
	if err := mysqlDataRecordValuesValid(r.fields, row); err != nil {
		return nil, err
	}
	return row, nil
}

// encoding/json replaces malformed UTF-8 and unpaired UTF-16 escapes with
// U+FFFD. Reject both before decoding so import never silently changes text.
func mysqlDataJSONUnicodeValid(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	hex4 := func(p []byte) (uint16, bool) {
		if len(p) < 4 {
			return 0, false
		}
		var value uint16
		for _, c := range p[:4] {
			value <<= 4
			switch {
			case c >= '0' && c <= '9':
				value |= uint16(c - '0')
			case c >= 'a' && c <= 'f':
				value |= uint16(c - 'a' + 10)
			case c >= 'A' && c <= 'F':
				value |= uint16(c - 'A' + 10)
			default:
				return 0, false
			}
		}
		return value, true
	}
	inString := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		value, ok := hex4(raw[i+1:])
		if !ok {
			return false
		}
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value < 0xd800 || value > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		low, ok := hex4(raw[i+3:])
		if !ok || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}
func (r *mysqlDataJSONRows) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.closed || r.ended || r.options.LastRow > 0 && r.index >= r.options.LastRow {
			return nil, io.EOF
		}
		row := r.pending
		r.pending = nil
		if row == nil {
			var err error
			row, err = r.raw(ctx)
			if err != nil {
				return nil, err
			}
		}
		r.index++
		if r.index >= max(int64(1), r.options.FirstRow) {
			return row, nil
		}
	}
}

type mysqlDataXMLRows struct {
	decoder               *xml.Decoder
	window                *mysqlDataRecordWindow
	fields                []string
	pending               []mysqlsvc.MySQLDataValue
	options               mysqlDataSourceOptions
	index                 int64
	opened, ended, closed bool
}

func mysqlDataOpenXMLRows(ctx context.Context, input io.Reader, options mysqlDataSourceOptions) (*mysqlDataXMLRows, error) {
	if !mysqlDataXMLNameValid(options.XMLRoot) || !mysqlDataXMLNameValid(options.XMLRow) {
		return nil, mysqlDataXMLError()
	}
	encoded, err := mysqlDataEncodedReader(input, options.Encoding)
	if err != nil {
		return nil, err
	}
	window := &mysqlDataRecordWindow{reader: encoded, ctx: ctx, left: 40 << 20}
	dec := xml.NewDecoder(window)
	if options.Encoding != "" && options.Encoding != "UTF-8" {
		dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	}
	r := &mysqlDataXMLRows{decoder: dec, window: window, options: options}
	row, err := r.raw(ctx)
	if err != nil {
		return nil, err
	}
	r.pending = row
	return r, nil
}
func (r *mysqlDataXMLRows) Fields() []string { return slices.Clone(r.fields) }
func (r *mysqlDataXMLRows) Close() error {
	r.closed = true
	r.pending = nil
	r.decoder = nil
	r.window = nil
	return nil
}
func (r *mysqlDataXMLRows) raw(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	r.window.ctx = ctx
	r.window.left = 40 << 20
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := r.decoder.Token()
		if err == io.EOF {
			if !r.ended {
				return nil, mysqlDataXMLError()
			}
			return nil, io.EOF
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.ProcInst:
			if r.opened || t.Target != "xml" {
				return nil, mysqlDataXMLError()
			}
		case xml.Directive:
			return nil, mysqlDataXMLError()
		case xml.Comment:
		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				return nil, mysqlDataXMLError()
			}
		case xml.StartElement:
			if r.ended || t.Name.Space != "" {
				return nil, mysqlDataXMLError()
			}
			if !r.opened {
				if t.Name.Local != r.options.XMLRoot || !mysqlDataXMLNamespacesOnly(t.Attr) {
					return nil, mysqlDataXMLError()
				}
				r.opened = true
				continue
			}
			if t.Name.Local != r.options.XMLRow {
				return nil, mysqlDataXMLError()
			}
			attributes := !mysqlDataXMLNamespacesOnly(t.Attr)
			if attributes && !r.options.XMLAttributes {
				return nil, errors.New("XML record attributes require the attributes-as-fields option")
			}
			if !attributes && r.options.XMLAttributes {
				return nil, errors.New("XML record has no attribute fields")
			}
			fields, values, err := mysqlDataReadXMLRecord(r.decoder, t)
			if err != nil {
				return nil, err
			}
			if len(r.fields) == 0 {
				r.fields = slices.Clone(fields)
			}
			if len(fields) != len(r.fields) {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			byName := map[string]mysqlsvc.MySQLDataValue{}
			for i, f := range fields {
				byName[f] = values[i]
			}
			row := make([]mysqlsvc.MySQLDataValue, len(r.fields))
			for i, f := range r.fields {
				v, ok := byName[f]
				if !ok {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				row[i] = v
			}
			if err := mysqlDataRecordValuesValid(r.fields, row); err != nil {
				return nil, err
			}
			return row, nil
		case xml.EndElement:
			if !r.opened || r.ended || t.Name.Space != "" || t.Name.Local != r.options.XMLRoot {
				return nil, mysqlDataXMLError()
			}
			r.ended = true
		default:
			return nil, mysqlDataXMLError()
		}
	}
}
func (r *mysqlDataXMLRows) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.closed || r.options.LastRow > 0 && r.index >= r.options.LastRow {
			return nil, io.EOF
		}
		row := r.pending
		r.pending = nil
		if row == nil {
			var err error
			row, err = r.raw(ctx)
			if err != nil {
				return nil, err
			}
		}
		r.index++
		if r.index >= max(int64(1), r.options.FirstRow) {
			return row, nil
		}
	}
}
