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
