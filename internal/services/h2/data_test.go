package h2

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func testStructure() *contract.TableStructure {
	return &contract.TableStructure{Object: contract.ObjectRef{ProfileID: 17, Catalog: "OWNED", Schema: "PUBLIC", Kind: "table", Name: "records\";--\n"}, Name: "records\";--\n", Columns: []contract.StructureColumn{{Name: "id", Type: "BIGINT", DataType: "BIGINT", ValueKind: contract.Integer}, {Name: "note", Type: "VARCHAR", DataType: "VARCHAR", ValueKind: contract.Text, Nullable: true}}, Indexes: []contract.StructureIndex{{Type: "PRIMARY KEY", Unique: true, Columns: []contract.StructureIndexColumn{{Name: "id", Position: 1}}}}}
}

func TestH2CommitReceiptWinsCancellationOnlyAfterOriginalAcknowledgement(t *testing.T) {
	ack := agentbridge.Receipt{Result: json.RawMessage(`{"ok":true}`), Dispatched: true, ExecutorAcknowledged: true, CancellationRequested: true}
	if err := commitAcknowledged(ack, context.Canceled); err != nil {
		t.Fatal("original confirmed commit became unknown after cancellation", err)
	}
	for _, r := range []agentbridge.Receipt{
		{Result: ack.Result, ExecutorAcknowledged: true},
		{Result: ack.Result, Dispatched: true, CancellationRequested: true},
		{Dispatched: true, CancellationRequested: true, ProcessExitConfirmed: true},
		{Dispatched: true, ExecutorAcknowledged: true, Result: json.RawMessage(`{"ok":false}`)},
		{Dispatched: true, ExecutorAcknowledged: true, Result: ack.Result, RemoteError: &agentbridge.RemoteError{Category: "connection"}},
	} {
		if err := commitAcknowledged(r, context.Canceled); !errors.Is(err, contract.ErrDataCommitUncertain) || !errors.Is(err, context.Canceled) {
			t.Fatal("missing/failed original commit receipt granted replay authority", err)
		}
	}
}

func TestH2GeneratedStatementsKeepIdentifierAndTypedValueBoundaries(t *testing.T) {
	table := testStructure()
	s := &Session{scope: "original"}
	statement, err := s.editStatement(table, contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"id": {Text: "9007199254740993"}, "note": {Text: "literal '); DROP TABLE unsafe; --"}}})
	parts, splitErr := sqltext.SplitPortable(statement)
	if err != nil || splitErr != nil || len(parts) != 1 || !strings.Contains(statement, "9007199254740993") || !strings.Contains(statement, "literal ''); DROP TABLE unsafe; --") {
		t.Fatal("generated SQL escaped original literal/identifier boundaries", err, splitErr)
	}
	o := contract.DataOptions{Limit: 1, FilterColumn: "note", FilterMode: "contains", FilterValue: "literal ' %_~; --"}
	actual, err := dataQuery(table, o, false)
	parts, splitErr = sqltext.SplitPortable(actual)
	masked, maskErr := dataQuery(table, o, true)
	if err != nil || splitErr != nil || maskErr != nil || len(parts) != 1 || strings.Contains(masked, "literal") || !strings.Contains(masked, "LIKE ?") || !strings.Contains(actual, "~%~_~~") {
		t.Fatal("filter SQL escaped input boundaries or exposed values", err)
	}
}

func TestH2TypedValuesRejectLossyNumericConversions(t *testing.T) {
	precision, scale := int64(38), int64(20)
	c := contract.StructureColumn{DataType: "DECIMAL", ValueKind: contract.Decimal, NumericPrecision: &precision, NumericScale: &scale}
	if _, err := valueLiteral(c, contract.DataValue{Text: "123456789012345678.12345678901234567890"}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"1234567890123456789.0", "0.000000000000000000001", "1/2", "1;SELECT 1", "NaN"} {
		if _, err := valueLiteral(c, contract.DataValue{Text: value}); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("lossy/invalid decimal admitted", value, err)
		}
	}
	c = contract.StructureColumn{DataType: "BIGINT", ValueKind: contract.Integer}
	for _, value := range []string{"9223372036854775808", "-9223372036854775809", "1.0", "1;SELECT 1"} {
		if _, err := valueLiteral(c, contract.DataValue{Text: value}); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("invalid integer admitted", value, err)
		}
	}
}

func TestH2SnapshotsBindMetadataOriginalSessionAndCompleteKeys(t *testing.T) {
	table := testStructure()
	s := &Session{scope: "original"}
	cells := []contract.Cell{{Kind: contract.Integer, Text: "9007199254740993"}, {Kind: contract.Null}}
	hash := strings.Repeat("a", 64)
	snap := s.snapshot(table, cells, []*string{&hash, nil}, "fingerprints")
	if _, err := s.originalWhere(table, snap); err != nil {
		t.Fatal(err)
	}
	table.Definition = "native SCRIPT with a new identity restart position"
	if _, err := s.originalWhere(table, snap); err != nil {
		t.Fatal("moving native identity state invalidated stable schema", err)
	}
	other := &Session{scope: "different-original-session"}
	if _, err := other.originalWhere(table, snap); err == nil {
		t.Fatal("cross-session snapshot admitted")
	}
	table.Columns[1].Nullable = false
	if _, err := s.originalWhere(table, snap); err == nil {
		t.Fatal("changed original metadata admitted")
	}
	table.Columns[1].Nullable = true
	snap.Ref.Values[0] = []byte("rounded")
	if _, err := s.originalWhere(table, snap); err == nil {
		t.Fatal("typed/raw key disagreement admitted")
	}
	table.Columns[0].Nullable = true
	if keyColumns(table) != nil {
		t.Fatal("nullable unique key admitted")
	}
}
