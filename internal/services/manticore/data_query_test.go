package manticore

import (
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDataOptionsCaptureOriginalConditionAndRejectAmbiguousInputs(t *testing.T) {
	object := contract.ObjectRef{ProfileID: 1, Catalog: "Manticore", Kind: "TABLE", Name: "original"}
	leaf := contract.DataFilterRule{Column: "qty", Mode: "equal", Value: "7"}
	options := contract.DataOptions{Object: object, Condition: &contract.FilterExpression[contract.DataFilterRule]{Op: "rule", Rule: &leaf}, Sorts: []contract.DataSortRule{{Column: "id"}}}
	copy, e := captureOptions(options, object)
	if e != nil {
		t.Fatal(e)
	}
	leaf.Value = "8"
	options.Sorts[0].Column = "other"
	if copy.Condition.Rule.Value != "7" || copy.Sorts[0].Column != "id" {
		t.Fatal("borrowed original request")
	}
	normal, e := captureOptions(contract.DataOptions{FilterColumn: "qty", FilterValue: "7"}, object)
	if e != nil || normal.FilterMode != "equal" {
		t.Fatal("legacy filter silently omitted", normal, e)
	}
	for _, o := range []contract.DataOptions{{FilterValue: "7"}, {Condition: copy.Condition, Filters: []contract.DataFilterRule{leaf}}, {Object: contract.ObjectRef{ProfileID: 2}}, {Condition: &contract.FilterExpression[contract.DataFilterRule]{Op: "and"}}} {
		if _, e = captureOptions(o, object); !errors.Is(e, connections.ErrInvalid) {
			t.Fatal("ambiguous original request", e)
		}
	}
}
