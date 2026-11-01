package dataworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/parquet-go/parquet-go"
	parquetformat "github.com/parquet-go/parquet-go/format"
	"io"
	"math"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The encoder uses standard Parquet UTF8 and BINARY annotations. Exact database
// numeric strings remain strings rather than first passing through float64.
// External DECIMAL/INTEGER/DATE/TIMESTAMP columns are decoded from their actual
// logical types by the reader below.
type mysqlDataParquetEncoder struct {
	spool *mysqlDataCodecSpool
	w     io.Writer
}

func newMySQLDataParquetEncoder(w io.Writer, fields []string) (*mysqlDataParquetEncoder, error) {
	s, err := newMySQLDataCodecSpool(fields)
	if err != nil {
		return nil, err
	}
	return &mysqlDataParquetEncoder{spool: s, w: w}, nil
}

func (e *mysqlDataParquetEncoder) Row(values []contract.DataValue) error {
	return e.spool.Row(values)
}
func (e *mysqlDataParquetEncoder) Abort() error { return e.spool.close() }
func (e *mysqlDataParquetEncoder) Close() error { return e.CloseContext(mysqlDataCodecContext(e.w)) }
func (e *mysqlDataParquetEncoder) CloseContext(ctx context.Context) (err error) {
	if e.spool.closed {
		return nil
	}
	defer func() { err = errors.Join(err, e.spool.close()) }()
	group := parquet.Group{}
	for i, field := range e.spool.fields {
		node := parquet.String()
		if e.spool.kinds[i] == "binary" {
			node = parquet.Leaf(parquet.ByteArrayType)
		}
		group[field] = parquet.Optional(node)
	}
	schema := parquet.NewSchema("NyaDB", group)
	order, _ := json.Marshal(e.spool.fields)
	pages, err := os.MkdirTemp("", "nyadb-transfer-parquet-pages-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(pages)) }()
	w := parquet.NewWriter(e.w, schema, parquet.MaxRowsPerRowGroup(1024), parquet.PageBufferSize(64<<10), parquet.WriteBufferSize(64<<10), parquet.ColumnPageBuffers(parquet.NewFileBufferPool(pages, "column-*")), parquet.CreatedBy("NyaDB", "0.1.0", ""), parquet.KeyValueMetadata("nyadb.column_order", string(order)))
	// Reset revokes buffered pages even when cancellation or a disk write fails.
	defer w.Reset(io.Discard)
	indices := make([]int, len(e.spool.fields))
	for i, field := range e.spool.fields {
		leaf, ok := schema.Lookup(field)
		if !ok {
			return contract.ErrDataPage
		}
		indices[i] = leaf.ColumnIndex
	}
	groupBytes, groupRows := 0, 0
	err = e.spool.each(ctx, func(values []contract.DataValue) error {
		rowBytes := 0
		for _, v := range values {
			rowBytes += len(v.Text) + len(v.Bytes)
		}
		if groupRows > 0 && (groupRows >= 1024 || groupBytes+rowBytes > 16<<20) {
			if err := w.Flush(); err != nil {
				return err
			}
			groupBytes, groupRows = 0, 0
		}
		row := make(parquet.Row, len(values))
		for i, value := range values {
			if value.Null {
				row[indices[i]] = parquet.NullValue().Level(0, 0, indices[i])
				continue
			}
			data := []byte(value.Text)
			if value.Binary {
				data = value.Bytes
			}
			row[indices[i]] = parquet.ByteArrayValue(data).Level(0, 1, indices[i])
		}
		_, err := w.WriteRows([]parquet.Row{row})
		groupBytes += rowBytes
		groupRows++
		return err
	})
	if err != nil {
		return err
	}
	return w.Close()
}

type mysqlDataParquetRows struct {
	reader  *parquet.Reader
	fields  []string
	nodes   []parquet.Node
	indices []int
	closed  bool
}

func mysqlDataOpenParquetRows(input io.ReaderAt, size int64) (*mysqlDataParquetRows, error) {
	f, err := parquet.OpenFile(input, size, parquet.SkipPageIndex(true), parquet.ReadBufferSize(64<<10))
	if err != nil {
		return nil, err
	}
	paths := f.Schema().Columns()
	if len(paths) == 0 || len(paths) > 128 {
		return nil, contract.ErrDataPage
	}
	r := &mysqlDataParquetRows{}
	for _, path := range paths {
		if len(path) != 1 {
			return nil, errors.New("nested Parquet columns require an explicit flat mapping")
		}
		leaf, ok := f.Schema().Lookup(path...)
		if !ok || leaf.MaxRepetitionLevel != 0 || !leaf.Node.Leaf() {
			return nil, contract.ErrDataPage
		}
		r.fields = append(r.fields, path[0])
		r.nodes = append(r.nodes, leaf.Node)
		r.indices = append(r.indices, leaf.ColumnIndex)
	}
	if order, ok := f.Lookup("nyadb.column_order"); ok {
		var fields []string
		if err := json.Unmarshal([]byte(order), &fields); err != nil || len(fields) != len(r.fields) {
			return nil, contract.ErrDataPage
		}
		seen := map[string]bool{}
		var nodes []parquet.Node
		var indices []int
		for _, field := range fields {
			leaf, ok := f.Schema().Lookup(field)
			if !ok || seen[field] {
				return nil, contract.ErrDataPage
			}
			seen[field] = true
			nodes = append(nodes, leaf.Node)
			indices = append(indices, leaf.ColumnIndex)
		}
		r.fields, r.nodes, r.indices = fields, nodes, indices
	}
	r.reader = parquet.NewReader(f)
	return r, nil
}

func (r *mysqlDataParquetRows) Fields() []string { return append([]string(nil), r.fields...) }
func (r *mysqlDataParquetRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.reader.Close()
}
func (r *mysqlDataParquetRows) Next(ctx context.Context) ([]contract.DataValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.closed {
		return nil, io.EOF
	}
	rows := make([]parquet.Row, 1)
	n, err := r.reader.ReadRows(rows)
	if n == 0 {
		return nil, err
	}
	if len(rows[0]) != len(r.fields) {
		return nil, contract.ErrDataPage
	}
	byColumn := make([]parquet.Value, len(r.fields))
	seen := make([]bool, len(r.fields))
	for _, v := range rows[0] {
		c := v.Column()
		if c < 0 || c >= len(byColumn) || seen[c] || v.RepetitionLevel() != 0 {
			return nil, contract.ErrDataPage
		}
		seen[c] = true
		byColumn[c] = v
	}
	values := make([]contract.DataValue, len(r.fields))
	total := 0
	for i, index := range r.indices {
		if !seen[index] {
			return nil, contract.ErrDataPage
		}
		value, e := mysqlDataParquetValue(byColumn[index], r.nodes[i])
		if e != nil {
			return nil, fmt.Errorf("Parquet field %s: %w", r.fields[i], e)
		}
		size := len(value.Text) + len(value.Bytes)
		total += size
		if size > contract.DataExportFieldMaxBytes || total > contract.DataExportRowMaxBytes {
			return nil, contract.ErrDataEditLimit
		}
		values[i] = value
	}
	return values, nil
}

func mysqlDataParseParquetImport(data []byte) (*mysqlDataImportInput, error) {
	r, err := mysqlDataOpenParquetRows(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	input := &mysqlDataImportInput{Fields: r.Fields()}
	for {
		row, err := r.Next(context.Background())
		if err == io.EOF {
			return input, nil
		}
		if err != nil {
			return nil, err
		}
		input.Rows = append(input.Rows, row)
	}
}

func mysqlDataParquetValue(v parquet.Value, node parquet.Node) (contract.DataValue, error) {
	if v.IsNull() {
		return contract.DataValue{Null: true}, nil
	}
	var logical parquetformat.LogicalTypeValue
	if lt := node.Type().LogicalType(); lt != nil {
		logical = lt.Value
	}
	switch lt := logical.(type) {
	case *parquetformat.DecimalType:
		if lt.Scale < 0 || lt.Scale > contract.DataExportFieldMaxBytes || lt.Precision <= 0 {
			return contract.DataValue{}, contract.ErrDataPage
		}
		n := new(big.Int)
		switch v.Kind() {
		case parquet.Int32:
			n.SetInt64(int64(v.Int32()))
		case parquet.Int64:
			n.SetInt64(v.Int64())
		case parquet.ByteArray, parquet.FixedLenByteArray:
			b := v.ByteArray()
			if len(b) == 0 || len(b) > contract.DataExportFieldMaxBytes {
				return contract.DataValue{}, contract.ErrDataPage
			}
			n.SetBytes(b)
			if b[0]&0x80 != 0 {
				n.Sub(n, new(big.Int).Lsh(big.NewInt(1), uint(8*len(b))))
			}
		default:
			return contract.DataValue{}, contract.ErrDataPage
		}
		negative := n.Sign() < 0
		n.Abs(n)
		digits := n.String()
		scale := int(lt.Scale)
		if scale > 0 {
			if len(digits) <= scale {
				digits = strings.Repeat("0", scale+1-len(digits)) + digits
			}
			digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
		}
		if negative {
			digits = "-" + digits
		}
		return contract.DataValue{Text: digits}, nil
	case *parquetformat.IntType:
		if !lt.IsSigned {
			if v.Kind() == parquet.Int32 {
				return contract.DataValue{Text: strconv.FormatUint(uint64(v.Uint32()), 10)}, nil
			}
			if v.Kind() == parquet.Int64 {
				return contract.DataValue{Text: strconv.FormatUint(v.Uint64(), 10)}, nil
			}
		}
	case *parquetformat.DateType:
		if v.Kind() != parquet.Int32 {
			return contract.DataValue{}, contract.ErrDataPage
		}
		d := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(v.Int32()))
		if d.Year() < 1 || d.Year() > 9999 {
			return contract.DataValue{}, contract.ErrDataPage
		}
		return contract.DataValue{Text: d.Format("2006-01-02")}, nil
	case *parquetformat.TimestampType:
		if v.Kind() != parquet.Int64 || lt.Unit.Value == nil {
			return contract.DataValue{}, contract.ErrDataPage
		}
		unit := lt.Unit.Value.Duration()
		factor := int64(time.Second / unit)
		raw := v.Int64()
		d := time.Unix(raw/factor, (raw%factor)*int64(unit)).UTC()
		if d.Year() < 1 || d.Year() > 9999 {
			return contract.DataValue{}, contract.ErrDataPage
		}
		return contract.DataValue{Text: d.Format("2006-01-02 15:04:05.999999999")}, nil
	case *parquetformat.TimeType:
		if lt.Unit.Value == nil {
			return contract.DataValue{}, contract.ErrDataPage
		}
		var raw int64
		if v.Kind() == parquet.Int32 {
			raw = int64(v.Int32())
		} else if v.Kind() == parquet.Int64 {
			raw = v.Int64()
		} else {
			return contract.DataValue{}, contract.ErrDataPage
		}
		unit := lt.Unit.Value.Duration()
		factor := int64(time.Second / unit)
		if raw < 0 || raw >= 86400*factor {
			return contract.DataValue{}, contract.ErrDataPage
		}
		d := time.Unix(raw/factor, (raw%factor)*int64(unit)).UTC()
		return contract.DataValue{Text: d.Format("15:04:05.999999999")}, nil
	}
	value := contract.DataValue{}
	switch v.Kind() {
	case parquet.Boolean:
		if v.Boolean() {
			value.Text = "1"
		} else {
			value.Text = "0"
		}
	case parquet.Int32:
		value.Text = strconv.FormatInt(int64(v.Int32()), 10)
	case parquet.Int64:
		value.Text = strconv.FormatInt(v.Int64(), 10)
	case parquet.Float:
		if math.IsNaN(float64(v.Float())) || math.IsInf(float64(v.Float()), 0) {
			return value, contract.ErrDataPage
		}
		value.Text = strconv.FormatFloat(float64(v.Float()), 'g', -1, 32)
	case parquet.Double:
		if math.IsNaN(v.Double()) || math.IsInf(v.Double(), 0) {
			return value, contract.ErrDataPage
		}
		value.Text = strconv.FormatFloat(v.Double(), 'g', -1, 64)
	case parquet.ByteArray, parquet.FixedLenByteArray:
		switch logical.(type) {
		case *parquetformat.StringType, *parquetformat.JsonType, *parquetformat.EnumType:
			if !utf8.Valid(v.ByteArray()) {
				return value, contract.ErrDataPage
			}
			value.Text = string(v.ByteArray())
		default:
			value.Binary = true
			value.Bytes = bytes.Clone(v.ByteArray())
			if value.Bytes == nil {
				value.Bytes = []byte{}
			}
		}
	default:
		return value, errors.New("unsupported Parquet logical/physical type")
	}
	return value, nil
}
