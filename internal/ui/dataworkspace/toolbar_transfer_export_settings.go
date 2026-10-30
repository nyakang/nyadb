package dataworkspace

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"slices"
	"strings"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

type mysqlDataContextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w mysqlDataContextWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(data)
}

type mysqlDataConfiguredEncoder struct {
	base     *mysqlDataTransferEncoder
	text     *mysqlDataDelimitedEncoder
	encoded  *transform.Writer
	options  mysqlDataSourceOptions
	metadata []mysqlsvc.MySQLStructureColumn
	fields   []string
	format   string
}

func mysqlDataTextEncoding(name string) (encoding.Encoding, error) {
	switch name {
	case "", "UTF-8":
		return nil, nil
	case "UTF-16LE":
		return unicode.UTF16(unicode.LittleEndian, unicode.UseBOM), nil
	case "UTF-16BE":
		return unicode.UTF16(unicode.BigEndian, unicode.UseBOM), nil
	case "GB18030":
		return simplifiedchinese.GB18030, nil
	case "Windows-1252":
		return charmap.Windows1252, nil
	case "Shift_JIS":
		return japanese.ShiftJIS, nil
	default:
		return nil, errors.New("unsupported output encoding")
	}
}
func mysqlDataTextFormat(format string) bool {
	return slices.Contains([]string{"CSV", "TXT", "TSV", "JSON", "XML", "HTML", "SQL"}, format)
}

func newMySQLDataConfiguredEncoder(ctx context.Context, w io.Writer, format, db, table string, fields []string, metadata []mysqlsvc.MySQLStructureColumn, o mysqlDataSourceOptions) (*mysqlDataConfiguredEncoder, error) {
	e := &mysqlDataConfiguredEncoder{options: o, metadata: metadata, fields: fields, format: format}
	w = mysqlDataContextWriter{ctx: ctx, writer: w}
	if mysqlDataTextFormat(format) {
		enc, err := mysqlDataTextEncoding(o.Encoding)
		if err != nil {
			return nil, err
		}
		if enc != nil {
			e.encoded = transform.NewWriter(w, enc.NewEncoder())
			w = e.encoded
		}
	}
	var err error
	if format == "CSV" || format == "TXT" || format == "TSV" {
		e.text, err = newMySQLDataDelimitedEncoder(w, fields, o)
	} else if format == "XML" {
		e.base = &mysqlDataTransferEncoder{w: w, format: format, db: db, table: table, fields: fields}
		e.base.xml, err = newMySQLDataXMLEncoderOptions(w, fields, o)
	} else if format == "HTML" {
		e.base = &mysqlDataTransferEncoder{w: w, format: format, db: db, table: table, fields: fields}
		e.base.additional, err = newMySQLDataHTMLEncoderEncoding(w, fields, o.Encoding)
	} else {
		e.base, err = newMySQLDataTransferEncoder(w, format, db, table, fields)
	}
	if err != nil {
		e.Abort()
		return nil, err
	}
	return e, nil
}
func (e *mysqlDataConfiguredEncoder) Row(values []mysqlsvc.MySQLDataValue) error {
	if len(values) != len(e.fields) {
		return mysqlsvc.ErrMySQLDataPage
	}
	converted := slices.Clone(values)
	for i, field := range e.fields {
		value := &converted[i]
		if e.format == "SQL" {
			continue
		}
		if value.Null {
			continue
		}
		if value.Binary {
			if mysqlDataTextFormat(e.format) && e.format != "XML" && e.format != "SQL" && e.options.BinaryEncoding == "base64" {
				value.Text = base64.StdEncoding.EncodeToString(value.Bytes)
				value.Bytes = nil
				value.Binary = false
			}
			continue
		}
		for _, col := range e.metadata {
			if col.Name != field {
				continue
			}
			kind := strings.ToLower(col.DataType)
			if slices.Contains([]string{"decimal", "numeric", "double", "float"}, kind) && e.options.DecimalSeparator != "" && e.options.DecimalSeparator != "." {
				value.Text = strings.Replace(value.Text, ".", e.options.DecimalSeparator, 1)
			}
			if kind == "date" || kind == "datetime" || kind == "timestamp" {
				date, clock, hasClock := strings.Cut(value.Text, " ")
				parts := strings.Split(date, "-")
				if len(parts) == 3 {
					if e.options.DateOrder == "DMY" {
						parts = []string{parts[2], parts[1], parts[0]}
					} else if e.options.DateOrder == "MDY" {
						parts = []string{parts[1], parts[2], parts[0]}
					}
					date = strings.Join(parts, e.options.DateSeparator)
				}
				if e.options.TimeSeparator != "" && e.options.TimeSeparator != ":" {
					clock = strings.ReplaceAll(clock, ":", e.options.TimeSeparator)
				}
				value.Text = date
				if hasClock {
					value.Text += " " + clock
				}
			} else if kind == "time" && e.options.TimeSeparator != "" && e.options.TimeSeparator != ":" {
				value.Text = strings.ReplaceAll(value.Text, ":", e.options.TimeSeparator)
			}
			break
		}
	}
	if e.text != nil {
		return e.text.Row(converted)
	}
	return e.base.Row(converted)
}
func (e *mysqlDataConfiguredEncoder) Close() error {
	var err error
	if e.text != nil {
		err = e.text.Close()
	} else {
		err = e.base.Close()
	}
	if err != nil {
		return err
	}
	if e.encoded != nil {
		return e.encoded.Close()
	}
	return nil
}

// Aborting releases format staging files without publishing an incomplete
// output or running a second encoding pass after cancellation.
func (e *mysqlDataConfiguredEncoder) Abort() error {
	if e.base == nil {
		return nil
	}
	switch codec := e.base.additional.(type) {
	case interface{ Abort() error }:
		return codec.Abort()
	case *mysqlDataXLSEncoder:
		return codec.spool.close()
	case *mysqlDataParquetEncoder:
		return codec.spool.close()
	}
	return nil
}
