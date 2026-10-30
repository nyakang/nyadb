package dataworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// Source settings are copied into an owned preparation request. Row numbers
// refer to the original source, before filtering and field mapping.
type mysqlDataSourceOptions struct {
	Encoding, Delimiter, Quote, Newline                                       string
	HeaderRow, FirstRow, LastRow                                              int64
	Sheet                                                                     int
	XMLRoot, XMLRow                                                           string
	XMLAttributes                                                             bool
	DateOrder, DateSeparator, TimeSeparator, DecimalSeparator, BinaryEncoding string
	Compatibility                                                             string
}

func defaultMySQLDataSourceOptions() mysqlDataSourceOptions {
	return mysqlDataSourceOptions{Encoding: "UTF-8", Delimiter: ",", Quote: "\"", Newline: "LF", HeaderRow: 1, FirstRow: 2, XMLRoot: "RECORDS", XMLRow: "RECORD", DateOrder: "YMD", DateSeparator: "-", TimeSeparator: ":", DecimalSeparator: ".", BinaryEncoding: "hex", Compatibility: "preserve"}
}

func mysqlDataSourceDefaults(format string) mysqlDataSourceOptions {
	o := defaultMySQLDataSourceOptions()
	if format == "TXT" || format == "TSV" {
		o.Delimiter = "\\t"
	}
	if format == "DBF" {
		o.Encoding = ""
	}
	if format != "CSV" && format != "TXT" && format != "TSV" && format != "XLS" && format != "XLSX" && format != "HTML" {
		o.HeaderRow, o.FirstRow = 0, 1
	}
	return o
}

// Conversion settings do not change the parsed source. All settings which do
// affect parsing must match the immutable spool before preparation is allowed.
func mysqlDataSourceKey(format string, o mysqlDataSourceOptions) string {
	o.DateOrder, o.DateSeparator, o.TimeSeparator, o.DecimalSeparator, o.BinaryEncoding, o.Compatibility = "", "", "", "", "", ""
	key, _ := json.Marshal(struct {
		Format  string
		Options mysqlDataSourceOptions
	}{format, o})
	return string(key)
}

func mysqlDataCurrentSourceKey(tr *mysqlDataTransferState) string {
	key := mysqlDataSourceKey(tr.Format, tr.Options)
	if tr.Format != "ODBC" && tr.Format != "Access" && tr.Format != "Paradox" {
		return key
	}
	var selected any
	if tr.SourceTable >= 0 && tr.SourceTable < len(tr.SourceTables) {
		selected = tr.SourceTables[tr.SourceTable]
	}
	encoded, _ := json.Marshal(struct {
		Config any
		Table  any
	}{tr.SourceConfig, selected})
	return fmt.Sprintf("%s:%x", key, sha256.Sum256(encoded))
}

type mysqlDataImportRowReader interface {
	Fields() []string
	Next(context.Context) ([]mysqlsvc.MySQLDataValue, error)
	Close() error
}

type mysqlDataSliceRowReader struct {
	input *mysqlDataImportInput
	index int
}

type mysqlDataRangeRowReader struct {
	reader             mysqlDataImportRowReader
	index, first, last int64
}

func (r *mysqlDataRangeRowReader) Fields() []string { return r.reader.Fields() }
func (r *mysqlDataRangeRowReader) Close() error     { return r.reader.Close() }
func (r *mysqlDataRangeRowReader) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.last > 0 && r.index >= r.last {
			return nil, io.EOF
		}
		row, err := r.reader.Next(ctx)
		if err != nil {
			return nil, err
		}
		r.index++
		if r.index >= r.first {
			return row, nil
		}
	}
}

func (r *mysqlDataSliceRowReader) Fields() []string { return append([]string(nil), r.input.Fields...) }
func (r *mysqlDataSliceRowReader) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.index >= len(r.input.Rows) {
		return nil, io.EOF
	}
	row := r.input.Rows[r.index]
	r.index++
	return row, nil
}
func (r *mysqlDataSliceRowReader) Close() error { r.input = nil; return nil }
