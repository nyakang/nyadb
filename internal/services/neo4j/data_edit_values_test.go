package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func dataEditSnapshotTest(t *testing.T, l *dataLease, entity Value, properties []graphPropertyColumn) *contract.RowSnapshot {
	t.Helper()
	cells, err := l.project(entity, properties)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := l.snapshot(entity, properties, cells)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestNeo4jDataEditTypedValuesRejectAmbiguousJSONAndPreserveNativeComponents(t *testing.T) {
	for _, v := range []contract.DataValue{
		{Kind: contract.Integer, Text: "9223372036854775807"},
		{Kind: contract.Floating, Text: "-0"},
		{Kind: contract.Boolean, Text: "true"},
		{Kind: contract.Text, Text: "exact 猫 ` value"},
		{Kind: contract.Binary, Binary: true, Bytes: []byte{0, 1, 255}},
		{Null: true},
		{Kind: contract.JSON, Text: `{"Kind":"duration","Text":"PT-0.000000001S","Properties":[{"Name":"months","Value":{"Kind":"integer","Text":"0"}},{"Name":"days","Value":{"Kind":"integer","Text":"0"}},{"Name":"seconds","Value":{"Kind":"integer","Text":"-1"}},{"Name":"nanoseconds","Value":{"Kind":"integer","Text":"999999999"}}]}`},
		{Kind: contract.JSON, Text: `{"Kind":"list","Values":[{"Kind":"integer","Text":"-9223372036854775808"},{"Kind":"integer","Text":"9223372036854775807"}]}`},
		{Kind: contract.JSON, Text: `{"Kind":"list","Values":[{"Kind":"date","Text":"2026-01-01"}]}`},
	} {
		value, parameter, err := dataPropertyValue(v)
		budget := valueBudget{limit: 1 << 20}
		captured, _, captureErr := budget.capture(parameter, true, 0)
		if err != nil || captureErr != nil || !reflect.DeepEqual(value, captured) {
			t.Fatal("grid input lost native kind/components", v.Kind, err, captureErr)
		}
		if v.Kind == contract.Floating && !math.Signbit(parameter.(float64)) {
			t.Fatal("grid float input lost negative zero")
		}
	}
	for _, v := range []contract.DataValue{
		{Kind: contract.JSON, Text: `[1,2]`}, {Kind: contract.JSON, Text: `{"Kind":"integer","kind":"string","Text":"1"}`},
		{Kind: contract.JSON, Text: `{"Kind":"list","Values":[{"Kind":"integer","text":"1"}]}`}, {Kind: contract.JSON, Text: `{"Kind":"integer","Text":"1","Kind":"string"}`},
		{Kind: contract.JSON, Text: `{"Kind":"integer","Text":"1","unknown":true}`},
		{Kind: contract.JSON, Text: `{"Kind":"integer","Text":"1"} {}`},
		{Kind: contract.JSON, Text: `{"Kind":"list","Values":[{"Kind":"date","Text":"2026-02-29"}]}`},
		{Kind: contract.Floating, Text: "NaN"}, {Kind: contract.Integer, Text: "9223372036854775808"},
		{Kind: contract.Text, Text: "bad", Null: true}, {Kind: contract.Text, Default: true},
		{Kind: contract.Text, Binary: true}, {Kind: contract.Text, Bytes: []byte{1}},
	} {
		if _, _, err := dataPropertyValue(v); err == nil {
			t.Fatal("ambiguous/unsupported grid input admitted", v.Kind, v.Text)
		}
	}
}

func TestNeo4jDataEditCapturesSignedIdentityFingerprintsAndOnlyPropertyUpdates(t *testing.T) {
	s := fakeSessionTest(t)
	l := dataLeaseTest(t, s, "label", "owned")
	entity := Value{Kind: "node", Node: &Node{ElementID: "original", Labels: []string{"owned"}, Properties: []Property{{Name: "key ` 猫", Value: Value{Kind: "integer", Text: "1"}}}}}
	projection := []graphPropertyColumn{{Key: "key ` 猫", Types: []string{"INTEGER NOT NULL"}}}
	snapshot := dataEditSnapshotTest(t, l, entity, projection)
	edit := contract.DataEdit{Kind: "update", Original: snapshot, Values: map[string]contract.DataValue{"property.key ` 猫": {Kind: contract.Integer, Text: "9223372036854775807"}}}
	prepared, err := l.prepareDataEdits([]contract.DataEdit{edit})
	if err != nil || l.validateLockedDataEdit(prepared[0], entity) != nil {
		t.Fatal("original signed row cannot produce a native property update", err)
	}
	original, _ := json.Marshal(prepared[0].edit)
	edit.Values["property.key ` 猫"] = contract.DataValue{Kind: contract.Integer, Text: "2"}
	edit.Original.Ref.Values[0][0] = '?'
	*edit.Original.Fingerprints[0] = "changed"
	after, _ := json.Marshal(prepared[0].edit)
	if string(original) != string(after) {
		t.Fatal("captured grid update aliases mutable draft/identity/fingerprints")
	}
	concurrent := entity.Clone()
	concurrent.Node.Properties[0].Value.Text = "3"
	if !errors.Is(l.validateLockedDataEdit(prepared[0], concurrent), contract.ErrDataConflict) {
		t.Fatal("full locked original fingerprint ignored a concurrent property")
	}
	for _, invalid := range []contract.DataEdit{
		{Kind: "insert", Values: prepared[0].edit.Values}, {Kind: "delete", Original: prepared[0].edit.Original},
		{Kind: "update", Original: prepared[0].edit.Original, Values: map[string]contract.DataValue{"element_id": {Kind: contract.Text, Text: "replacement"}}},
		{Kind: "update", Original: prepared[0].edit.Original, Values: map[string]contract.DataValue{"property.unknown": {Kind: contract.Text, Text: "new"}}},
	} {
		if _, err = l.prepareDataEdits([]contract.DataEdit{invalid}); err == nil {
			t.Fatal("identity/meta column, undiscovered property or whole entity mutation admitted")
		}
	}
	forged := prepared[0].edit
	forged.Original = cloneDataSnapshot(forged.Original)
	*forged.Original.Fingerprints[0] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	p, err := l.prepareDataEdits([]contract.DataEdit{forged})
	if err != nil || l.validateLockedDataEdit(p[0], entity) == nil {
		t.Fatal("tampered complete-row validation payload was ignored")
	}
	if _, err = l.prepareDataEdits([]contract.DataEdit{prepared[0].edit, prepared[0].edit}); err == nil {
		t.Fatal("same graph entity appeared twice in one atomic batch")
	}
}

func TestNeo4jDataEditQueuedWritesObservePoisonBeforeOpeningNativeClient(t *testing.T) {
	s := fakeSessionTest(t)
	l := dataLeaseTest(t, s, "label", "owned")
	entity := Value{Kind: "node", Node: &Node{ElementID: "original", Labels: []string{"owned"}, Properties: []Property{{Name: "n", Value: Value{Kind: "integer", Text: "1"}}}}}
	snapshot := dataEditSnapshotTest(t, l, entity, []graphPropertyColumn{{Key: "n", Types: []string{"INTEGER NOT NULL"}}})
	edits := []contract.DataEdit{{Kind: "update", Original: snapshot, Values: map[string]contract.DataValue{"property.n": {Kind: contract.Integer, Text: "2"}}}}
	s.openClient = func(context.Context, connections.Neo4jConfig, string) (*Client, error) {
		t.Error("queued poisoned writer opened a native connection")
		return nil, ErrInvalid
	}
	l.writeMu.Lock()
	done := make(chan error, 1)
	go func() { _, err := l.save(context.Background(), 42, "neo4j", "owned", edits); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	registered := false
	for time.Now().Before(deadline) {
		l.mu.Lock()
		registered = l.active == 1
		l.mu.Unlock()
		if registered {
			break
		}
		time.Sleep(time.Millisecond)
	}
	l.mu.Lock()
	l.writeLost = true
	l.mu.Unlock()
	l.writeMu.Unlock()
	if !registered {
		t.Fatal("queued write did not register original request")
	}
	if err := <-done; !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("queued write ignored lost commit", err)
	}
}
