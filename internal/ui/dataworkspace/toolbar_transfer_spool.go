package dataworkspace

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// Spools are private immutable files, never configuration or product assets.
// Every opened reader verifies its own file descriptor before exposing rows.
type mysqlDataImportSpool struct {
	mu     sync.Mutex
	path   string
	digest [32]byte
	fields []string
	rows   int64
	closed bool
}

type mysqlDataSpoolOwner struct {
	mu      sync.Mutex
	closed  bool
	spools  []*mysqlDataImportSpool
	cleanup []func() error
}

func (o *mysqlDataSpoolOwner) add(s *mysqlDataImportSpool) error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		s.Close()
		return context.Canceled
	}
	o.spools = append(o.spools, s)
	return nil
}
func (o *mysqlDataSpoolOwner) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	for _, cleanup := range o.cleanup {
		cleanup()
	}
	o.cleanup = nil
	for _, s := range o.spools {
		s.Close()
	}
	o.spools = nil
}

func mysqlDataReadSpool(ctx context.Context, f *os.File, format string, options mysqlDataSourceOptions, owner *mysqlDataSpoolOwner) (*mysqlDataImportSpool, *mysqlDataImportInput, []mysqlsvc.MySQLDataImportColumn, error) {
	reader, err := mysqlDataOpenImportRows(ctx, f, format, options)
	if err != nil {
		return nil, nil, nil, err
	}
	spool, input, columns, err := mysqlDataSpoolRows(ctx, reader)
	if err != nil {
		return nil, nil, nil, err
	}
	if err = owner.add(spool); err != nil {
		return nil, nil, nil, err
	}
	return spool, input, columns, nil
}

func (s *mysqlDataImportSpool) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return os.Remove(s.path)
}

func (s *mysqlDataImportSpool) open(ctx context.Context) (*mysqlDataSpoolReader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("source was closed")
	}
	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	buffer := make([]byte, 64<<10)
	for {
		if err = ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		n, e := f.Read(buffer)
		h.Write(buffer[:n])
		if e == io.EOF {
			break
		}
		if e != nil {
			f.Close()
			return nil, e
		}
	}
	if !slices.Equal(h.Sum(nil), s.digest[:]) {
		f.Close()
		return nil, errors.New("reviewed source changed; prepare it again")
	}
	if _, err = f.Seek(0, 0); err != nil {
		f.Close()
		return nil, err
	}
	reader := &mysqlDataSpoolReader{file: f, fields: append([]string(nil), s.fields...), expected: s.rows, digest: s.digest, hash: sha256.New()}
	reader.reader = bufio.NewReaderSize(io.TeeReader(f, reader.hash), 64<<10)
	return reader, nil
}

type mysqlDataSpoolReader struct {
	digest          [32]byte
	hash            hash.Hash
	file            *os.File
	reader          *bufio.Reader
	fields          []string
	count, expected int64
}

func (r *mysqlDataSpoolReader) Fields() []string { return append([]string(nil), r.fields...) }
func (r *mysqlDataSpoolReader) Close() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
func (r *mysqlDataSpoolReader) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.file == nil {
		return nil, errors.New("source was closed")
	}
	var data []byte
	for {
		part, e := r.reader.ReadSlice('\n')
		data = append(data, part...)
		if len(data) > 100<<20 {
			return nil, mysqlsvc.ErrMySQLDataEditLimit
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		if e == io.EOF {
			if len(data) != 0 || r.count != r.expected || !slices.Equal(r.hash.Sum(nil), r.digest[:]) {
				return nil, errors.New("reviewed source is incomplete")
			}
			return nil, io.EOF
		}
		if e != nil {
			return nil, e
		}
		break
	}
	var row []mysqlsvc.MySQLDataValue
	if err := json.Unmarshal(data, &row); err != nil {
		return nil, errors.New("invalid reviewed source")
	}
	r.count++
	if len(row) != len(r.fields) || r.count > r.expected {
		return nil, errors.New("invalid reviewed source")
	}
	return row, nil
}

func mysqlDataSpoolRows(ctx context.Context, reader mysqlDataImportRowReader) (*mysqlDataImportSpool, *mysqlDataImportInput, []mysqlsvc.MySQLDataImportColumn, error) {
	defer reader.Close()
	fields := reader.Fields()
	seen := map[string]bool{}
	if len(fields) == 0 || len(fields) > 128 {
		return nil, nil, nil, mysqlsvc.ErrMySQLDataPage
	}
	for _, field := range fields {
		if field == "" || seen[field] {
			return nil, nil, nil, mysqlsvc.ErrMySQLDataPage
		}
		seen[field] = true
	}
	f, err := os.CreateTemp("", "nyadb-import-*.jsonl")
	if err != nil {
		return nil, nil, nil, err
	}
	keep := false
	defer func() {
		f.Close()
		if !keep {
			os.Remove(f.Name())
		}
	}()
	h := sha256.New()
	writer := bufio.NewWriterSize(io.MultiWriter(f, h), 64<<10)
	encoder := json.NewEncoder(writer)
	spool := &mysqlDataImportSpool{path: f.Name(), fields: fields}
	preview := &mysqlDataImportInput{Fields: append([]string(nil), fields...)}
	var inferred []mysqlsvc.MySQLDataImportColumn
	for {
		row, e := reader.Next(ctx)
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, nil, nil, e
		}
		if len(row) != len(fields) {
			return nil, nil, nil, mysqlsvc.ErrMySQLDataPage
		}
		total := 0
		for _, value := range row {
			total += len(value.Text) + len(value.Bytes)
			if len(value.Text)+len(value.Bytes) > mysqlsvc.MySQLDataEditMaxBytes || total > mysqlsvc.MySQLDataExportRowMaxBytes {
				return nil, nil, nil, mysqlsvc.ErrMySQLDataEditLimit
			}
		}
		if err = encoder.Encode(row); err != nil {
			return nil, nil, nil, err
		}
		spool.rows++
		one := mysqlDataInferImportColumns(&mysqlDataImportInput{Fields: fields, Rows: [][]mysqlsvc.MySQLDataValue{row}})
		if inferred == nil {
			inferred = one
		} else {
			for i := range inferred {
				if !row[i].Null && inferred[i].Type != one[i].Type {
					if row[i].Binary {
						inferred[i].Type = "LONGBLOB"
					} else {
						inferred[i].Type = "LONGTEXT"
					}
				}
			}
		}
		if len(preview.Rows) < 100 {
			owned := make([]mysqlsvc.MySQLDataValue, len(row))
			for i, v := range row {
				v.Bytes = slices.Clone(v.Bytes)
				owned[i] = v
			}
			preview.Rows = append(preview.Rows, owned)
		}
	}
	if spool.rows == 0 {
		return nil, nil, nil, mysqlsvc.ErrMySQLDataPage
	}
	if err = writer.Flush(); err != nil {
		return nil, nil, nil, err
	}
	if err = f.Sync(); err != nil {
		return nil, nil, nil, err
	}
	copy(spool.digest[:], h.Sum(nil))
	if err = f.Chmod(0400); err != nil {
		return nil, nil, nil, err
	}
	keep = true
	return spool, preview, inferred, nil
}

type mysqlDataDelimitedRows struct {
	reader  *csv.Reader
	fields  []string
	index   int64
	options mysqlDataSourceOptions
}

func (r *mysqlDataDelimitedRows) Fields() []string { return append([]string(nil), r.fields...) }
func (r *mysqlDataDelimitedRows) Close() error     { return nil }
func (r *mysqlDataDelimitedRows) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.options.LastRow > 0 && r.index >= r.options.LastRow {
			return nil, io.EOF
		}
		cells, err := r.reader.Read()
		if err != nil {
			return nil, err
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

func mysqlDataEncodedReader(r io.Reader, encoding string) (io.Reader, error) {
	return newMySQLDataStrictTextReader(r, encoding)
}

func mysqlDataOpenImportRows(ctx context.Context, f *os.File, format string, options mysqlDataSourceOptions) (mysqlDataImportRowReader, error) {
	if format == "CSV" || format == "TSV" || format == "TXT" {
		return mysqlDataOpenDelimitedRows(f, options)
	}
	if format == "JSON" {
		return mysqlDataOpenJSONRows(ctx, f, options)
	}
	if format == "XML" {
		return mysqlDataOpenXMLRows(ctx, f, options)
	}
	if format == "HTML" {
		return mysqlDataOpenHTMLRows(ctx, f, options)
	}
	if format == "XLSX" {
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		return mysqlDataOpenXLSXRows(ctx, f, info.Size(), options)
	}
	// The specialized binary readers below stream records from seekable files.
	if format == "Parquet" || format == "PARQUET" {
		info, e := f.Stat()
		if e != nil {
			return nil, e
		}
		reader, err := mysqlDataOpenParquetRows(f, info.Size())
		if err != nil {
			return nil, err
		}
		return &mysqlDataRangeRowReader{reader: reader, first: options.FirstRow, last: options.LastRow}, nil
	}
	if format == "DBF" {
		reader, err := mysqlDataOpenDBFRowsWithEncoding(f, options.Encoding)
		if err != nil {
			return nil, err
		}
		return &mysqlDataRangeRowReader{reader: reader, first: options.FirstRow, last: options.LastRow}, nil
	}
	if format == "XLS" {
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		return mysqlDataOpenXLSRowsOptions(ctx, f, info.Size(), options)
	}
	input, err := mysqlDataParseImport(f, format)
	if err != nil {
		return nil, err
	}
	return &mysqlDataSliceRowReader{input: input}, nil
}

func mysqlDataPreparationKey(t *Document) string {
	tr := &t.transfer
	key, _ := json.Marshal(struct {
		Options                                               mysqlDataSourceOptions
		Target, Name, Mode, Format, Destination, ParsedSource string
		Mapping, Match                                        []string
		Columns                                               []mysqlsvc.MySQLDataImportColumn
		Empty, Continue, Extended, Ignore                     bool
		StatementKB                                           int
		Metadata                                              []mysqlsvc.MySQLStructureColumn
	}{tr.Options, tr.Target, tr.NewName, tr.Mode, tr.Format, mysqlDataImportDestination(t), tr.ParsedSourceKey + ":" + mysqlDataCurrentSourceKey(tr), tr.Mapping, tr.MatchColumns, tr.NewColumns, tr.EmptyAsNull, tr.ContinueOnError, tr.ExtendedInsert, tr.IgnoreForeignKeys, tr.StatementKB, mysqlDataImportTargetMetadata(t)})
	return string(key)
}

func mysqlDataConvertImportValue(value mysqlsvc.MySQLDataValue, col mysqlsvc.MySQLStructureColumn, options mysqlDataSourceOptions, emptyAsNull bool) (mysqlsvc.MySQLDataValue, error) {
	if options.Compatibility == "navicat_xml" && !value.Null && !value.Binary && value.Text == "" {
		kind := strings.ToLower(col.DataType)
		if slices.Contains([]string{"tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "double", "float"}, kind) {
			value.Text = "0"
			return value, nil
		}
	}
	value = mysqlDataImportValue(value, emptyAsNull)
	if value.Null || value.Binary {
		return value, nil
	}
	if mysqlDataBinaryField(col) {
		text := value.Text
		if options.BinaryEncoding == "base64" {
			return mysqlDataDecodeBase64Value(text)
		}
		if !strings.HasPrefix(text, "0x") {
			return value, errors.New("binary input must have an explicit hex prefix")
		}
		b, err := hex.DecodeString(text[2:])
		if err != nil {
			return value, err
		}
		return mysqlsvc.MySQLDataValue{Binary: true, Bytes: b}, nil
	}
	kind := strings.ToLower(col.DataType)
	if options.DecimalSeparator != "" && options.DecimalSeparator != "." && slices.Contains([]string{"decimal", "numeric", "float", "double"}, kind) {
		value.Text = strings.ReplaceAll(value.Text, options.DecimalSeparator, ".")
	}
	if slices.Contains([]string{"date", "datetime", "timestamp", "time"}, kind) {
		var err error
		value.Text, err = mysqlDataConvertDate(value.Text, kind, options)
		if err != nil {
			return value, err
		}
	}
	return value, nil
}

func mysqlDataMappedRow(row []mysqlsvc.MySQLDataValue, mapping []string, metadata []mysqlsvc.MySQLStructureColumn, options mysqlDataSourceOptions, empty bool) ([]mysqlsvc.MySQLDataValue, int64, error) {
	columns := map[string]mysqlsvc.MySQLStructureColumn{}
	for _, col := range metadata {
		columns[col.Name] = col
	}
	values := make([]mysqlsvc.MySQLDataValue, 0, len(mapping))
	changed := int64(0)
	for i, name := range mapping {
		if name == "" {
			continue
		}
		if i >= len(row) {
			return nil, 0, mysqlsvc.ErrMySQLDataPage
		}
		value, err := mysqlDataConvertImportValue(row[i], columns[name], options, empty)
		if err != nil {
			return nil, 0, fmt.Errorf("field %s: invalid source conversion", name)
		}
		if !reflect.DeepEqual(value, row[i]) {
			changed++
		}
		values = append(values, value)
	}
	return values, changed, nil
}
