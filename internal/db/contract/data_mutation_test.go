package contract

import (
	"encoding/json"
	"testing"
)

func TestDataMutationSnapshotCopiesAllOriginalEditAndIdentityEvidence(t *testing.T) {
	f := "original fingerprint"
	s := DataMutationSnapshot{Items: []DataMutationItem{{Kind: "detach_delete", AffectedRelationships: 2}}, Edits: []DataEdit{{Kind: "delete", Values: map[string]DataValue{"bytes": {Bytes: []byte{1, 2}}}, Original: &RowSnapshot{Ref: RowRef{Columns: []string{"id"}, Values: [][]byte{{1}}, TypedValues: []Cell{{Kind: Binary, Bytes: []byte{2}}}}, Columns: []string{"bytes"}, Original: []Cell{{Kind: Binary, Bytes: []byte{3}}}, Fingerprints: []*string{&f}}}}}
	copy := CloneDataMutationSnapshot(s)
	want, _ := json.Marshal(copy)
	s.Items[0].Kind = "insert"
	s.Edits[0].Kind = "insert"
	s.Edits[0].Values["bytes"].Bytes[0] = 9
	s.Edits[0].Original.Ref.Columns[0] = "other"
	s.Edits[0].Original.Ref.Values[0][0] = 9
	s.Edits[0].Original.Ref.TypedValues[0].Bytes[0] = 9
	s.Edits[0].Original.Columns[0] = "other"
	s.Edits[0].Original.Original[0].Bytes[0] = 9
	f = "changed fingerprint"
	got, _ := json.Marshal(copy)
	if string(got) != string(want) {
		t.Fatal("immutable preview followed mutable request values or original evidence")
	}
}

func TestDataMutationCopiesCompleteDirectedEndpointEvidenceIndependently(t *testing.T) {
	e := &DataGraphEndpoints{Object: ObjectRef{ProfileID: 42, Catalog: "original", Kind: "relationship", Name: "owns"},
		Source: GraphNode{ElementID: "source", Labels: []string{"original"}, Properties: []GraphProperty{{Name: "nested", Value: GraphValue{Kind: "list", Values: []GraphValue{{Kind: "integer", Text: "9223372036854775807"}}}}}},
		Target: GraphNode{ElementID: "target", Labels: []string{"target"}}, NativeJSON: "original", Token: "original"}
	s := CloneDataMutationSnapshot(DataMutationSnapshot{Edits: []DataEdit{{Kind: "insert", GraphEndpoints: e}}})
	want, _ := json.Marshal(s)
	e.Object.Catalog = "other"
	e.Source.Labels[0] = "other"
	e.Source.Properties[0].Value.Values[0].Text = "0"
	e.Target.Labels[0] = "other"
	e.Token = "other"
	got, _ := json.Marshal(s)
	if string(got) != string(want) {
		t.Fatal("relationship confirmation followed mutable endpoint content")
	}
	copy := CloneDataMutationSnapshot(s)
	copy.Edits[0].GraphEndpoints.Source.Labels[0] = "executor-only"
	if s.Edits[0].GraphEndpoints.Source.Labels[0] != "original" {
		t.Fatal("executor and UI shared endpoint evidence")
	}
}
