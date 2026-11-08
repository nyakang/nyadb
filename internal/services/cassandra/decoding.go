package cassandra

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"
)

func (r *reader) typ(depth int) *Type {
	if r.err != nil {
		return nil
	}
	r.types++
	if depth > maxDepth || r.types > maxItems {
		r.err = ErrBounds
		return nil
	}
	t := &Type{Code: r.u16()}
	switch t.Code {
	case 0:
		t.Custom = r.str()
		if t.Custom == "" {
			r.err = ErrProtocol
		}
	case 0x20, 0x22:
		t.Arguments = []*Type{r.typ(depth + 1)}
	case 0x21:
		t.Arguments = []*Type{r.typ(depth + 1), r.typ(depth + 1)}
	case 0x30:
		t.Keyspace, t.Name = r.str(), r.str()
		n := int(r.u16())
		if n > MaxColumns {
			r.err = ErrBounds
			return t
		}
		t.Fields = make([]Field, n)
		for i := range t.Fields {
			t.Fields[i] = Field{r.str(), r.typ(depth + 1)}
		}
	case 0x31:
		n := int(r.u16())
		if n > MaxColumns {
			r.err = ErrBounds
			return t
		}
		t.Arguments = make([]*Type, n)
		for i := range t.Arguments {
			t.Arguments[i] = r.typ(depth + 1)
		}
	default:
		if t.Code < 1 || t.Code > 0x14 {
			r.err = ErrProtocol
		}
	}
	return t
}
func signedInteger(b []byte) *big.Int {
	n := new(big.Int).SetBytes(b)
	if len(b) > 0 && b[0]&0x80 != 0 {
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), uint(8*len(b))))
	}
	return n
}
func decimalText(b []byte) (string, error) {
	if len(b) < 5 {
		return "", ErrProtocol
	}
	scale := int64(int32(binary.BigEndian.Uint32(b)))
	coefficient := signedInteger(b[4:]).String()
	// Extreme native scales remain exact without allocating billions of zeroes.
	if scale > 4096 || scale < -4096 {
		return coefficient + "e" + strconv.FormatInt(-scale, 10), nil
	}
	sign := ""
	if strings.HasPrefix(coefficient, "-") {
		sign = "-"
		coefficient = coefficient[1:]
	}
	if scale <= 0 {
		return sign + coefficient + strings.Repeat("0", int(-scale)), nil
	}
	if int64(len(coefficient)) <= scale {
		return sign + "0." + strings.Repeat("0", int(scale)-len(coefficient)) + coefficient, nil
	}
	n := len(coefficient) - int(scale)
	return sign + coefficient[:n] + "." + coefficient[n:], nil
}
func vint(r *reader) int64 {
	first := r.u8()
	n := 0
	for mask := byte(0x80); mask != 0 && first&mask != 0; mask >>= 1 {
		n++
	}
	var v uint64
	if n < 8 {
		v = uint64(first & (0x7f >> n))
	}
	for _, b := range r.take(n) {
		v = (v << 8) | uint64(b)
	}
	return int64(v>>1) ^ -int64(v&1)
}
func durationText(b []byte) (string, error) {
	r := &reader{data: b}
	m, d, n := vint(r), vint(r), vint(r)
	if r.done() != nil || m < math.MinInt32 || m > math.MaxInt32 || d < math.MinInt32 || d > math.MaxInt32 {
		return "", ErrProtocol
	}
	negative := m < 0 || d < 0 || n < 0
	positive := m > 0 || d > 0 || n > 0
	if negative && positive {
		return "", ErrProtocol
	}
	abs := func(v int64) string {
		if v >= 0 {
			return strconv.FormatInt(v, 10)
		}
		return new(big.Int).Abs(big.NewInt(v)).String()
	}
	sign := ""
	if negative {
		sign = "-"
	}
	return sign + abs(m) + "mo" + abs(d) + "d" + abs(n) + "ns", nil
}
func (r *reader) value(t *Type, b []byte, isNull bool, depth int) Value {
	v := Value{Type: t, Raw: b, Null: isNull}
	r.cells++
	if r.cells > maxCells || depth > maxDepth {
		r.err = ErrBounds
		return v
	}
	if r.err != nil || isNull {
		return v
	}
	if t == nil {
		r.err = ErrProtocol
		return v
	}
	sized := func(n int) bool {
		if len(b) != n {
			r.err = ErrProtocol
			return false
		}
		return true
	}
	switch t.Code {
	case 0: // Unknown custom types stay opaque with their original type/bytes.
		// Cassandra exposes duration as this custom marshal type in protocol v4;
		// its dedicated 0x15 type identifier belongs to newer protocol versions.
		if t.Custom == "org.apache.cassandra.db.marshal.DurationType" {
			var e error
			v.Text, e = durationText(b)
			if e != nil {
				r.err = e
			}
		}
	case 1:
		for _, x := range b {
			if x > 127 {
				r.err = ErrProtocol
				return v
			}
		}
		v.Text = string(b)
	case 0x0a, 0x0d:
		if !utf8.Valid(b) {
			r.err = ErrProtocol
			return v
		}
		v.Text = string(b)
	case 2, 5, 0x0b:
		if sized(8) {
			v.Text = strconv.FormatInt(int64(binary.BigEndian.Uint64(b)), 10)
		}
	case 3:
		v.Text = base64.StdEncoding.EncodeToString(b)
	case 4:
		if sized(1) {
			if b[0] > 1 {
				r.err = ErrProtocol
			} else {
				v.Text = strconv.FormatBool(b[0] == 1)
			}
		}
	case 6:
		if len(b) > 4096 {
			r.err = ErrBounds
			return v
		}
		var e error
		v.Text, e = decimalText(b)
		if e != nil {
			r.err = e
		}
	case 7:
		if sized(8) {
			v.Text = strconv.FormatFloat(math.Float64frombits(binary.BigEndian.Uint64(b)), 'g', -1, 64)
		}
	case 8:
		if sized(4) {
			v.Text = strconv.FormatFloat(float64(math.Float32frombits(binary.BigEndian.Uint32(b))), 'g', -1, 32)
		}
	case 9:
		if sized(4) {
			v.Text = strconv.FormatInt(int64(int32(binary.BigEndian.Uint32(b))), 10)
		}
	case 0x0c, 0x0f:
		if sized(16) {
			v.Text = fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
		}
	case 0x0e:
		if len(b) > 4096 {
			r.err = ErrBounds
		} else if len(b) == 0 {
			r.err = ErrProtocol
		} else {
			v.Text = signedInteger(b).String()
		}
	case 0x10:
		if len(b) != 4 && len(b) != 16 {
			r.err = ErrProtocol
		} else {
			v.Text = net.IP(b).String()
		}
	case 0x11:
		if sized(4) {
			v.Text = strconv.FormatInt(int64(binary.BigEndian.Uint32(b))-(1<<31), 10)
		}
	case 0x12:
		if sized(8) {
			n := int64(binary.BigEndian.Uint64(b))
			if n < 0 || n >= 86400*1000000000 {
				r.err = ErrProtocol
			} else {
				v.Text = strconv.FormatInt(n, 10)
			}
		}
	case 0x13:
		if sized(2) {
			v.Text = strconv.FormatInt(int64(int16(binary.BigEndian.Uint16(b))), 10)
		}
	case 0x14:
		if sized(1) {
			v.Text = strconv.FormatInt(int64(int8(b[0])), 10)
		}
	case 0x20, 0x21, 0x22, 0x30, 0x31:
		inner := &reader{data: b, cells: r.cells, decoded: r.decoded}
		item := func(typ *Type, nullable bool) Value {
			if nullable && inner.err == nil && inner.pos == len(inner.data) {
				inner.cells++
				if inner.cells > maxCells {
					inner.err = ErrBounds
				}
				return Value{Type: typ, Missing: true}
			}
			raw, null := inner.blob()
			if null && !nullable {
				inner.err = ErrProtocol
			}
			return inner.value(typ, raw, null, depth+1)
		}
		switch t.Code {
		case 0x20, 0x22:
			n := int(inner.i32())
			if n < 0 || n > maxItems {
				inner.err = ErrBounds
				break
			}
			v.Items = make([]Value, n)
			for i := range v.Items {
				v.Items[i] = item(t.Arguments[0], false)
			}
		case 0x21:
			n := int(inner.i32())
			if n < 0 || n > maxItems {
				inner.err = ErrBounds
				break
			}
			v.Entries = make([]Entry, n)
			for i := range v.Entries {
				v.Entries[i] = Entry{item(t.Arguments[0], false), item(t.Arguments[1], false)}
			}
		case 0x30:
			v.Fields = make([]NamedValue, len(t.Fields))
			for i, f := range t.Fields {
				v.Fields[i] = NamedValue{f.Name, item(f.Type, true)}
			}
		case 0x31:
			v.Items = make([]Value, len(t.Arguments))
			for i, typ := range t.Arguments {
				v.Items[i] = item(typ, true)
			}
		}
		r.cells, r.decoded = inner.cells, inner.decoded
		if e := inner.done(); e != nil {
			r.err = e
		}
	}
	r.decoded += len(v.Text)
	if r.decoded > maxDecoded {
		r.err = ErrBounds
	}
	return v
}
func decodeResult(f frame) (*Result, []byte, error) {
	r, warnings, e := frameReader(f)
	if e != nil {
		return nil, nil, e
	}
	if f.opcode == 0 {
		code := r.i32()
		_ = r.str()
		switch code {
		case 0, 0xa, 0x100, 0x1001, 0x1002, 0x1003, 0x2000, 0x2100, 0x2200, 0x2300:
		case 0x1000:
			r.u16()
			r.i32()
			r.i32()
		case 0x1100, 0x1500:
			r.u16()
			r.i32()
			r.i32()
			if code == 0x1500 {
				r.i32()
			}
			r.str()
		case 0x1200, 0x1300:
			r.u16()
			r.i32()
			r.i32()
			if code == 0x1300 {
				r.i32()
			}
			r.u8()
		case 0x1400:
			r.str()
			r.str()
			r.list()
		case 0x2400:
			r.str()
			r.str()
		case 0x2500:
			r.take(int(r.u16()))
		default:
			r.err = ErrProtocol
		}
		if e := r.done(); e != nil {
			return nil, nil, e
		}
		return nil, nil, &ServerError{Code: code}
	}
	if f.opcode != 8 {
		return nil, nil, ErrProtocol
	}
	out := &Result{Kind: r.i32(), Warnings: warnings}
	var state []byte
	switch out.Kind {
	case 1: // VOID is an acknowledgement, not an affected-row or rollback receipt.
	case 2:
		flags, n := r.i32(), int(r.i32())
		if flags & ^int32(3) != 0 || n < 0 || n > MaxColumns {
			return nil, nil, ErrProtocol
		}
		if flags&2 != 0 {
			b, null := r.blob()
			if null || len(b) == 0 || len(b) > maxPageState {
				return nil, nil, ErrProtocol
			}
			state = append([]byte(nil), b...)
		}
		ks, table := "", ""
		if flags&1 != 0 {
			ks, table = r.str(), r.str()
		}
		out.Columns = make([]Column, n)
		for i := range out.Columns {
			col := Column{Keyspace: ks, Table: table}
			if flags&1 == 0 {
				col.Keyspace, col.Table = r.str(), r.str()
			}
			col.Name, col.Type = r.str(), r.typ(0)
			out.Columns[i] = col
		}
		rows := int(r.i32())
		if rows < 0 || rows > MaxRows || int64(rows)*int64(n) > maxCells {
			return nil, nil, ErrBounds
		}
		if r.err != nil {
			return nil, nil, r.err
		}
		out.Rows = make([][]Value, rows)
		for i := range out.Rows {
			out.Rows[i] = make([]Value, n)
			for j, col := range out.Columns {
				b, null := r.blob()
				out.Rows[i][j] = r.value(col.Type, b, null, 0)
			}
		}
	case 3:
		out.Keyspace = r.str()
	case 5:
		s := &SchemaChange{Change: r.str(), Target: r.str(), Keyspace: r.str()}
		if s.Change != "CREATED" && s.Change != "UPDATED" && s.Change != "DROPPED" || s.Keyspace == "" {
			r.err = ErrProtocol
		}
		switch s.Target {
		case "KEYSPACE":
		case "TABLE", "TYPE":
			s.Name = r.str()
		case "FUNCTION", "AGGREGATE":
			s.Name = r.str()
			s.ArgumentTypes = r.list()
		default:
			r.err = ErrProtocol
		}
		if s.Target != "KEYSPACE" && s.Name == "" {
			r.err = ErrProtocol
		}
		out.SchemaChange = s
	default:
		return nil, nil, ErrProtocol
	}
	if e := r.done(); e != nil {
		return nil, nil, e
	}
	return out, state, nil
}
