package tdengine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func writeTestMetadata(l *dataLease) *dataMetadata {
	return &dataMetadata{Columns: []contract.StructureColumn{
		{Name: "ts", DataType: "TIMESTAMP", ValueKind: contract.DateTime},
		{Name: "v", DataType: "INT", ValueKind: contract.Integer, Nullable: true},
		{Name: "txt", DataType: "VARCHAR", ValueKind: contract.Text, Nullable: true},
	}, Keys: []int{0}, Precision: "ns", Signature: "original", Epoch: 1,
		Permissions: map[string][]string{"ts": {"insert"}, "v": {"insert", "update"}, "txt": {"insert", "update"}}, Children: map[string]contract.ObjectRef{}}
}

func TestDataWriteStatementBindsOriginalGrantAndNativeMerge(t *testing.T) {
	s := localDataSession(t)
	l := nativeDataDocument(t, s, s.Namespace("original").Object("table", "items"))
	m := writeTestMetadata(l)
	original, err := l.snapshot(m, []contract.Cell{{Kind: contract.DateTime, Text: "2024-01-01T00:00:00.123456789Z"}, {Kind: contract.Integer, Text: "42"}, {Kind: contract.Text, Text: "original"}})
	if err != nil {
		t.Fatal(err)
	}
	update := contract.DataEdit{Kind: "update", Original: original, Values: map[string]contract.DataValue{"v": {Kind: contract.Null}}}
	statement, err := l.prepareDataStatement(m, update)
	if err != nil || statement.SQL != "INSERT INTO `original`.`items`(`ts`,`v`) VALUES(1704067200123456789,NULL)" || statement.Destination != l.object {
		t.Fatal("original merge changed", statement, err)
	}
	for _, edit := range []contract.DataEdit{
		{Kind: "update", Original: original, Values: map[string]contract.DataValue{"ts": {Kind: contract.DateTime, Text: "2024-01-01T00:00:01Z"}}},
		{Kind: "update", Original: original, Values: map[string]contract.DataValue{"foreign": {Kind: contract.Text, Text: "x"}}},
		{Kind: "insert", Values: map[string]contract.DataValue{"v": {Kind: contract.Integer, Text: "1"}}},
		{Kind: "copy", Original: original},
	} {
		if _, err := l.prepareDataStatement(m, edit); err == nil {
			t.Fatal("unsupported mutation admitted", edit)
		}
	}
	other := nativeDataDocument(t, s, l.object)
	if _, err := other.prepareDataStatement(m, update); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("grant crossed document", err)
	}
	revoked := *m
	revoked.Epoch++
	if _, err := l.prepareDataStatement(&revoked, update); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("revoked metadata admitted", err)
	}
	large, err := l.snapshot(m, []contract.Cell{original.Original[0], original.Original[1], {Kind: contract.Text, Text: strings.Repeat("x", contract.DataEditMaxBytes+1)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.prepareDataStatement(m, contract.DataEdit{Kind: "update", Original: large, Values: update.Values}); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("fingerprints alone opened complete editing", err)
	}
	if statement, err := l.prepareDataStatement(m, contract.DataEdit{Kind: "delete", Original: large}); err != nil || statement.SQL != "DELETE FROM `original`.`items` WHERE `ts`=1704067200123456789" {
		t.Fatal("fingerprint-only deletion lost original key", statement, err)
	}
}

func TestDataWritePlanSnapshotCloseAndForeignSubmission(t *testing.T) {
	s := localDataSession(t)
	l := nativeDataDocument(t, s, s.Namespace("original").Object("table", "items"))
	other := nativeDataDocument(t, s, l.object)
	p := &dataWritePlan{owner: l, snapshot: contract.DataMutationSnapshot{Object: l.object, Edits: []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"bin": {Kind: contract.Binary, Bytes: []byte{0, 255}}}}}, NativeJSON: `{"original":true}`}}
	l.writePlan = p
	before := p.Snapshot()
	copy := p.Snapshot()
	copy.Edits[0].Values["bin"].Bytes[0] = 1
	if !reflect.DeepEqual(before, p.Snapshot()) {
		t.Fatal("preview followed caller mutation")
	}
	if _, err := other.submitEdits(context.Background(), s.ProfileID(), "original", "items", p); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign plan admitted", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if l.writePlan != nil || !reflect.DeepEqual(p.Snapshot(), contract.DataMutationSnapshot{}) {
		t.Fatal("closed preview retained payload or slot")
	}
	if _, err := l.submitEdits(context.Background(), s.ProfileID(), "original", "items", p); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("closed plan admitted", err)
	}
	if _, err := l.previewEdits(nil, s.ProfileID(), "original", "items", []contract.DataEdit{{Kind: "insert"}}); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("nil context admitted", err)
	}
	l.session.profile.TDengineOptions.ReadOnly = true
	if _, err := l.previewEdits(context.Background(), s.ProfileID(), "original", "items", []contract.DataEdit{{Kind: "insert"}}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("readonly admitted writes", err)
	}
}

func TestDataWriteAcknowledgementsRequireCompleteOriginalResponse(t *testing.T) {
	for _, raw := range []string{
		`{"req_id":7,"action":"binary_query","is_update":true,"affected_rows":1}`,
		`{"req_id":7,"action":"binary_query","code":null,"is_update":true,"affected_rows":1}`,
		`{"req_id":7,"action":"binary_query","code":0,"is_update":true}`,
		`{"req_id":7,"action":"binary_query","code":0,"is_update":true,"affected_rows":null}`,
		`{"req_id":7,"action":"binary_query","code":0,"is_update":true,"affected_rows":-1}`,
		`{"req_id":8,"action":"binary_query","code":0,"is_update":true,"affected_rows":1}`,
		`{"req_id":7,"action":"foreign","code":1}`,
		`{"req_id":7,"action":"binary_query","code":0,"is_update":false,"affected_rows":1}`,
		`invalid`,
	} {
		var response nativeResponse
		if nativeDataResponse([]byte(raw), 7, &response) {
			t.Fatal("malformed original response accepted", raw)
		}
	}
	for _, raw := range []string{`{"req_id":7,"action":"binary_query","code":0,"is_update":true,"affected_rows":0}`, `{"req_id":7,"action":"binary_query","code":123,"message":"original native failure"}`} {
		var response nativeResponse
		if !nativeDataResponse([]byte(raw), 7, &response) {
			t.Fatal("original response lost", raw)
		}
	}
}
