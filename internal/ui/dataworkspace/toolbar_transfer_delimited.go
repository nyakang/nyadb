package dataworkspace

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// A record is bounded independently of the file. Qualifiers are doubled inside
// quoted values; quoted newlines and exact leading/trailing spaces are retained.
type mysqlDataDelimitedReader struct {
	input            *bufio.Reader
	delimiter, quote rune
}

func mysqlDataTextCharacters(o mysqlDataSourceOptions) (rune, rune, error) {
	delimiter := o.Delimiter
	if delimiter == `\t` {
		delimiter = "\t"
	}
	d := []rune(delimiter)
	q := []rune(o.Quote)
	if len(d) != 1 || len(q) > 1 || d[0] == '\n' || d[0] == '\r' || len(q) == 1 && (q[0] == d[0] || q[0] == '\n' || q[0] == '\r') {
		return 0, 0, errors.New("delimiter and text qualifier must be distinct single characters")
	}
	qualifier := rune(0)
	if len(q) == 1 {
		qualifier = q[0]
	}
	return d[0], qualifier, nil
}

func (r *mysqlDataDelimitedReader) Read() ([]string, error) {
	fields := []string{}
	var value strings.Builder
	quoted, ended, started := false, false, false
	total := 0
	appendRune := func(ch rune) error {
		value.WriteRune(ch)
		total += utf8.RuneLen(ch)
		if value.Len() > mysqlsvc.MySQLDataExportFieldMaxBytes || total > mysqlsvc.MySQLDataExportRowMaxBytes {
			return mysqlsvc.ErrMySQLDataEditLimit
		}
		return nil
	}
	for {
		ch, size, err := r.input.ReadRune()
		if err != nil {
			if err != io.EOF {
				return nil, err
			}
			if quoted {
				return nil, errors.New("unterminated quoted field")
			}
			if !started && len(fields) == 0 && value.Len() == 0 {
				return nil, io.EOF
			}
			return append(fields, value.String()), nil
		}
		if ch == utf8.RuneError && size == 1 {
			return nil, errors.New("invalid text encoding")
		}
		if quoted {
			if ch == r.quote {
				next, _, e := r.input.ReadRune()
				if e == nil && next == r.quote {
					if err := appendRune(ch); err != nil {
						return nil, err
					}
					continue
				}
				if e != nil && e != io.EOF {
					return nil, e
				}
				if e == nil {
					r.input.UnreadRune()
				}
				quoted, ended = false, true
			} else if err := appendRune(ch); err != nil {
				return nil, err
			}
			continue
		}
		if ch == r.delimiter {
			fields = append(fields, value.String())
			if len(fields) >= 128 {
				return nil, mysqlsvc.ErrMySQLDataEditLimit
			}
			value.Reset()
			ended = false
			started = true
			continue
		}
		if ch == '\n' || ch == '\r' {
			if ch == '\r' {
				next, _, e := r.input.ReadRune()
				if e == nil && next != '\n' {
					r.input.UnreadRune()
				} else if e != nil && e != io.EOF {
					return nil, e
				}
			}
			if !started && len(fields) == 0 && value.Len() == 0 {
				continue
			}
			return append(fields, value.String()), nil
		}
		if ended {
			return nil, errors.New("unexpected text after closing qualifier")
		}
		if r.quote != 0 && ch == r.quote {
			if value.Len() != 0 {
				return nil, errors.New("text qualifier inside an unquoted field")
			}
			quoted = true
			started = true
			continue
		}
		started = true
		if err := appendRune(ch); err != nil {
			return nil, err
		}
	}
}

type mysqlDataDelimitedStream struct {
	reader  *mysqlDataDelimitedReader
	fields  []string
	pending []string
	index   int64
	options mysqlDataSourceOptions
}

func (r *mysqlDataDelimitedStream) Fields() []string { return append([]string(nil), r.fields...) }
func (r *mysqlDataDelimitedStream) Close() error     { return nil }
func (r *mysqlDataDelimitedStream) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.options.LastRow > 0 && r.index >= r.options.LastRow {
			return nil, io.EOF
		}
		cells := r.pending
		r.pending = nil
		if cells == nil {
			var err error
			cells, err = r.reader.Read()
			if err != nil {
				return nil, err
			}
		}
		r.index++
		if r.index < r.options.FirstRow {
			continue
		}
		if len(cells) != len(r.fields) {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		row := make([]mysqlsvc.MySQLDataValue, len(cells))
		for i, text := range cells {
			row[i] = mysqlDataDecodeDelimited(text)
		}
		return row, nil
	}
}
func mysqlDataOpenDelimitedRows(input io.Reader, o mysqlDataSourceOptions) (mysqlDataImportRowReader, error) {
	if o.HeaderRow < 0 || o.FirstRow < 1 || o.HeaderRow > 0 && o.FirstRow <= o.HeaderRow || o.LastRow > 0 && o.LastRow < o.FirstRow {
		return nil, errors.New("invalid header or data row range")
	}
	d, q, err := mysqlDataTextCharacters(o)
	if err != nil {
		return nil, err
	}
	decoded, err := mysqlDataEncodedReader(input, o.Encoding)
	if err != nil {
		return nil, err
	}
	reader := &mysqlDataDelimitedReader{input: bufio.NewReader(decoded), delimiter: d, quote: q}
	rows := &mysqlDataDelimitedStream{reader: reader, options: o}
	if o.HeaderRow == 0 {
		rows.pending, err = reader.Read()
		if err != nil {
			return nil, err
		}
		for i := range rows.pending {
			rows.fields = append(rows.fields, fmt.Sprintf("field_%d", i+1))
		}
	} else {
		for rows.index < o.HeaderRow {
			rows.fields, err = reader.Read()
			if err != nil {
				return nil, err
			}
			rows.index++
		}
		if len(rows.fields) > 0 {
			rows.fields[0] = strings.TrimPrefix(rows.fields[0], "\ufeff")
		}
	}
	return rows, nil
}

type mysqlDataDelimitedEncoder struct {
	w                io.Writer
	delimiter, quote rune
	newline          string
	fields           int
}

func newMySQLDataDelimitedEncoder(w io.Writer, fields []string, o mysqlDataSourceOptions) (*mysqlDataDelimitedEncoder, error) {
	d, q, err := mysqlDataTextCharacters(o)
	if err != nil {
		return nil, err
	}
	newline := "\n"
	if o.Newline == "CRLF" {
		newline = "\r\n"
	}
	e := &mysqlDataDelimitedEncoder{w: w, delimiter: d, quote: q, newline: newline, fields: len(fields)}
	if o.HeaderRow != 0 {
		if err := e.write(fields); err != nil {
			return nil, err
		}
	}
	return e, nil
}
func (e *mysqlDataDelimitedEncoder) write(fields []string) error {
	for i, text := range fields {
		if i > 0 {
			if _, err := io.WriteString(e.w, string(e.delimiter)); err != nil {
				return err
			}
		}
		needs := len(fields) == 1 && text == "" || strings.ContainsAny(text, string(e.delimiter)+"\r\n") || e.quote != 0 && strings.ContainsRune(text, e.quote)
		if needs && e.quote == 0 {
			return errors.New("value requires a text qualifier; choose one to preserve delimiters/newlines")
		}
		if needs {
			q := string(e.quote)
			text = q + strings.ReplaceAll(text, q, q+q) + q
		}
		if _, err := io.WriteString(e.w, text); err != nil {
			return err
		}
	}
	_, err := io.WriteString(e.w, e.newline)
	return err
}
func (e *mysqlDataDelimitedEncoder) Row(values []mysqlsvc.MySQLDataValue) error {
	if len(values) != e.fields {
		return mysqlsvc.ErrMySQLDataPage
	}
	fields := make([]string, len(values))
	for i, v := range values {
		fields[i] = v.Text
		if v.Binary {
			fields[i] = fmt.Sprintf("0x%x", v.Bytes)
		}
		if v.Null {
			fields[i] = `\N`
		} else if strings.HasPrefix(fields[i], `\`) {
			fields[i] = `\` + fields[i]
		}
	}
	return e.write(fields)
}
func (e *mysqlDataDelimitedEncoder) Close() error { return nil }
