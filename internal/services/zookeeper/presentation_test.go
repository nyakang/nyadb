package zookeeper

import (
	"encoding/json"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestConfiguredPrefixAuthorsValidNativeRequestsWithoutOpeningOrPrepending(t *testing.T) {
	s, opens := testSession(t)
	s.scope.Prefix = []byte(`/原路径/"quoted"`)
	presentation := s.KVRequestPresentation()
	var list struct{ Prefix string }
	if json.Unmarshal([]byte(presentation.Default.JSON), &list) != nil || list.Prefix != string(s.scope.Prefix) {
		t.Fatal("configured initial prefix was lost or JSON was not escaped")
	}
	for _, method := range presentation.Methods {
		if _, _, err := decodeRequest(method.Request); err != nil {
			t.Fatal("configured native template is invalid", method.Name, err)
		}
	}
	l := reserve(t, s, 1)
	original := contract.KVRequest{Method: "kv_get", JSON: `{"key":"/outside-suggestion"}`}
	prepared := plan(t, l, original.Method, original.JSON)
	if prepared.Snapshot() != original || opens.Load() != 0 {
		t.Fatal("initial suggestion changed explicit request or opened a process")
	}
}
