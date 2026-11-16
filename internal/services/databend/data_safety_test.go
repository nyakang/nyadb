package databend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDataLiteralPreservesExactStructuredValuesAndRejectsInvalidJSON(t *testing.T) {
	for _, test := range []struct {
		typ      string
		value    contract.DataValue
		contains string
	}{
		{"UInt64", contract.DataValue{Kind: contract.Integer, Text: "18446744073709551615"}, "18446744073709551615"},
		{"Decimal(38,20)", contract.DataValue{Kind: contract.Decimal, Text: "9007199254740993.12345678901234567890"}, "9007199254740993.12345678901234567890"},
		{"Binary", contract.DataValue{Kind: contract.Binary, Bytes: []byte{0, 255, 128}, Binary: true}, "unhex('00ff80')"},
		{"Nullable(Array(UInt64))", contract.DataValue{Kind: contract.JSON, Text: "[18446744073709551615]"}, "AS NULLABLE(ARRAY(UINT64))"},
		{"Array(Nullable(Variant))", contract.DataValue{Kind: contract.JSON, Text: "[null,\"hi\"]"}, "parse_json('null')"},
		{"Nullable(Variant)", contract.DataValue{Kind: contract.JSON, Text: "null"}, "parse_json('null')"},
		{"Nullable(Variant)", contract.DataValue{Kind: contract.Null, Null: true}, "CAST(NULL AS NULLABLE(VARIANT))"},
	} {
		typeInfo, err := parseDataType(test.typ)
		if err != nil {
			t.Fatal(err)
		}
		literal, err := dataLiteral(typeInfo, test.value)
		if err != nil || !strings.Contains(literal, test.contains) {
			t.Fatalf("%s: %s %v", test.typ, literal, err)
		}
	}
	for _, input := range []string{`{"a":1,"a":2}`, `[] x`, `[] !`, `null 1`, `[1,]`, `{"x":1} {`} {
		if _, err := strictDataJSON(input); err == nil {
			t.Fatal("accepted invalid/ambiguous JSON", input)
		}
	}
	for _, test := range []struct{ typ, text string }{{"UInt64", "18446744073709551616"}, {"Int8", "128"}, {"Decimal(5,2)", "1000.00"}, {"Decimal(5,2)", "1.001"}, {"Float64", "Inf"}, {"Timestamp", "2026-01-01 01:01:01.1234567"}, {"Array(UInt64)", "[null]"}} {
		typeInfo, err := parseDataType(test.typ)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = dataLiteral(typeInfo, contract.DataValue{Kind: typeInfo.kind, Text: test.text}); err == nil {
			t.Fatal("accepted out-of-domain native value", test)
		}
	}
}

func projectionRow(p *dataProjection, value string, total int64) ([]contract.Column, []contract.Cell) {
	h := sha256.Sum256([]byte(value))
	return []contract.Column{{Name: "__nyadb_value_0"}, {Name: "__nyadb_units_0"}, {Name: "__nyadb_digest_0"}}, []contract.Cell{{Kind: contract.Text, Text: value}, {Kind: contract.Integer, Text: fmtInt(total)}, {Kind: contract.Text, Text: hex.EncodeToString(h[:])}}
}

func TestFieldSlicesKeepNativeJSONTextAndBounds(t *testing.T) {
	m := &contract.TableStructure{Columns: []contract.StructureColumn{{Name: "j", Type: "Variant", ValueKind: contract.JSON}}}
	p, err := newDataProjection(m, nil, 1024, 4, false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	p.rawSlice = true
	columns, row := projectionRow(p, "\n  2\n]", 10)
	cells, _, _, _, err := p.decode(columns, row)
	if err != nil || cells[0].Text != "\n  2\n]" || cells[0].Kind != contract.Text {
		t.Fatal("JSON suffix was parsed or compacted", err, cells)
	}
	if _, err := newDataProjection(m, nil, 1024, math.MaxInt64, false, false, ""); err == nil {
		t.Fatal("offset overflow accepted")
	}
	p.rawSlice = false
	if _, _, _, _, err := p.decode(columns, row); err == nil {
		t.Fatal("whole JSON projection accepted a suffix")
	}
}

func fmtInt(n int64) string { return strconv.FormatInt(n, 10) }

func safetyLease(t *testing.T) (*dataLease, *contract.TableStructure) {
	t.Helper()
	o := contract.ObjectRef{ProfileID: 1, Schema: "original", Kind: "table", Name: "quoted\"table"}
	l := &dataLease{object: o, done: make(chan struct{})}
	l.ctx, l.cancel = context.WithCancel(context.Background())
	l.key[0] = 42
	m := &contract.TableStructure{Object: o, Definition: "CREATE TABLE t ENGINE=FUSE", Columns: []contract.StructureColumn{{Name: "b", Type: "Binary", ValueKind: contract.Binary}}}
	return l, m
}

func TestSignedOriginalRowsRejectForeignTamperedValuesAndMetadata(t *testing.T) {
	l, m := safetyLease(t)
	cells := []contract.Cell{{Kind: contract.Binary, Bytes: []byte{0, 255, 128}}}
	h := sha256.Sum256([]byte("00ff80"))
	digest := hex.EncodeToString(h[:])
	s, err := l.snapshot(m, cells, []*string{&digest}, "18446744073709551615")
	if err != nil {
		t.Fatal(err)
	}
	s.Original[0].Bytes[0] = 9
	if cells[0].Bytes[0] != 0 {
		t.Fatal("snapshot aliased native bytes")
	}
	g, err := l.grant(s.Ref)
	if err != nil {
		t.Fatal(err)
	}
	foreign, _ := safetyLease(t)
	foreign.key[0] = 43
	if _, err := foreign.grant(s.Ref); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("foreign document grant admitted", err)
	}
	ref := cloneDataRef(s.Ref)
	ref.Values[0][0] = '9'
	if _, err := l.grant(ref); err == nil {
		t.Fatal("tampered original row admitted")
	}
	m.Definition += " COMMENT 'new'"
	if err := validateDataGrant(g, m, g.Digests, g.RowID); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("changed metadata admitted", err)
	}
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	if _, err := l.grant(s.Ref); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed grant survived", err)
	}
}

func TestOriginalWritePlanSnapshotsOwnMutableBytesAndCannotAuthorizeAnotherPlan(t *testing.T) {
	l, _ := safetyLease(t)
	p := &dataWritePlan{owner: l, snapshot: contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"b": {Kind: contract.Binary, Bytes: []byte{0, 255}}}}}})}
	l.plan = p
	s := p.Snapshot()
	v := s.Edits[0].Values["b"]
	v.Bytes[0] = 10
	if !bytes.Equal(p.Snapshot().Edits[0].Values["b"].Bytes, []byte{0, 255}) {
		t.Fatal("confirmation view changed original write")
	}
	if err := p.Close(); err != nil || l.plan != nil || len(p.Snapshot().Edits) != 0 {
		t.Fatal("unused plan retained authorization")
	}
}

func TestEditableEngineUsesNativeDDLClauseAndIgnoresQuotedNamesAndComments(t *testing.T) {
	for _, test := range []struct {
		ddl      string
		editable bool
	}{
		{`CREATE TABLE "ENGINE=FUSE" (v String DEFAULT 'ENGINE=FUSE') ENGINE=MEMORY`, false},
		{`CREATE TABLE t (v String) ENGINE=MEMORY /* ENGINE=FUSE */`, false},
		{`CREATE TABLE t (v String) ENGINE=FUSE`, true},
	} {
		m := &contract.TableStructure{Object: contract.ObjectRef{Kind: "table"}, Definition: test.ddl}
		if dataHasRowID(m) != test.editable {
			t.Fatal("quoted/comment engine changed capabilities", test)
		}
	}
}
