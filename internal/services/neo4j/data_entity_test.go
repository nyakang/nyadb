package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNeo4jEntityPreparationCapturesOriginalMutationsAndRejectsForeignSnapshots(t *testing.T) {
	s := fakeSessionTest(t)
	l := dataLeaseTest(t, s, "label", "owned")
	entity := Value{Kind: "node", Node: &Node{ElementID: "original", Labels: []string{"owned"}, Properties: []Property{{Name: "n", Value: Value{Kind: "integer", Text: "1"}}}}}
	projection := []graphPropertyColumn{{Key: "n", Types: []string{"INTEGER NOT NULL"}}}
	snapshot := dataEditSnapshotTest(t, l, entity, projection)
	input := []contract.DataEdit{
		{Kind: "insert", Values: map[string]contract.DataValue{"property.binary": {Kind: contract.Binary, Binary: true, Bytes: []byte{0, 255}}, "property.missing": {Null: true}}},
		{Kind: "detach_delete", Original: snapshot},
	}
	p, err := l.prepareEntityEdits(input)
	if err != nil || len(p[0].identity.Properties) != 1 || p[0].params["missing"] != nil || l.validateLockedDataEdit(p[1], entity) != nil {
		t.Fatal("entity preparation did not preserve native kinds and complete original validation", err)
	}
	original, _ := json.Marshal([]contract.DataEdit{p[0].edit, p[1].edit})
	input[0].Values["property.binary"].Bytes[0] = 42
	input[0].Values["property.extra"] = contract.DataValue{Kind: contract.Text, Text: "new"}
	snapshot.Ref.Values[0][0] = '?'
	*snapshot.Fingerprints[0] = "forged"
	if got, _ := json.Marshal([]contract.DataEdit{p[0].edit, p[1].edit}); string(got) != string(original) || p[0].params["binary"].([]byte)[0] != 0 {
		t.Fatal("entity capture followed mutated request maps/bytes/original snapshot")
	}
	foreign := dataLeaseTest(t, s, "label", "owned")
	if _, err = foreign.prepareEntityEdits([]contract.DataEdit{p[1].edit}); err == nil {
		t.Fatal("entity deletion accepted another original document's signature")
	}
	if _, err = l.prepareEntityEdits([]contract.DataEdit{p[1].edit, p[1].edit}); err == nil {
		t.Fatal("same original entity could be deleted twice in one batch")
	}
	for _, invalid := range []contract.DataEdit{
		{Kind: "insert", Original: p[1].edit.Original},
		{Kind: "insert", Values: map[string]contract.DataValue{"labels": {Kind: contract.JSON, Text: `["another"]`}}},
		{Kind: "delete", Original: p[1].edit.Original, Values: map[string]contract.DataValue{"property.n": {Kind: contract.Integer, Text: "2"}}},
		{Kind: "delete"},
	} {
		if _, err = l.prepareEntityEdits([]contract.DataEdit{invalid}); err == nil {
			t.Fatal("native entity preparation admitted forged identity/operation payload")
		}
	}
	r := dataLeaseTest(t, s, "relationship", "owned")
	if _, err = r.prepareEntityEdits([]contract.DataEdit{{Kind: "insert"}}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("relationship creation guessed unqualified endpoints", err)
	}
	if _, err = l.prepareEntityEdits([]contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"property.large": {Kind: contract.Text, Text: strings.Repeat("x", contract.DataEditMaxBytes)}}}}); !errors.Is(err, ErrBounds) {
		t.Fatal("complete entity request bypassed capture bound", err)
	}
}

func TestNeo4jEntityPublicKindsRequireOriginalPreviewSubmission(t *testing.T) {
	s := fakeSessionTest(t)
	l := dataLeaseTest(t, s, "label", "owned")
	if !slices.Equal(l.Runtime().EditKinds, []string{"update", "insert", "delete", "detach_delete"}) {
		t.Fatal("public entity kinds not bound to the original confirmation flow")
	}
	for _, kind := range []string{"insert", "delete", "detach_delete"} {
		if _, err := l.Runtime().Saver(context.Background(), 42, "neo4j", "owned", []contract.DataEdit{{Kind: kind}}); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("public original saver admitted a private entity operation", err)
		}
	}
}

func TestNeo4jDeletedRelationshipEvidencePreservesDirectedIdentityAndBounds(t *testing.T) {
	edge := Value{Kind: "map", Properties: []Property{
		{Name: "element_id", Value: Value{Kind: "string", Text: "edge-1"}},
		{Name: "legacy_id", Value: Value{Kind: "integer", Text: "9223372036854775807"}},
		{Name: "type", Value: Value{Kind: "string", Text: "exact 猫`"}},
		{Name: "start_element_id", Value: Value{Kind: "string", Text: "original"}},
		{Name: "end_element_id", Value: Value{Kind: "string", Text: "other"}},
		{Name: "properties", Value: Value{Kind: "map", Properties: []Property{{Name: "n", Value: Value{Kind: "integer", Text: "-9223372036854775808"}}}}},
	}}
	value := Value{Kind: "list", Values: []Value{edge}}
	if err := validateDeletedRelationships("original", value); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []Value{
		{Kind: "map"}, {Kind: "list", Values: []Value{edge, edge}},
		{Kind: "list", Values: make([]Value, graphDetachMaxRelationships+1)},
	} {
		if validateDeletedRelationships("original", invalid) == nil {
			t.Fatal("malformed/duplicate/unbounded detached relationship receipt admitted")
		}
	}
	if validateDeletedRelationships("foreign", value) == nil || !reflect.DeepEqual(value.Values[0], edge) {
		t.Fatal("unrelated incident edge accepted or evidence mutated")
	}
}
