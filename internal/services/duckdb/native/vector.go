package native

import (
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unsafe"
)

type dateValue struct{ Days int32 }
type timeValue struct{ Value int64 }
type unsignedValue struct{ Value uint64 }
type intervalValue struct {
	Months, Days int32
	Micros       int64
}
type hugeValue struct {
	Lower uint64
	Upper int64
}
type listValue struct{ Offset, Length uint64 }

var ErrResultType = errors.New("DuckDB result type is unsupported; CAST the value to VARCHAR")

func cstring(p unsafe.Pointer) (string, error) {
	if p == nil {
		return "", errors.New("DuckDB value conversion failed")
	}
	base := p
	for i := 0; i < 8<<20; i++ {
		if *(*byte)(unsafe.Add(base, i)) == 0 {
			return string(unsafe.Slice((*byte)(base), i)), nil
		}
	}
	return "", errors.New("DuckDB value exceeds the read limit")
}
func (a *api) scalarString(value uintptr) (driver.Value, error) {
	defer a.destroyValue(&value)
	p := a.valueToString(value)
	defer a.free(p)
	s, err := cstring(p)
	if err != nil {
		return nil, err
	}
	// DuckDB's value_to_string uses SQL literal notation for temporal values
	// such as TIMESTAMP_NS. The driver must publish their value, without a
	// quote/cast wrapper, so typed grid edits and exports can round-trip them.
	if strings.HasPrefix(s, "'") {
		if end := strings.LastIndex(s, "'::"); end > 0 {
			s = strings.ReplaceAll(s[1:end], "''", "'")
		} else if strings.HasSuffix(s, "'") {
			s = strings.ReplaceAll(s[1:len(s)-1], "''", "'")
		}
	}
	return s, nil
}
func signedHuge(h hugeValue) *big.Int {
	n := new(big.Int).Lsh(big.NewInt(h.Upper), 64)
	return n.Add(n, new(big.Int).SetUint64(h.Lower))
}
func decimalText(n *big.Int, scale uint8) string {
	negative := n.Sign() < 0
	v := new(big.Int).Abs(n).String()
	if scale > 0 {
		for len(v) <= int(scale) {
			v = "0" + v
		}
		at := len(v) - int(scale)
		v = v[:at] + "." + v[at:]
	}
	if negative {
		v = "-" + v
	}
	return v
}
func (a *api) vectorValue(vector uintptr, row uint64, depth int) (driver.Value, error) {
	if depth > 32 {
		return nil, errors.New("DuckDB nested value exceeds the depth limit")
	}
	validity := a.vectorValidity(vector)
	if validity != nil && *(*uint64)(unsafe.Add(validity, (row/64)*8))&(1<<(row%64)) == 0 {
		return nil, nil
	}
	t := a.vectorType(vector)
	defer a.destroyType(&t)
	id := a.typeID(t)
	p := a.vectorData(vector)
	if p == nil && id != 25 && id != 28 {
		return nil, fmt.Errorf("%w (type %d)", ErrResultType, id)
	}
	signed := func(size uint64) int64 {
		v := unsafe.Add(p, row*size)
		switch size {
		case 1:
			return int64(*(*int8)(v))
		case 2:
			return int64(*(*int16)(v))
		case 4:
			return int64(*(*int32)(v))
		default:
			return *(*int64)(v)
		}
	}
	unsigned := func(size uint64) uint64 {
		v := unsafe.Add(p, row*size)
		switch size {
		case 1:
			return uint64(*(*uint8)(v))
		case 2:
			return uint64(*(*uint16)(v))
		case 4:
			return uint64(*(*uint32)(v))
		default:
			return *(*uint64)(v)
		}
	}
	switch id {
	case 1:
		return strconv.FormatBool(unsigned(1) != 0), nil
	case 2:
		return strconv.FormatInt(signed(1), 10), nil
	case 3:
		return strconv.FormatInt(signed(2), 10), nil
	case 4:
		return strconv.FormatInt(signed(4), 10), nil
	case 5:
		return strconv.FormatInt(signed(8), 10), nil
	case 6:
		return strconv.FormatUint(unsigned(1), 10), nil
	case 7:
		return strconv.FormatUint(unsigned(2), 10), nil
	case 8:
		return strconv.FormatUint(unsigned(4), 10), nil
	case 9:
		return strconv.FormatUint(unsigned(8), 10), nil
	case 10:
		return strconv.FormatFloat(float64(*(*float32)(unsafe.Add(p, row*4))), 'g', -1, 32), nil
	case 11:
		return strconv.FormatFloat(*(*float64)(unsafe.Add(p, row*8)), 'g', -1, 64), nil
	case 16:
		return signedHuge(*(*hugeValue)(unsafe.Add(p, row*16))).String(), nil
	case 32:
		h := *(*hugeValue)(unsafe.Add(p, row*16))
		n := new(big.Int).SetUint64(uint64(h.Upper))
		n.Lsh(n, 64)
		n.Add(n, new(big.Int).SetUint64(h.Lower))
		return n.String(), nil
	case 19:
		width, scale := a.decimalWidth(t), a.decimalScale(t)
		n := new(big.Int)
		switch {
		case width <= 4:
			n.SetInt64(signed(2))
		case width <= 9:
			n.SetInt64(signed(4))
		case width <= 18:
			n.SetInt64(signed(8))
		default:
			n = signedHuge(*(*hugeValue)(unsafe.Add(p, row*16)))
		}
		return decimalText(n, scale), nil
	case 17, 18:
		s := unsafe.Add(p, row*16)
		length := *(*uint32)(s)
		if length > 8<<20 {
			return nil, errors.New("DuckDB value exceeds the read limit")
		}
		data := unsafe.Add(s, 4)
		if length > 12 {
			data = *(*unsafe.Pointer)(unsafe.Add(s, 8))
		}
		b := append([]byte{}, unsafe.Slice((*byte)(data), int(length))...)
		if id == 18 {
			return b, nil
		}
		return string(b), nil
	case 12:
		return a.scalarString(a.createTimestamp(timeValue{signed(8)}))
	case 13:
		return a.scalarString(a.createDate(dateValue{int32(signed(4))}))
	case 14:
		return a.scalarString(a.createTime(timeValue{signed(8)}))
	case 15:
		return a.scalarString(a.createInterval(*(*intervalValue)(unsafe.Add(p, row*16))))
	case 20:
		return a.scalarString(a.createTimestampS(timeValue{signed(8)}))
	case 21:
		return a.scalarString(a.createTimestampMS(timeValue{signed(8)}))
	case 22:
		return a.scalarString(a.createTimestampNS(timeValue{signed(8)}))
	case 30:
		return a.scalarString(a.createTimeTZ(unsignedValue{unsigned(8)}))
	case 31:
		return a.scalarString(a.createTimestampTZ(timeValue{signed(8)}))
	case 39:
		return a.scalarString(a.createTimeNS(timeValue{signed(8)}))
	case 28:
		tag := a.structChild(vector, 0)
		tagValue, err := a.vectorValue(tag, row, depth+1)
		if err != nil {
			return nil, err
		}
		index, err := strconv.ParseUint(fmt.Sprint(tagValue), 10, 64)
		if err != nil {
			return nil, err
		}
		return a.vectorValue(a.structChild(vector, index+1), row, depth+1)
	case 27:
		h := *(*hugeValue)(unsafe.Add(p, row*16))
		n := new(big.Int).SetUint64(uint64(h.Upper) ^ (1 << 63))
		n.Lsh(n, 64)
		n.Add(n, new(big.Int).SetUint64(h.Lower))
		raw := fmt.Sprintf("%032x", n)
		return raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:], nil
	case 23:
		internal := a.enumInternal(t)
		size := uint64(1)
		if internal == 7 {
			size = 2
		}
		if internal == 8 {
			size = 4
		}
		s := a.enumValue(t, unsigned(size))
		defer a.free(s)
		return cstring(s)
	case 24, 26, 33:
		var start, count uint64
		var child uintptr
		if id == 33 {
			count = a.arraySize(t)
			start = row * count
			child = a.arrayChild(vector)
		} else {
			entry := *(*listValue)(unsafe.Add(p, row*16))
			start, count = entry.Offset, entry.Length
			child = a.listChild(vector)
		}
		if count > 100000 {
			return nil, errors.New("DuckDB nested value exceeds the entry limit")
		}
		var parts []string
		budget := 0
		for i := uint64(0); i < count; i++ {
			s, e := a.nestedVectorText(child, start+i, depth+1)
			if e != nil {
				return nil, e
			}
			budget += len(s)
			if budget > 8<<20 {
				return nil, errors.New("DuckDB nested value exceeds the read limit")
			}
			parts = append(parts, s)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case 25:
		count := a.structCount(t)
		if count > 4096 {
			return nil, errors.New("DuckDB struct exceeds the entry limit")
		}
		var parts []string
		budget := 0
		for i := uint64(0); i < count; i++ {
			name := a.structName(t, i)
			label, e := cstring(name)
			a.free(name)
			if e != nil {
				return nil, e
			}
			value, e := a.nestedVectorText(a.structChild(vector, i), row, depth+1)
			if e != nil {
				return nil, e
			}
			s := nestedText(label) + ": " + value
			budget += len(s)
			if budget > 8<<20 {
				return nil, errors.New("DuckDB nested value exceeds the read limit")
			}
			parts = append(parts, s)
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	default:
		return nil, fmt.Errorf("%w (type %d)", ErrResultType, id)
	}
}
func (a *api) nestedVectorText(vector uintptr, row uint64, depth int) (string, error) {
	v, err := a.vectorValue(vector, row, depth)
	if err != nil {
		return "", err
	}
	if v == nil {
		return "NULL", nil
	}
	t := a.vectorType(vector)
	id := a.typeID(t)
	a.destroyType(&t)
	if id == 17 || id == 23 || id == 27 || id == 12 || id == 13 || id == 14 || id == 15 || id == 20 || id == 21 || id == 22 || id == 30 || id == 31 || id == 39 {
		return nestedText(v), nil
	}
	if _, ok := v.([]byte); ok {
		return nestedText(v), nil
	}
	return fmt.Sprint(v), nil
}
func nestedText(v driver.Value) string {
	if v == nil {
		return "NULL"
	}
	if b, ok := v.([]byte); ok {
		return "0x" + hex.EncodeToString(b)
	}
	return "'" + strings.ReplaceAll(fmt.Sprint(v), "'", "''") + "'"
}
