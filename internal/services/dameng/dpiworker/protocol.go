// Package dpiworker confines each caller-installed native client and its
// process-wide settings to one original physical session.
package dpiworker

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strconv"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

const entryFlag = "--nyadb-dpi-worker-v1"
const maxFrame = 16 << 20

var errProtocol = errors.New("DM8 private native worker protocol is invalid")

type value struct {
	Kind  string `json:"kind"`
	Text  string `json:"text,omitempty"`
	Bytes []byte `json:"bytes,omitempty"`
}

func encodeValue(v any) (value, error) {
	switch v := v.(type) {
	case nil:
		return value{Kind: "null"}, nil
	case string:
		if len(v) <= 1<<20 && utf8.ValidString(v) {
			return value{Kind: "text", Text: v}, nil
		}
	case []byte:
		if len(v) <= 1<<20 {
			return value{Kind: "bytes", Bytes: append([]byte(nil), v...)}, nil
		}
	case int64:
		return value{Kind: "int64", Text: strconv.FormatInt(v, 10)}, nil
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			return value{Kind: "float64", Text: strconv.FormatFloat(v, 'g', -1, 64)}, nil
		}
	case bool:
		return value{Kind: "bool", Text: strconv.FormatBool(v)}, nil
	case dpi.Decimal:
		if len(v) <= 128 {
			return value{Kind: "decimal", Text: string(v)}, nil
		}
	}
	return value{}, errors.New("DM8 private worker value is unsupported or exceeds its bounds")
}

func decodeValue(v value) (any, error) {
	if len(v.Text) > 1<<20 || len(v.Bytes) > 1<<20 || !utf8.ValidString(v.Text) {
		return nil, errProtocol
	}
	switch v.Kind {
	case "null":
		if v.Text == "" && len(v.Bytes) == 0 {
			return nil, nil
		}
	case "text":
		if len(v.Bytes) == 0 {
			return v.Text, nil
		}
	case "bytes":
		if v.Text == "" {
			return append([]byte{}, v.Bytes...), nil
		}
	case "int64":
		if n, err := strconv.ParseInt(v.Text, 10, 64); err == nil && strconv.FormatInt(n, 10) == v.Text && len(v.Bytes) == 0 {
			return n, nil
		}
	case "float64":
		if n, err := strconv.ParseFloat(v.Text, 64); err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && len(v.Bytes) == 0 {
			return n, nil
		}
	case "bool":
		if (v.Text == "true" || v.Text == "false") && len(v.Bytes) == 0 {
			return v.Text == "true", nil
		}
	case "decimal":
		if len(v.Text) <= 128 && len(v.Bytes) == 0 {
			return dpi.Decimal(v.Text), nil
		}
	}
	return nil, errProtocol
}

type request struct {
	ID       uint64           `json:"id"`
	Method   string           `json:"method"`
	Deadline int64            `json:"deadline,omitempty"`
	Handle   uint64           `json:"handle,omitempty"`
	Owner    uint64           `json:"owner,omitempty"`
	SQL      string           `json:"sql,omitempty"`
	Values   []value          `json:"values,omitempty"`
	Options  *dpi.Options     `json:"options,omitempty"`
	Password string           `json:"password,omitempty"`
	Tx       driver.TxOptions `json:"tx,omitempty"`
}

type column struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	Scan          string `json:"scan"`
	Nullable      bool   `json:"nullable"`
	NullableKnown bool   `json:"nullable_known"`
	Length        int64  `json:"length"`
	LengthKnown   bool   `json:"length_known"`
	Precision     int64  `json:"precision"`
	Scale         int64  `json:"scale"`
	DecimalKnown  bool   `json:"decimal_known"`
}

type failure struct {
	Message  string     `json:"message"`
	Canceled bool       `json:"canceled"`
	Deadline bool       `json:"deadline"`
	Closed   bool       `json:"closed"`
	Unknown  bool       `json:"unknown"`
	Native   *dpi.Error `json:"native,omitempty"`
}

func encodeError(err error) *failure {
	if err == nil {
		return nil
	}
	var native *dpi.Error
	errors.As(err, &native)
	return &failure{Native: native, Message: err.Error(), Canceled: errors.Is(err, context.Canceled), Deadline: errors.Is(err, context.DeadlineExceeded), Closed: errors.Is(err, dpi.ErrSessionClosed), Unknown: errors.Is(err, dpi.ErrOutcomeUnknown)}
}

func (f *failure) err() error {
	if f == nil {
		return nil
	}
	var joined []error
	if f.Native != nil {
		joined = append(joined, f.Native)
	}
	joined = append(joined, errors.New(f.Message))
	if f.Canceled {
		joined = append(joined, context.Canceled)
	}
	if f.Deadline {
		joined = append(joined, context.DeadlineExceeded)
	}
	if f.Closed {
		joined = append(joined, dpi.ErrSessionClosed)
	}
	if f.Unknown {
		joined = append(joined, dpi.ErrOutcomeUnknown)
	}
	return errors.Join(joined...)
}

type response struct {
	ID            uint64   `json:"id"`
	Handle        uint64   `json:"handle,omitempty"`
	Affected      int64    `json:"affected,omitempty"`
	AffectedError *failure `json:"affected_error,omitempty"`
	Outputs       []value  `json:"outputs,omitempty"`
	Inputs        int      `json:"inputs,omitempty"`
	Columns       []column `json:"columns,omitempty"`
	Values        []value  `json:"values,omitempty"`
	EOF           bool     `json:"eof,omitempty"`
	Valid         bool     `json:"valid"`
	Error         *failure `json:"error,omitempty"`
}

func marshalFrame(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil || len(data) == 0 || len(data) > maxFrame {
		return nil, errProtocol
	}
	frame := make([]byte, 8+len(data))
	copy(frame[:4], "NDPI")
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(data)))
	copy(frame[8:], data)
	return frame, nil
}
func writeBytes(w io.Writer, b []byte) error {
	for len(b) != 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n < 1 || n > len(b) {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func writeFrame(w io.Writer, v any) error {
	frame, err := marshalFrame(v)
	if err != nil {
		return err
	}
	return writeBytes(w, frame)
}

func readFrame(r io.Reader, v any) error {
	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(header[4:])
	if string(header[:4]) != "NDPI" || n == 0 || n > maxFrame {
		return errProtocol
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return errProtocol
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errProtocol
	}
	return nil
}

func columnsFrom(rows driver.Rows) ([]column, error) {
	names := rows.Columns()
	if len(names) > 4096 {
		return nil, errProtocol
	}
	db, d := rows.(driver.RowsColumnTypeDatabaseTypeName)
	null, n := rows.(driver.RowsColumnTypeNullable)
	length, l := rows.(driver.RowsColumnTypeLength)
	precision, p := rows.(driver.RowsColumnTypePrecisionScale)
	scan, s := rows.(driver.RowsColumnTypeScanType)
	if !d || !n || !l || !p || !s {
		return nil, errProtocol
	}
	out := make([]column, len(names))
	for i, name := range names {
		c := &out[i]
		c.Name, c.Type = name, db.ColumnTypeDatabaseTypeName(i)
		c.Nullable, c.NullableKnown = null.ColumnTypeNullable(i)
		c.Length, c.LengthKnown = length.ColumnTypeLength(i)
		c.Precision, c.Scale, c.DecimalKnown = precision.ColumnTypePrecisionScale(i)
		switch scan.ColumnTypeScanType(i) {
		case reflect.TypeFor[int64]():
			c.Scan = "int64"
		case reflect.TypeFor[float64]():
			c.Scan = "float64"
		case reflect.TypeFor[[]byte]():
			c.Scan = "bytes"
		case reflect.TypeFor[string]():
			c.Scan = "text"
		default:
			return nil, errProtocol
		}
	}
	return out, nil
}
