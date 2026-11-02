package engines

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

type pgWireCell struct {
	oid   uint32
	value []byte
	null  bool
}

func pgWireRecord(values ...pgWireCell) []byte {
	var out bytes.Buffer
	_ = binary.Write(&out, binary.BigEndian, uint32(len(values)))
	for _, v := range values {
		_ = binary.Write(&out, binary.BigEndian, v.oid)
		if v.null {
			_ = binary.Write(&out, binary.BigEndian, int32(-1))
		} else {
			_ = binary.Write(&out, binary.BigEndian, int32(len(v.value)))
			out.Write(v.value)
		}
	}
	return out.Bytes()
}
func pgWireNumber(weight int16, sign, scale uint16, digits ...uint16) []byte {
	var out bytes.Buffer
	for _, v := range []uint16{uint16(len(digits)), uint16(weight), sign, scale} {
		_ = binary.Write(&out, binary.BigEndian, v)
	}
	for _, v := range digits {
		_ = binary.Write(&out, binary.BigEndian, v)
	}
	return out.Bytes()
}
func pgWireInteger(value int64, size int) []byte {
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], uint64(value))
	return append([]byte(nil), out[8-size:]...)
}
func pgWireArray(element uint32, bound int32, values ...pgWireCell) []byte {
	var out bytes.Buffer
	nulls := uint32(0)
	for _, v := range values {
		if v.null {
			nulls = 1
		}
	}
	for _, v := range []uint32{1, nulls, element, uint32(len(values)), uint32(bound)} {
		_ = binary.Write(&out, binary.BigEndian, v)
	}
	for _, v := range values {
		if v.null {
			_ = binary.Write(&out, binary.BigEndian, int32(-1))
		} else {
			_ = binary.Write(&out, binary.BigEndian, int32(len(v.value)))
			out.Write(v.value)
		}
	}
	return out.Bytes()
}
func pgProofTypes() map[uint32]postgresDesignType {
	types := map[uint32]postgresDesignType{}
	for _, oid := range []uint32{16, 17, 20, 21, 23, 25, 1043, 1700, 700, 701, 1082, 2950} {
		types[oid] = postgresDesignType{}
	}
	types[16000] = postgresDesignType{base: 1700}
	types[1007] = postgresDesignType{element: 23}
	types[1016] = postgresDesignType{element: 20}
	return types
}
func pgProofCanonical(t *testing.T, values ...pgWireCell) []byte {
	t.Helper()
	raw, err := postgresCanonicalRecord(pgWireRecord(values...), pgProofTypes(), 0, len(values))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPostgreSQLBinaryProofPreservesExactNumericAndLosslessConversions(t *testing.T) {
	exact := pgProofCanonical(t, pgWireCell{oid: 20, value: pgWireInteger(9007199254740993, 8)})
	numeric := pgProofCanonical(t, pgWireCell{oid: 16000, value: pgWireNumber(3, 0, 0, 9007, 1992, 5474, 993)})
	if !bytes.Equal(exact, numeric) {
		t.Fatal("domain/numeric conversion rounded an exact large integer")
	}
	rounded := pgProofCanonical(t, pgWireCell{oid: 701, value: pgWireInteger(int64(math.Float64bits(float64(9007199254740993))), 8)})
	if bytes.Equal(exact, rounded) {
		t.Fatal("float rounding above 2^53 was not detected")
	}
	floatHalf := pgProofCanonical(t, pgWireCell{oid: 701, value: pgWireInteger(int64(math.Float64bits(0.5)), 8)})
	decimalHalf := pgProofCanonical(t, pgWireCell{oid: 1700, value: pgWireNumber(-1, 0, 1, 5000)})
	if !bytes.Equal(floatHalf, decimalHalf) {
		t.Fatal("exact rational conversion failed")
	}
	floatTenth := pgProofCanonical(t, pgWireCell{oid: 701, value: pgWireInteger(int64(math.Float64bits(0.1)), 8)})
	decimalTenth := pgProofCanonical(t, pgWireCell{oid: 1700, value: pgWireNumber(-1, 0, 1, 1000)})
	if bytes.Equal(floatTenth, decimalTenth) {
		t.Fatal("binary floating approximation was replaced by rounded decimal")
	}
	four := pgProofCanonical(t, pgWireCell{oid: 700, value: pgWireInteger(int64(math.Float32bits(float32(0.1))), 4)})
	eight := pgProofCanonical(t, pgWireCell{oid: 701, value: pgWireInteger(int64(math.Float64bits(float64(float32(0.1)))), 8)})
	if !bytes.Equal(four, eight) {
		t.Fatal("float4 widening changed its exact stored value")
	}
}

func TestPostgreSQLBinaryProofKeepsNullBytesSignsAndColumnAssociations(t *testing.T) {
	null := pgProofCanonical(t, pgWireCell{oid: 25, null: true})
	empty := pgProofCanonical(t, pgWireCell{oid: 25, value: []byte{}})
	binaryEmpty := pgProofCanonical(t, pgWireCell{oid: 17, value: []byte{}})
	if bytes.Equal(null, empty) || bytes.Equal(empty, binaryEmpty) {
		t.Fatal("NULL, text and binary representations collapsed")
	}
	if bytes.Equal(pgProofCanonical(t, pgWireCell{oid: 25, value: []byte("01")}), pgProofCanonical(t, pgWireCell{oid: 23, value: pgWireInteger(1, 4)})) {
		t.Fatal("text storage converted to integer without detection")
	}
	negativeZero := pgProofCanonical(t, pgWireCell{oid: 701, value: pgWireInteger(math.MinInt64, 8)})
	positiveZero := pgProofCanonical(t, pgWireCell{oid: 701, value: pgWireInteger(0, 8)})
	if bytes.Equal(negativeZero, positiveZero) {
		t.Fatal("floating negative zero was lost")
	}
	a := pgProofCanonical(t, pgWireCell{oid: 25, value: []byte("a")}, pgWireCell{oid: 25, value: []byte("b")})
	b := pgProofCanonical(t, pgWireCell{oid: 25, value: []byte("b")}, pgWireCell{oid: 25, value: []byte("a")})
	if bytes.Equal(a, b) {
		t.Fatal("row association/column ordering disappeared")
	}
}

func TestPostgreSQLBinaryProofKeepsArrayBoundsNullsAndExactElements(t *testing.T) {
	four := pgProofCanonical(t, pgWireCell{oid: 1007, value: pgWireArray(23, 2, pgWireCell{value: pgWireInteger(1, 4)}, pgWireCell{null: true})})
	eight := pgProofCanonical(t, pgWireCell{oid: 1016, value: pgWireArray(20, 2, pgWireCell{value: pgWireInteger(1, 8)}, pgWireCell{null: true})})
	if !bytes.Equal(four, eight) {
		t.Fatal("array element widening did not preserve exact values")
	}
	changedBound := pgProofCanonical(t, pgWireCell{oid: 1016, value: pgWireArray(20, 1, pgWireCell{value: pgWireInteger(1, 8)}, pgWireCell{null: true})})
	if bytes.Equal(four, changedBound) {
		t.Fatal("array lower bounds were lost")
	}
	changedNull := pgProofCanonical(t, pgWireCell{oid: 1016, value: pgWireArray(20, 2, pgWireCell{value: pgWireInteger(1, 8)}, pgWireCell{value: pgWireInteger(0, 8)})})
	if bytes.Equal(eight, changedNull) {
		t.Fatal("NULL array element became zero")
	}
}

func TestPostgreSQLBinaryProofRejectsMalformedAndOversizedPayloads(t *testing.T) {
	bad := []pgWireCell{
		{oid: 23, value: []byte{1}}, {oid: 16, value: []byte{2}}, {oid: 2950, value: []byte{1}},
		{oid: 1700, value: pgWireNumber(0, 0, 0, 10000)}, {oid: 1700, value: pgWireNumber(0, 0xffff, 0)},
		{oid: 1700, value: pgWireNumber(-32768, 0, 0, 1)}, {oid: 1700, value: pgWireNumber(0, 0, 16384, 1)},
		{oid: 1700, value: pgWireNumber(0, 0xc000, 0, 1)},
		{oid: 17, value: make([]byte, postgresDesignCellLimit+1)}, {oid: 99999, value: []byte{}},
		{oid: 1007, value: pgWireArray(20, 1, pgWireCell{value: pgWireInteger(1, 8)})},
	}
	for i, v := range bad {
		if _, err := postgresCanonicalRecord(pgWireRecord(v), pgProofTypes(), 0, 1); err == nil {
			t.Fatalf("malformed cell %d acquired verification", i)
		}
	}
	raw := pgWireRecord(pgWireCell{oid: 25, value: []byte("data")})
	for _, v := range [][]byte{raw[:len(raw)-1], append(append([]byte{}, raw...), 0)} {
		if _, err := postgresCanonicalRecord(v, pgProofTypes(), 0, 1); err == nil {
			t.Fatal("partial record acquired verification")
		}
	}
	types := pgProofTypes()
	types[16000] = postgresDesignType{base: 16001}
	types[16001] = postgresDesignType{base: 16000}
	if _, err := postgresCanonicalRecord(pgWireRecord(pgWireCell{oid: 16000, null: true}), types, 0, 1); err == nil {
		t.Fatal("cyclic domain metadata accepted")
	}
}
