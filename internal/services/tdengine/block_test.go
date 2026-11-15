package tdengine

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestBlockExactIntegersDecimalsAndTimestampPrecision(t *testing.T) {
	for _, test := range []struct {
		typ              byte
		value            uint64
		precision, scale int
		text             string
	}{
		{5, 9007199254740993, 0, 0, "9007199254740993"},
		{14, math.MaxUint64 - 1, 0, 0, "18446744073709551614"},
		{9, 1704067200123, 0, 0, "2024-01-01T00:00:00.123Z"},
		{9, 1704067200123456, 1, 0, "2024-01-01T00:00:00.123456Z"},
		{9, 1704067200123456789, 2, 0, "2024-01-01T00:00:00.123456789Z"},
		{9, math.MaxUint64, 2, 0, "1969-12-31T23:59:59.999999999Z"},
		{21, 12345, 0, 3, "12.345"},
		{21, math.MaxUint64 - 12344, 0, 3, "-12.345"},
	} {
		raw := make([]byte, 8)
		binary.LittleEndian.PutUint64(raw, test.value)
		cell, err := fixedCell(raw, test.typ, test.precision, test.scale)
		if err != nil || cell.Text != test.text {
			t.Fatalf("type %d precision %d: %+v %v", test.typ, test.precision, cell, err)
		}
	}
	decimal := make([]byte, 16)
	binary.LittleEndian.PutUint64(decimal, math.MaxUint64)
	for i := 8; i < 16; i++ {
		decimal[i] = 255
	}
	cell, err := fixedCell(decimal, 17, 0, 10)
	if err != nil || cell.Kind != contract.Decimal || cell.Text != "-0.0000000001" {
		t.Fatal(cell, err)
	}
}

func TestBlockOffsetsNullEmptyAndOwnedBinary(t *testing.T) {
	// One native VARBINARY column: empty, NULL, and a complete 00ff value.
	raw := make([]byte, 56)
	binary.LittleEndian.PutUint32(raw, 3)
	binary.LittleEndian.PutUint32(raw[4:], uint32(len(raw)))
	binary.LittleEndian.PutUint32(raw[8:], 3)
	binary.LittleEndian.PutUint32(raw[12:], 1)
	raw[28] = 16
	binary.LittleEndian.PutUint32(raw[33:], 7)
	binary.LittleEndian.PutUint32(raw[37:], 0)
	binary.LittleEndian.PutUint32(raw[41:], math.MaxUint32)
	binary.LittleEndian.PutUint32(raw[45:], 2)
	binary.LittleEndian.PutUint16(raw[51:], 2)
	raw[53], raw[54] = 0, 255
	rows, err := decodeBlock(raw, []byte{16}, 0)
	if err != nil || len(rows) != 3 || rows[0][0].Kind != contract.Binary || len(rows[0][0].Bytes) != 0 || rows[1][0].Kind != contract.Null || len(rows[2][0].Bytes) != 2 || rows[2][0].Bytes[1] != 255 {
		t.Fatal(rows, err)
	}
	raw[54] = 3
	if rows[2][0].Bytes[1] != 255 {
		t.Fatal("decoded bytes alias the original frame")
	}
	for n := 0; n < 55; n++ {
		if _, err := decodeBlock(raw[:n], []byte{16}, 0); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("short frame admitted", n, err)
		}
	}
	for _, offset := range []uint32{math.MaxUint32 - 1, 7, 1000} {
		binary.LittleEndian.PutUint32(raw[37:], offset)
		if _, err := decodeBlock(raw, []byte{16}, 0); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("invalid native offset admitted", offset, err)
		}
	}
}

func TestReadOnlyRejectsWritesAcrossCommentsAndLiterals(t *testing.T) {
	for _, q := range []string{"SELECT 'DROP TABLE t'", "SHOW CREATE TABLE t", "/* INSERT */ SELECT 1", "WITH t AS (SELECT 1) SELECT * FROM t"} {
		if !readonly(q) {
			t.Fatal("read rejected", q)
		}
	}
	for _, q := range []string{"INSERT INTO t VALUES(1)", "WITH t AS (DELETE FROM x) SELECT * FROM t", "SELECT * INTO t FROM x", "ALTER TABLE t ADD v INT", "USE other"} {
		if readonly(q) {
			t.Fatal("write admitted", q)
		}
	}
}
