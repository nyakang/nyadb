package dataworkspace

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// TXT defaults to a tab-delimited text table. The wizard can select other
// delimiters using the same streaming reader; fields are never inferred from
// whitespace and values are never silently trimmed.
func mysqlDataParseTXTImport(data []byte) (*mysqlDataImportInput, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = '\t'
	fields, err := reader.Read()
	if err != nil {
		return nil, err
	}
	input := &mysqlDataImportInput{Fields: fields}
	for {
		row, err := reader.Read()
		if err == io.EOF {
			return input, nil
		}
		if err != nil {
			return nil, err
		}
		values := make([]contract.DataValue, len(row))
		for i, value := range row {
			values[i] = mysqlDataDecodeDelimited(value)
		}
		input.Rows = append(input.Rows, values)
	}
}

type mysqlDataTXTEncoder struct {
	csv    *csv.Writer
	fields int
	closed bool
}

func newMySQLDataTXTEncoder(w io.Writer, fields []string) (*mysqlDataTXTEncoder, error) {
	e := &mysqlDataTXTEncoder{csv: csv.NewWriter(w), fields: len(fields)}
	e.csv.Comma = '\t'
	if err := e.csv.Write(fields); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *mysqlDataTXTEncoder) Row(values []contract.DataValue) error {
	if e.closed || len(values) != e.fields {
		return contract.ErrDataPage
	}
	row := make([]string, len(values))
	for i, v := range values {
		row[i] = v.Text
		if v.Binary {
			row[i] = "0x" + hex.EncodeToString(v.Bytes)
		}
		if v.Null {
			row[i] = `\N`
		} else if strings.HasPrefix(row[i], `\`) {
			row[i] = `\` + row[i]
		}
	}
	return e.csv.Write(row)
}

func (e *mysqlDataTXTEncoder) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	e.csv.Flush()
	return e.csv.Error()
}

// Schema-dependent formats use a private row spool. Only one row is decoded at
// a time; a small first row does not force later NULL/binary values into an
// incorrectly inferred schema. Close always deletes the temporary spool.
type mysqlDataCodecSpool struct {
	file    *os.File
	encoder *json.Encoder
	fields  []string
	rows    int64
	kinds   []string
	widths  []int
	closed  bool
}

func mysqlDataCodecContext(w io.Writer) context.Context {
	if writer, ok := w.(mysqlDataContextWriter); ok {
		return writer.ctx
	}
	return context.Background()
}

func newMySQLDataCodecSpool(fields []string) (*mysqlDataCodecSpool, error) {
	if len(fields) == 0 || len(fields) > 128 {
		return nil, contract.ErrDataPage
	}
	seen := map[string]bool{}
	for _, field := range fields {
		if field == "" || !utf8.ValidString(field) || seen[field] {
			return nil, contract.ErrDataPage
		}
		seen[field] = true
	}
	f, err := os.CreateTemp("", "nyadb-transfer-codec-*")
	if err != nil {
		return nil, err
	}
	return &mysqlDataCodecSpool{file: f, encoder: json.NewEncoder(f), fields: append([]string(nil), fields...), kinds: make([]string, len(fields)), widths: make([]int, len(fields))}, nil
}

func (s *mysqlDataCodecSpool) Row(values []contract.DataValue) error {
	if s.closed || len(values) != len(s.fields) {
		return contract.ErrDataPage
	}
	total := 0
	for i, v := range values {
		if v.Default || v.Null && v.Binary || !v.Binary && !utf8.ValidString(v.Text) {
			return contract.ErrDataPage
		}
		size := len(v.Text) + len(v.Bytes)
		total += size
		if size > contract.DataExportFieldMaxBytes || total > contract.DataExportRowMaxBytes {
			return contract.ErrDataEditLimit
		}
		if v.Null {
			continue
		}
		kind := "text"
		if v.Binary {
			kind = "binary"
		}
		if s.kinds[i] != "" && s.kinds[i] != kind {
			return errors.New("file column contains both text and binary values; select a compatible mapping")
		}
		s.kinds[i] = kind
		s.widths[i] = max(s.widths[i], size)
	}
	if err := s.encoder.Encode(values); err != nil {
		return err
	}
	s.rows++
	return nil
}

func (s *mysqlDataCodecSpool) each(ctx context.Context, visit func([]contract.DataValue) error) error {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	dec := json.NewDecoder(s.file)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var row []contract.DataValue
		if err := dec.Decode(&row); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := visit(row); err != nil {
			return err
		}
	}
}

func (s *mysqlDataCodecSpool) close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	name := s.file.Name()
	err := s.file.Close()
	remove := os.Remove(name)
	return errors.Join(err, remove)
}
