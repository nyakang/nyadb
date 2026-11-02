package engines

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

const postgresDesignCellLimit = 1 << 20
const postgresDesignRowLimit = 16 << 20

type postgresDesignType struct{ base, element uint32 }
type postgresDesignDigest struct {
	Rows int64
	Sum  [32]byte
}

// Type identity comes from the complete captured catalog, not a display type
// name or driver preview. Domains resolve recursively to their storage type;
// custom binary send representations retain their original base type ID.
func postgresDesignTypes(catalog *schemaddl.PostgreSQLCatalog) (map[uint32]postgresDesignType, error) {
	if catalog == nil {
		return nil, schemaddl.ErrUnsupported
	}
	types := map[uint32]postgresDesignType{}
	for _, entry := range catalog.Entries {
		if entry.Kind != "type" {
			continue
		}
		var raw struct {
			Base     json.RawMessage `json:"typbasetype"`
			Element  json.RawMessage `json:"typelem"`
			Category string          `json:"typcategory"`
		}
		if err := json.Unmarshal(entry.Data, &raw); err != nil {
			return nil, err
		}
		parse := func(v json.RawMessage) (uint32, error) {
			if len(v) == 0 {
				return 0, nil
			}
			n, err := strconv.ParseUint(strings.Trim(string(v), `"`), 10, 32)
			return uint32(n), err
		}
		base, err := parse(raw.Base)
		if err != nil {
			return nil, err
		}
		element, err := parse(raw.Element)
		if err != nil {
			return nil, err
		}
		if raw.Category != "A" {
			element = 0
		}
		if entry.OID <= 0 || entry.OID > math.MaxUint32 {
			return nil, schemaddl.ErrUnsupported
		}
		types[uint32(entry.OID)] = postgresDesignType{base: base, element: element}
	}
	return types, nil
}

func postgresResolveType(oid uint32, types map[uint32]postgresDesignType) (uint32, postgresDesignType, error) {
	seen := map[uint32]bool{}
	for depth := 0; depth < 32; depth++ {
		t, ok := types[oid]
		if !ok || seen[oid] {
			return 0, postgresDesignType{}, errors.New("incomplete PostgreSQL value type catalog")
		}
		seen[oid] = true
		if t.base == 0 {
			return oid, t, nil
		}
		oid = t.base
	}
	return 0, postgresDesignType{}, errors.New("PostgreSQL domain depth exceeded")
}

// postgresValueDigest reads bounded native record_send payloads. It does not
// cast values, render text, use collation, paginate or silently truncate. Row
// hashes form a streaming multiset, so duplicates and cross-column associations
// remain part of verification while PostgreSQL can rewrite physical CTIDs.
func postgresValueDigest(ctx context.Context, c *sql.Conn, o contract.ObjectRef, columns []string, catalog *schemaddl.PostgreSQLCatalog) (digest postgresDesignDigest, err error) {
	if catalog == nil || catalog.Namespace != o.Schema || catalog.Database != o.Catalog || len(columns) > 1600 {
		return digest, schemaddl.ErrUnsupported
	}
	table, e := catalog.Table()
	if e != nil || table.Name != o.Name || schemaddl.PostgreSQLIdentifier(o.Name) != nil {
		return digest, errors.Join(schemaddl.ErrUnsupported, e)
	}
	types, err := postgresDesignTypes(catalog)
	if err != nil {
		return digest, err
	}
	var quoted []string
	for _, name := range columns {
		if schemaddl.PostgreSQLIdentifier(name) != nil {
			return digest, schemaddl.ErrUnsupported
		}
		quoted = append(quoted, quote(name))
	}
	query := "SELECT CASE WHEN pg_catalog.octet_length(v.payload)<=" + strconv.Itoa(postgresDesignRowLimit) + " THEN v.payload ELSE NULL END FROM (SELECT pg_catalog.record_send(ROW(" + strings.Join(quoted, ",") + ")) AS payload FROM ONLY " + qualified(o) + ") v"
	rows, err := c.QueryContext(ctx, query)
	if err != nil {
		return digest, err
	}
	defer rows.Close()
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			return postgresDesignDigest{}, err
		}
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return postgresDesignDigest{}, err
		}
		if raw == nil || len(raw) > postgresDesignRowLimit {
			return postgresDesignDigest{}, errors.New("PostgreSQL verification row exceeds 16 MiB")
		}
		canonical, e := postgresCanonicalRecord(raw, types, 0, len(columns))
		if e != nil {
			return postgresDesignDigest{}, e
		}
		sum := sha256.Sum256(canonical)
		carry := 0
		for i := len(digest.Sum) - 1; i >= 0; i-- {
			n := int(digest.Sum[i]) + int(sum[i]) + carry
			digest.Sum[i], carry = byte(n), n>>8
		}
		digest.Rows++
	}
	if err = rows.Err(); err != nil {
		return postgresDesignDigest{}, err
	}
	return digest, nil
}

func postgresCanonicalRecord(raw []byte, types map[uint32]postgresDesignType, depth, expected int) ([]byte, error) {
	if depth > 32 || len(raw) < 4 || len(raw) > postgresDesignRowLimit {
		return nil, errors.New("invalid PostgreSQL binary record")
	}
	count := int(binary.BigEndian.Uint32(raw))
	if count != expected || count > 1600 {
		return nil, errors.New("PostgreSQL verification record field count changed")
	}
	at := 4
	var out bytes.Buffer
	for i := 0; i < count; i++ {
		if len(raw)-at < 8 {
			return nil, errors.New("truncated PostgreSQL record header")
		}
		oid := binary.BigEndian.Uint32(raw[at:])
		length := int64(int32(binary.BigEndian.Uint32(raw[at+4:])))
		at += 8
		if length == -1 {
			if _, _, err := postgresResolveType(oid, types); err != nil {
				return nil, err
			}
			writeDigestBytes(&out, []byte("null"))
			continue
		}
		if length < 0 || length > postgresDesignCellLimit || length > int64(len(raw)-at) {
			return nil, errors.New("invalid or oversized PostgreSQL binary cell")
		}
		value, err := postgresCanonicalValue(oid, raw[at:at+int(length)], types, depth+1)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(value)
		writeDigestBytes(&out, sum[:])
		at += int(length)
	}
	if at != len(raw) {
		return nil, errors.New("extra bytes in PostgreSQL verification record")
	}
	return out.Bytes(), nil
}

func postgresCanonicalValue(oid uint32, raw []byte, types map[uint32]postgresDesignType, depth int) ([]byte, error) {
	if depth > 32 || len(raw) > postgresDesignCellLimit {
		return nil, errors.New("PostgreSQL value nesting/size limit exceeded")
	}
	base, t, err := postgresResolveType(oid, types)
	if err != nil {
		return nil, err
	}
	if t.element != 0 {
		return postgresCanonicalArray(raw, t.element, types, depth+1)
	}
	var out bytes.Buffer
	var tag string
	value := raw
	switch base {
	case 20, 21, 23: // int8/int2/int4
		var number int64
		switch {
		case base == 20 && len(raw) == 8:
			number = int64(binary.BigEndian.Uint64(raw))
		case base == 21 && len(raw) == 2:
			number = int64(int16(binary.BigEndian.Uint16(raw)))
		case base == 23 && len(raw) == 4:
			number = int64(int32(binary.BigEndian.Uint32(raw)))
		default:
			return nil, errors.New("invalid PostgreSQL integer payload")
		}
		tag = "number"
		value = []byte(strconv.FormatInt(number, 10))
	case 1700:
		tag = "number"
		number, e := postgresCanonicalNumeric(raw)
		if e != nil {
			return nil, e
		}
		value = []byte(number)
	case 700, 701:
		var number float64
		if base == 700 && len(raw) == 4 {
			number = float64(math.Float32frombits(binary.BigEndian.Uint32(raw)))
		} else if base == 701 && len(raw) == 8 {
			number = math.Float64frombits(binary.BigEndian.Uint64(raw))
		} else {
			return nil, errors.New("invalid PostgreSQL float payload")
		}
		tag = "number"
		switch {
		case math.IsNaN(number):
			tag = fmt.Sprintf("float-nan:%d", base)
		case number == 0 && math.Signbit(number):
			tag = "float-negative-zero"
			value = nil
		case math.IsInf(number, 1):
			value = []byte("+infinity")
		case math.IsInf(number, -1):
			value = []byte("-infinity")
		default:
			value = []byte(new(big.Rat).SetFloat64(number).RatString())
		}
	case 18, 19, 25, 1042, 1043:
		if base == 18 && len(raw) != 1 || base == 19 && len(raw) > 63 {
			return nil, errors.New("invalid PostgreSQL character payload")
		}
		tag = "text" // char/name/text/bpchar/varchar
	case 17:
		tag = "binary"
	case 16:
		if len(raw) != 1 || raw[0] > 1 {
			return nil, errors.New("invalid PostgreSQL boolean payload")
		}
		tag = "boolean"
	default:
		fixed := map[uint32]int{26: 4, 1082: 4, 1083: 8, 1114: 8, 1184: 8, 1266: 12, 1186: 16, 2950: 16}
		if size, ok := fixed[base]; ok && len(raw) != size {
			return nil, errors.New("invalid PostgreSQL fixed-width value payload")
		}
		// Remaining types retain their native binary representation and base
		// identity, including timestamps, UUIDs, JSON, enums and composites.
		tag = fmt.Sprintf("type:%d", base)
	}
	writeDigestBytes(&out, []byte(tag))
	writeDigestBytes(&out, value)
	return out.Bytes(), nil
}

// PostgreSQL numeric uses exact base-10000 digits; no float64 round trip or
// display-scale rounding participates in conversion verification.
func postgresCanonicalNumeric(raw []byte) (string, error) {
	if len(raw) < 8 {
		return "", errors.New("truncated PostgreSQL numeric")
	}
	digits := int(int16(binary.BigEndian.Uint16(raw)))
	weight := int(int16(binary.BigEndian.Uint16(raw[2:])))
	sign := binary.BigEndian.Uint16(raw[4:])
	scale := binary.BigEndian.Uint16(raw[6:])
	if digits < 0 || len(raw) != 8+digits*2 || scale > 16383 {
		return "", errors.New("invalid PostgreSQL numeric header")
	}
	if sign == 0xc000 || sign == 0xd000 || sign == 0xf000 {
		if digits != 0 {
			return "", errors.New("invalid PostgreSQL non-finite numeric")
		}
		return map[uint16]string{0xc000: "numeric-nan", 0xd000: "+infinity", 0xf000: "-infinity"}[sign], nil
	}
	if sign != 0 && sign != 0x4000 {
		return "", errors.New("unknown PostgreSQL numeric sign")
	}
	var decimal strings.Builder
	for i := 0; i < digits; i++ {
		digit := binary.BigEndian.Uint16(raw[8+i*2:])
		if digit >= 10000 {
			return "", errors.New("invalid PostgreSQL numeric digit")
		}
		fmt.Fprintf(&decimal, "%04d", digit)
	}
	coefficient := strings.TrimLeft(decimal.String(), "0")
	if coefficient == "" {
		return "0", nil
	}
	exponent := 4 * (weight - digits + 1)
	trimmed := strings.TrimRight(coefficient, "0")
	exponent += len(coefficient) - len(trimmed)
	coefficient = trimmed
	if -exponent > int(scale) || len(coefficient)+exponent > 131072 {
		return "", errors.New("PostgreSQL numeric exceeds declared scale or value limits")
	}
	number, ok := new(big.Int).SetString(coefficient, 10)
	if !ok {
		return "", errors.New("invalid PostgreSQL numeric coefficient")
	}
	if sign == 0x4000 {
		number.Neg(number)
	}
	power := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(exponent))), nil)
	rational := new(big.Rat)
	if exponent >= 0 {
		rational.SetInt(number.Mul(number, power))
	} else {
		rational.SetFrac(number, power)
	}
	return rational.RatString(), nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func postgresCanonicalArray(raw []byte, element uint32, types map[uint32]postgresDesignType, depth int) ([]byte, error) {
	if len(raw) < 12 || depth > 32 {
		return nil, errors.New("invalid PostgreSQL binary array")
	}
	dimensions := int(int32(binary.BigEndian.Uint32(raw)))
	nullFlag := binary.BigEndian.Uint32(raw[4:])
	wireElement := binary.BigEndian.Uint32(raw[8:])
	if dimensions < 0 || dimensions > 6 || nullFlag > 1 || wireElement != element || len(raw) < 12+dimensions*8 {
		return nil, errors.New("invalid PostgreSQL array header")
	}
	at := 12
	count := 1
	var out bytes.Buffer
	writeDigestBytes(&out, []byte("array"))
	writeDigestBytes(&out, raw[12:12+dimensions*8])
	if dimensions == 0 {
		count = 0
	}
	for i := 0; i < dimensions; i++ {
		size := int(int32(binary.BigEndian.Uint32(raw[at:])))
		at += 8
		if size < 0 || size > postgresDesignCellLimit || size != 0 && count > postgresDesignCellLimit/size {
			return nil, errors.New("PostgreSQL array element count exceeded")
		}
		count *= size
	}
	for i := 0; i < count; i++ {
		if len(raw)-at < 4 {
			return nil, errors.New("truncated PostgreSQL array element")
		}
		length := int64(int32(binary.BigEndian.Uint32(raw[at:])))
		at += 4
		if length == -1 {
			if nullFlag == 0 {
				return nil, errors.New("PostgreSQL array null flag missing")
			}
			writeDigestBytes(&out, []byte("null"))
			continue
		}
		if length < 0 || length > postgresDesignCellLimit || length > int64(len(raw)-at) {
			return nil, errors.New("invalid PostgreSQL array element length")
		}
		value, err := postgresCanonicalValue(element, raw[at:at+int(length)], types, depth+1)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(value)
		writeDigestBytes(&out, sum[:])
		at += int(length)
	}
	if at != len(raw) {
		return nil, errors.New("extra bytes in PostgreSQL verification array")
	}
	return out.Bytes(), nil
}

func postgresRetainedValues(original, desired schemaddl.Table) (before, after []string, err error) {
	byID := map[string]schemaddl.Clause{}
	for _, c := range desired.Columns() {
		byID[c.ID] = c
	}
	for _, old := range original.Columns() {
		next, ok := byID[old.ID]
		if !ok {
			continue
		}
		a, e := schemaddl.ParsePostgreSQLField(old.SQL)
		if e != nil {
			return nil, nil, e
		}
		b, e := schemaddl.ParsePostgreSQLField(next.SQL)
		if e != nil {
			return nil, nil, e
		}
		// A deliberately replaced generated expression computes new values.
		// Retained ordinary values and generated->ordinary values must match.
		if a.Generation != "" && b.Generation != "" && a.Generation != b.Generation {
			continue
		}
		before = append(before, old.Name)
		after = append(after, next.Name)
	}
	return before, after, nil
}
