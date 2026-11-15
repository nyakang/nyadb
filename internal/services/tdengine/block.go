package tdengine

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var nativeTypes = []string{"NULL", "BOOL", "TINYINT", "SMALLINT", "INT", "BIGINT", "FLOAT", "DOUBLE", "VARCHAR", "TIMESTAMP", "NCHAR", "TINYINT UNSIGNED", "SMALLINT UNSIGNED", "INT UNSIGNED", "BIGINT UNSIGNED", "JSON", "VARBINARY", "DECIMAL", "BLOB", "MEDIUMBLOB", "GEOMETRY", "DECIMAL"}

func typeName(id byte) (string, error) {
	if int(id) >= len(nativeTypes) {
		return "", contract.ErrUnsupported
	}
	return nativeTypes[id], nil
}

func valueKind(decl string) contract.CellKind {
	decl = strings.ToUpper(decl)
	switch {
	case decl == "BOOL" || decl == "BOOLEAN":
		return contract.Boolean
	case decl == "TIMESTAMP":
		return contract.DateTime
	case strings.HasPrefix(decl, "DECIMAL"):
		return contract.Decimal
	case decl == "FLOAT" || decl == "DOUBLE":
		return contract.Floating
	case strings.Contains(decl, "INT"):
		return contract.Integer
	case strings.HasPrefix(decl, "VARBINARY") || decl == "GEOMETRY" || strings.Contains(decl, "BLOB"):
		return contract.Binary
	case decl == "JSON":
		return contract.JSON
	default:
		return contract.Text
	}
}

// decodeBlock uses checked slices only. Each column owns a separate bitmap or
// offset vector; native NULL remains distinct from an empty string or byte span.
func decodeBlock(raw []byte, types []byte, precision int) ([][]contract.Cell, error) {
	bad := func() ([][]contract.Cell, error) { return nil, contract.ErrUnsupported }
	cols := len(types)
	if cols == 0 || cols > 512 || len(raw) < 28+9*cols || precision < 0 || precision > 2 {
		return bad()
	}
	version := binary.LittleEndian.Uint32(raw)
	rows64 := uint64(binary.LittleEndian.Uint32(raw[8:]))
	if version < 1 || version > 3 || int(binary.LittleEndian.Uint32(raw[4:])) != len(raw) || int(binary.LittleEndian.Uint32(raw[12:])) != cols || rows64 > 1<<20 || rows64*uint64(cols) > 1<<20 {
		return bad()
	}
	rows := int(rows64)
	values := make([][]contract.Cell, rows)
	for r := range values {
		values[r] = make([]contract.Cell, cols)
	}
	position := 28 + 9*cols
	for col, typ := range types {
		if int(typ) >= len(nativeTypes) || raw[28+5*col] != typ {
			return bad()
		}
		length := int(binary.LittleEndian.Uint32(raw[28+5*cols+4*col:]))
		variable := typ == 8 || typ == 10 || typ == 15 || typ == 16 || typ == 18 || typ == 19 || typ == 20
		header := (rows + 7) / 8
		if variable {
			header = 4 * rows
		}
		if position > len(raw) || header > len(raw)-position || length > len(raw)-position-header {
			return bad()
		}
		h := raw[position : position+header]
		data := raw[position+header : position+header+length]
		position += header + length
		if variable {
			for r := 0; r < rows; r++ {
				offset := int(int32(binary.LittleEndian.Uint32(h[r*4:])))
				if offset == -1 {
					values[r][col] = contract.Cell{Kind: contract.Null}
					continue
				}
				prefix := 2
				if typ == 18 || typ == 19 {
					prefix = 4
				}
				if offset < 0 || offset > len(data)-prefix {
					return bad()
				}
				count := int(binary.LittleEndian.Uint16(data[offset:]))
				if prefix == 4 {
					count = int(binary.LittleEndian.Uint32(data[offset:]))
				}
				if count > len(data)-offset-prefix {
					return bad()
				}
				v, err := variableCell(data[offset+prefix:offset+prefix+count], typ)
				if err != nil {
					return nil, err
				}
				values[r][col] = v
			}
			continue
		}
		size := map[byte]int{0: 1, 1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 4, 7: 8, 9: 8, 11: 1, 12: 2, 13: 4, 14: 8, 17: 16, 21: 8}[typ]
		if size == 0 || length != rows*size {
			return bad()
		}
		scale := int(raw[28+5*col+1])
		for r := 0; r < rows; r++ {
			if typ == 0 || h[r/8]&(1<<uint(7-r%8)) != 0 {
				values[r][col] = contract.Cell{Kind: contract.Null}
				continue
			}
			v, err := fixedCell(data[r*size:(r+1)*size], typ, precision, scale)
			if err != nil {
				return nil, err
			}
			values[r][col] = v
		}
	}
	// Some native blocks append column segments after the value area. The
	// declared block length still bounds all accesses; no segment is dereferenced.
	return values, nil
}

func variableCell(raw []byte, typ byte) (contract.Cell, error) {
	if typ == 10 {
		if len(raw)%4 != 0 {
			return contract.Cell{}, contract.ErrUnsupported
		}
		runes := make([]rune, len(raw)/4)
		for i := range runes {
			r := rune(binary.LittleEndian.Uint32(raw[4*i:]))
			if !utf8.ValidRune(r) {
				return contract.Cell{}, contract.ErrUnsupported
			}
			runes[i] = r
		}
		return contract.Cell{Kind: contract.Text, Text: string(runes)}, nil
	}
	if typ == 16 || typ == 18 || typ == 19 || typ == 20 || !utf8.Valid(raw) {
		return contract.Cell{Kind: contract.Binary, Bytes: bytes.Clone(raw)}, nil
	}
	kind := contract.Text
	if typ == 15 {
		if !json.Valid(raw) {
			return contract.Cell{}, contract.ErrUnsupported
		}
		kind = contract.JSON
	}
	return contract.Cell{Kind: kind, Text: string(raw)}, nil
}

func fixedCell(raw []byte, typ byte, precision, scale int) (contract.Cell, error) {
	c := contract.Cell{Kind: contract.Integer}
	switch typ {
	case 1:
		if raw[0] > 1 {
			return c, contract.ErrUnsupported
		}
		c.Kind, c.Text = contract.Boolean, strconv.FormatBool(raw[0] == 1)
	case 2:
		c.Text = strconv.FormatInt(int64(int8(raw[0])), 10)
	case 3:
		c.Text = strconv.FormatInt(int64(int16(binary.LittleEndian.Uint16(raw))), 10)
	case 4:
		c.Text = strconv.FormatInt(int64(int32(binary.LittleEndian.Uint32(raw))), 10)
	case 5:
		c.Text = strconv.FormatInt(int64(binary.LittleEndian.Uint64(raw)), 10)
	case 11:
		c.Text = strconv.FormatUint(uint64(raw[0]), 10)
	case 12:
		c.Text = strconv.FormatUint(uint64(binary.LittleEndian.Uint16(raw)), 10)
	case 13:
		c.Text = strconv.FormatUint(uint64(binary.LittleEndian.Uint32(raw)), 10)
	case 14:
		c.Text = strconv.FormatUint(binary.LittleEndian.Uint64(raw), 10)
	case 6:
		c.Kind, c.Text = contract.Floating, strconv.FormatFloat(float64(math.Float32frombits(binary.LittleEndian.Uint32(raw))), 'g', -1, 32)
	case 7:
		c.Kind, c.Text = contract.Floating, strconv.FormatFloat(math.Float64frombits(binary.LittleEndian.Uint64(raw)), 'g', -1, 64)
	case 9:
		n := int64(binary.LittleEndian.Uint64(raw))
		units := []int64{1000, 1000000, 1000000000}[precision]
		layouts := []string{"2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05.000000Z", "2006-01-02T15:04:05.000000000Z"}
		c.Kind, c.Text = contract.DateTime, time.Unix(n/units, (n%units)*(1000000000/units)).UTC().Format(layouts[precision])
	case 17, 21:
		if scale > 38 || typ == 21 && scale > 18 {
			return c, contract.ErrUnsupported
		}
		b := bytes.Clone(raw)
		for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
			b[i], b[j] = b[j], b[i]
		}
		n := new(big.Int).SetBytes(b)
		if b[0]&0x80 != 0 {
			n.Sub(n, new(big.Int).Lsh(big.NewInt(1), uint(8*len(b))))
		}
		s := n.String()
		sign := ""
		if strings.HasPrefix(s, "-") {
			sign, s = "-", s[1:]
		}
		for len(s) <= scale {
			s = "0" + s
		}
		if scale > 0 {
			s = s[:len(s)-scale] + "." + s[len(s)-scale:]
		}
		c.Kind, c.Text = contract.Decimal, sign+s
	default:
		return c, contract.ErrUnsupported
	}
	return c, nil
}
