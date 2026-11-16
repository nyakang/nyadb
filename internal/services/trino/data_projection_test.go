package trino

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDataProjectionPreservesNullEmptyBinaryAndTruncation(t *testing.T) {
	table := &contract.TableStructure{Columns: []contract.StructureColumn{
		{Name: "b", Type: "varbinary"}, {Name: "j", Type: "array(bigint)"}, {Name: "s", Type: "varchar"},
	}}
	p, err := newDataProjection(table, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	rows := [][]contract.Cell{
		{{Kind: contract.Text, Text: `["AP8=",true]`}, {Kind: contract.Text, Text: `["[9007199254740993]",false]`}, {Kind: contract.Text, Text: `["",false]`}},
		{{Kind: contract.Text, Text: `["",false]`}, {Kind: contract.Text, Text: `["[9007199254740993,",true]`}, {Kind: contract.Text, Text: `[null,false]`}},
	}
	_, first, err := p.decode(p.columns, rows[0])
	if err != nil || !bytes.Equal(first[0].Bytes, []byte{0, 255}) || !first[0].Truncated || first[1].Kind != contract.JSON || first[1].Text != "[9007199254740993]" || first[2].Kind != contract.Text || first[2].Truncated {
		t.Fatal("complete composite, bounded binary or empty text changed", first, err)
	}
	_, second, err := p.decode(p.columns, rows[1])
	if err != nil || second[0].Bytes == nil || second[0].Kind != contract.Binary || !second[1].Truncated || second[1].Kind != contract.JSON || second[2].Kind != contract.Null {
		t.Fatal("empty binary, incomplete JSON or NULL changed", second, err)
	}
	for _, text := range []string{`[null,true]`, `["AP8=",null]`, `["bad base64",false]`, `["AP8="]`, `{"value":"AP8="}`} {
		bad := append([]contract.Cell(nil), rows[0]...)
		bad[0].Text = text
		if _, _, err := p.decode(p.columns, bad); !errors.Is(err, ErrProtocol) {
			t.Fatal("malformed envelope admitted", text, err)
		}
	}
}

func TestDataProjectionPreservesQualifiedQueriesAndIncompleteUnknownTypes(t *testing.T) {
	table := &contract.TableStructure{Object: contract.ObjectRef{ProfileID: 1, Catalog: "a", Schema: "b", Name: "quoted\"table"}, Columns: []contract.StructureColumn{
		{Name: "__nyadb_projection_1", Type: "row(\"quoted field\" array(decimal(38,20)),p map(varbinary,row(bigint,time(12))))"},
		{Name: "id", Type: "bigint", ValueKind: contract.Integer}, {Name: "unknown", Type: "varchar); SELECT private"},
		{Name: "keys", Type: "map(row(bigint),varchar)"},
	}}
	p, err := newDataProjection(table, nil, false)
	if err != nil || p.types[0] == nil || p.types[2] != nil || p.types[3] != nil {
		t.Fatal("native grammar or explicit incomplete fallback changed", err)
	}
	q, args, err := dataQueryProjected(table, contract.DataOptions{Limit: 2, Offset: 3, FilterColumn: "id", FilterValue: "9007199254740993", SortColumn: "id", SortDescending: true}, nil, true, p)
	if err != nil || strings.Contains(q, "SELECT private") || strings.Contains(q, "9007199254740993") || strings.Contains(q, "__nyadb_projection_1 ->") || !strings.Contains(q, `FROM "a"."b"."quoted""table" AS "__nyadb_projection_source" WHERE "id" = ? ORDER BY "__nyadb_projection_source"."id" DESC OFFSET 3 LIMIT 3`) || len(args) != 1 {
		t.Fatal("qualified native query, bind, lambda scope or bounds changed", q, args, err)
	}
	row := []contract.Cell{{Kind: contract.Text, Text: `[["1.23000000000000000000"],{"AP8=":[9007199254740993,"01:02:03.123456789123"]}]`}, {Kind: contract.Text, Text: "9007199254740993"}, {Kind: contract.Text, Text: "original"}, {Kind: contract.Text, Text: "original map"}}
	cols, values, err := p.decode(p.columns, row)
	if err != nil || cols[0].DatabaseType != table.Columns[0].Type || values[0].Kind != contract.JSON || values[0].Truncated || !values[2].Truncated || values[2].Text != "original" || !values[3].Truncated {
		t.Fatal("original metadata or incomplete unknown value lost", values, err)
	}
	cols[0].Name = "changed"
	if p.columns[0].Name == "changed" {
		t.Fatal("projection metadata escaped by reference")
	}
	row[0].Text = "[incomplete"
	if _, _, err := p.decode(p.columns, row); !errors.Is(err, ErrProtocol) {
		t.Fatal("invalid complete composite was relabeled JSON")
	}
	if _, err := newDataProjection(table, []string{"missing"}, false); err == nil {
		t.Fatal("unknown field admitted")
	}
}

func TestDataProjectionScalarDecodingDoesNotCoerceExactValues(t *testing.T) {
	table := &contract.TableStructure{Columns: []contract.StructureColumn{
		{Name: "d", Type: "decimal(38,20)"}, {Name: "t", Type: "timestamp(12) with time zone"}, {Name: "f", Type: "double"}, {Name: "j", Type: "json"},
	}}
	p, err := newDataProjection(table, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"123456789012345678.12345678901234567890", "2026-11-16 01:02:03.123456789123 UTC", "Infinity", `{"n":9007199254740993}`}
	row := make([]contract.Cell, len(want))
	for i, text := range want {
		row[i] = contract.Cell{Kind: contract.Text, Text: text}
	}
	_, cells, err := p.decode(p.columns, row)
	if err != nil {
		t.Fatal(err)
	}
	for i, cell := range cells {
		if cell.Text != want[i] || cell.Truncated {
			t.Fatal("native scalar changed", cell)
		}
	}
	if !json.Valid([]byte(cells[3].Text)) || cells[1].Kind != contract.DateTime {
		t.Fatal("native scalar types lost")
	}
}
