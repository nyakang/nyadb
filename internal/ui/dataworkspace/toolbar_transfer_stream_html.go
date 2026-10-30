package dataworkspace

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	xhtml "golang.org/x/net/html"
)

// Only inert cells of the first table are consumed. The tokenizer never builds
// a complete DOM, follows URLs, or evaluates scripts/styles/nested tables.
type mysqlDataHTMLRows struct {
	tokens                               *xhtml.Tokenizer
	window                               *mysqlDataRecordWindow
	fields                               []string
	options                              mysqlDataSourceOptions
	index                                int64
	tableDepth                           int
	opened, ended, closed, inRow, inCell bool
	skip                                 string
	kind                                 string
	text                                 strings.Builder
	row                                  []mysqlsvc.MySQLDataValue
	pending                              []mysqlsvc.MySQLDataValue
}

func mysqlDataOpenHTMLRows(ctx context.Context, input io.Reader, options mysqlDataSourceOptions) (*mysqlDataHTMLRows, error) {
	encoded, err := mysqlDataEncodedReader(input, options.Encoding)
	if err != nil {
		return nil, err
	}
	window := &mysqlDataRecordWindow{reader: encoded, ctx: ctx, left: 40 << 20}
	tokens := xhtml.NewTokenizer(window)
	tokens.SetMaxBuf(8 << 20)
	r := &mysqlDataHTMLRows{tokens: tokens, window: window, options: options}
	header := max(int64(1), options.HeaderRow)
	for at := int64(1); at <= header; at++ {
		row, e := r.raw(ctx)
		if e != nil {
			return nil, e
		}
		if at == header {
			for i, v := range row {
				if options.HeaderRow == 0 {
					r.fields = append(r.fields, fmt.Sprintf("Column%d", i+1))
					continue
				}
				if v.Null || v.Binary || v.Text == "" {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				r.fields = append(r.fields, v.Text)
			}
			if options.HeaderRow == 0 {
				r.pending = row
			}
		}
	}
	r.index = header
	if err := mysqlDataRecordValuesValid(r.fields, make([]mysqlsvc.MySQLDataValue, len(r.fields))); err != nil {
		return nil, err
	}
	return r, nil
}
func (r *mysqlDataHTMLRows) Fields() []string { return slices.Clone(r.fields) }
func (r *mysqlDataHTMLRows) Close() error {
	r.closed = true
	r.tokens = nil
	r.window = nil
	r.text.Reset()
	r.row = nil
	r.pending = nil
	return nil
}
func (r *mysqlDataHTMLRows) cell() error {
	if !r.inCell {
		return nil
	}
	r.inCell = false
	v := mysqlsvc.MySQLDataValue{Text: r.text.String()}
	switch r.kind {
	case "text":
	case "null":
		v = mysqlsvc.MySQLDataValue{Null: true}
	case "binary":
		if !strings.HasPrefix(v.Text, "0x") {
			return mysqlsvc.ErrMySQLDataPage
		}
		b, err := hex.DecodeString(v.Text[2:])
		if err != nil {
			return err
		}
		v = mysqlsvc.MySQLDataValue{Binary: true, Bytes: b}
	default:
		return mysqlsvc.ErrMySQLDataPage
	}
	r.row = append(r.row, v)
	r.text.Reset()
	if len(r.row) > 128 {
		return mysqlsvc.ErrMySQLDataEditLimit
	}
	return nil
}
func (r *mysqlDataHTMLRows) raw(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	r.window.ctx = ctx
	r.window.left = 40 << 20
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.ended {
			return nil, io.EOF
		}
		typ := r.tokens.Next()
		if typ == xhtml.ErrorToken {
			err := r.tokens.Err()
			if err == io.EOF && r.inRow {
				return nil, errors.New("HTML row is incomplete")
			}
			return nil, err
		}
		if !utf8.Valid(r.tokens.Raw()) {
			return nil, errors.New("HTML source encoding does not match the file")
		}
		t := r.tokens.Token()
		if typ == xhtml.StartTagToken || typ == xhtml.SelfClosingTagToken {
			if t.Data == "table" {
				r.tableDepth++
				r.opened = true
				continue
			}
			if !r.opened || r.tableDepth != 1 {
				continue
			}
			if r.skip != "" {
				continue
			}
			if t.Data == "script" || t.Data == "style" {
				r.skip = t.Data
				continue
			}
			switch t.Data {
			case "tr":
				if r.inRow {
					return nil, errors.New("HTML rows require explicit closing tags")
				}
				r.inRow = true
				r.row = nil
			case "td", "th":
				if !r.inRow {
					continue
				}
				if err := r.cell(); err != nil {
					return nil, err
				}
				r.inCell = true
				r.kind = "text"
				r.text.Reset()
				for _, a := range t.Attr {
					if (a.Key == "colspan" || a.Key == "rowspan") && a.Val != "1" {
						return nil, errors.New("HTML merged cells require an explicit flat mapping")
					}
					if a.Key == "data-nyadb-kind" {
						r.kind = a.Val
					}
				}
			case "br":
				if r.inCell {
					r.text.WriteByte('\n')
				}
			}
		} else if typ == xhtml.EndTagToken {
			if t.Data == "table" {
				if r.tableDepth > 0 {
					r.tableDepth--
				}
				if r.opened && r.tableDepth == 0 {
					r.ended = true
					if r.inRow {
						return nil, errors.New("HTML rows require explicit closing tags")
					}
				}
				continue
			}
			if r.tableDepth != 1 {
				continue
			}
			if r.skip != "" {
				if t.Data == r.skip {
					r.skip = ""
				}
				continue
			}
			switch t.Data {
			case "td", "th":
				if err := r.cell(); err != nil {
					return nil, err
				}
			case "tr":
				if r.inRow {
					if err := r.cell(); err != nil {
						return nil, err
					}
					r.inRow = false
					row := r.row
					r.row = nil
					if len(row) > 0 {
						return row, nil
					}
				}
			}
		} else if typ == xhtml.TextToken && r.tableDepth == 1 && r.inCell && r.skip == "" {
			if r.text.Len()+len(t.Data) > 2*mysqlsvc.MySQLDataExportFieldMaxBytes+2 {
				return nil, mysqlsvc.ErrMySQLDataEditLimit
			}
			r.text.WriteString(t.Data)
		}
	}
}
func (r *mysqlDataHTMLRows) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.closed || r.options.LastRow > 0 && r.index >= r.options.LastRow && r.pending == nil {
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
			r.index++
		}
		if err := mysqlDataRecordValuesValid(r.fields, row); err != nil {
			return nil, err
		}
		if r.index >= max(int64(1), r.options.FirstRow) {
			return row, nil
		}
	}
}
