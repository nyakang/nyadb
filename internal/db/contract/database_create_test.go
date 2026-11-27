package contract_test

import (
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDatabaseCreateReceiptClonePreservesIndependentObservations(t *testing.T) {
	target := contract.DatabaseCreateTarget{Namespace: contract.NamespaceRef{ProfileID: 7, Schema: "database"}, Generation: 9}
	before := contract.DatabaseCreateSnapshot{Target: target, Engine: "test", Revision: "absence"}
	actual := before
	actual.Exists = true
	actual.NativeID = "10"
	actual.Revision = "created"
	out := contract.DatabaseCreateOutcome{State: "committed", Verified: true, Actual: &actual, Receipt: &contract.DatabaseCreateReceipt{DocumentID: "create:7:1", Sequence: 1, Original: before, State: "committed", Verified: true, Actual: &actual, NativeJSON: `{"original":true}`}}
	copy := out.Clone()
	copy.Actual.NativeID = "changed-current"
	copy.Receipt.Actual.NativeID = "changed-receipt"
	copy.Receipt.Original.Revision = "changed-proof"
	if actual.NativeID != "10" || out.Receipt.Original.Revision != "absence" || copy.Actual.NativeID == copy.Receipt.Actual.NativeID {
		t.Fatal("original receipt or current observation aliased")
	}
	if copy.Receipt.NativeJSON != out.Receipt.NativeJSON {
		t.Fatal("raw receipt changed")
	}
}

func TestDatabaseCreateDocumentRequiresOriginalIdentity(t *testing.T) {
	r := contract.DatabaseCreateDocumentRequest{DocumentID: "create:7:1", Target: contract.DatabaseCreateTarget{Namespace: contract.NamespaceRef{ProfileID: 7, Schema: "database"}, Generation: 9}}
	if !r.Valid() {
		t.Fatal("qualified request rejected")
	}
	for _, edit := range []func(*contract.DatabaseCreateDocumentRequest){
		func(r *contract.DatabaseCreateDocumentRequest) { r.DocumentID = "" },
		func(r *contract.DatabaseCreateDocumentRequest) { r.DocumentID = "bad\nidentity" },
		func(r *contract.DatabaseCreateDocumentRequest) { r.Target.Generation = 0 },
		func(r *contract.DatabaseCreateDocumentRequest) { r.Target.Namespace.ProfileID = 0 },
		func(r *contract.DatabaseCreateDocumentRequest) { r.Target.Namespace.Schema = "" },
	} {
		bad := r
		edit(&bad)
		if bad.Valid() {
			t.Fatal("incomplete original scope admitted", bad)
		}
	}
}
