package dpi

import (
	"encoding/binary"
	"errors"
	"math"
	"math/big"
	"runtime"
	"strings"
	"unsafe"
)

// Decimal is a complete plain decimal value. Exponents and more than 38 digits
// are rejected rather than rounded through a floating-point intermediary.
type Decimal string

type numeric struct {
	Precision uint8
	Scale     int8
	Sign      uint8
	Value     [16]byte
}

func encodeDecimal(text string) (numeric, error) {
	var value numeric
	if len(text) > 128 {
		return value, errors.New("DM8 decimal text exceeds its explicit bounds")
	}
	value.Sign = 1
	if strings.HasPrefix(text, "-") {
		value.Sign, text = 0, text[1:]
	} else if strings.HasPrefix(text, "+") {
		text = text[1:]
	}
	parts := strings.Split(text, ".")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return value, errors.New("invalid DM8 decimal")
	}
	scale := 0
	if len(parts) == 2 {
		scale = len(parts[1])
		if scale == 0 {
			return value, errors.New("invalid DM8 decimal")
		}
	}
	digits := strings.Join(parts, "")
	for _, c := range digits {
		if c < '0' || c > '9' {
			return value, errors.New("invalid DM8 decimal")
		}
	}
	precision := max(1, len(strings.TrimLeft(digits, "0")), scale)
	if precision > 38 || scale > 38 {
		return value, errors.New("DM8 decimal exceeds native precision")
	}
	n, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return value, errors.New("invalid DM8 decimal")
	}
	var bytes [16]byte
	n.FillBytes(bytes[:])
	for i := range bytes {
		value.Value[i] = bytes[len(bytes)-1-i]
	}
	value.Precision, value.Scale = uint8(precision), int8(scale)
	return value, nil
}

func decodeDecimal(value numeric) (string, error) {
	if value.Sign > 1 || value.Precision < 1 || value.Precision > 38 || value.Scale > 38 || value.Scale < -38 {
		return "", errors.New("invalid DM8 native decimal representation")
	}
	var bytes [16]byte
	for i := range bytes {
		bytes[i] = value.Value[len(bytes)-1-i]
	}
	n := new(big.Int).SetBytes(bytes[:])
	text := n.String()
	if len(text) > 38 {
		return "", errors.New("DM8 native decimal exceeds precision")
	}
	scale := int(value.Scale)
	if scale > 0 {
		if len(text) <= scale {
			text = strings.Repeat("0", scale-len(text)+1) + text
		}
		text = text[:len(text)-scale] + "." + text[len(text)-scale:]
	} else if scale < 0 {
		text += strings.Repeat("0", -scale)
	}
	if value.Sign == 0 && n.Sign() != 0 {
		text = "-" + text
	}
	return text, nil
}

type binding struct {
	ctype, dtype int16
	precision    uint64
	scale        int16
	bytes        []byte
	length       int64
	pin          runtime.Pinner
}

func newBinding(value any) (*binding, error) {
	b := &binding{ctype: 1003, dtype: 2}
	switch v := value.(type) {
	case nil:
		b.bytes, b.length = []byte{0}, -1
	case string:
		b.bytes = append([]byte(v), 0)
		b.length, b.precision = int64(len(v)), uint64(max(1, len(v)))
	case []byte:
		b.ctype, b.dtype = 12, 18
		b.bytes = append(append([]byte(nil), v...), 0)
		b.length, b.precision = int64(len(v)), uint64(max(1, len(v)))
	case int64:
		b.ctype, b.dtype, b.length, b.precision = 10, 8, 8, 19
		b.bytes = binary.LittleEndian.AppendUint64(nil, uint64(v))
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, errors.New("DM8 nonfinite float binding is unavailable")
		}
		b.ctype, b.dtype, b.length, b.precision = 6, 11, 8, 15
		b.bytes = binary.LittleEndian.AppendUint64(nil, math.Float64bits(v))
	case bool:
		b.ctype, b.dtype, b.length, b.precision = 7, 3, 1, 1
		b.bytes = []byte{0}
		if v {
			b.bytes[0] = 1
		}
	case Decimal:
		n, err := encodeDecimal(string(v))
		if err != nil {
			return nil, err
		}
		b.ctype, b.dtype, b.length = 16, 9, 19
		b.precision, b.scale = uint64(n.Precision), int16(n.Scale)
		b.bytes = append([]byte{n.Precision, byte(n.Scale), n.Sign}, n.Value[:]...)
	default:
		return nil, errors.New("unsupported DM8 native parameter type")
	}
	if len(b.bytes) > 1<<20 {
		return nil, errors.New("DM8 native parameter exceeds 1 MiB")
	}
	b.pin.Pin(&b.bytes[0])
	b.pin.Pin(&b.length)
	return b, nil
}

func (b *binding) pointer() unsafe.Pointer { return unsafe.Pointer(&b.bytes[0]) }
func (b *binding) release()                { b.pin.Unpin() }
