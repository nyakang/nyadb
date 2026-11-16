package tdengine

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDataWriteRuntimePublishesOnlyOriginalConfirmedNativeEdits(t *testing.T) {
	s := localDataSession(t)
	l := nativeDataDocument(t, s, s.Namespace("original").Object("table", "items"))
	r := l.Runtime()
	if !contract.BoundDataCapabilities(r).EditableTables || r.Saver != nil || r.Verify != nil || r.Transactions.Begin != nil || !r.NativeReceipts || !r.SequentialWrites || !r.InsertOmittedValues || !slices.Equal(r.EditKinds, []string{"insert", "update", "delete"}) || !slices.Equal(r.EditKinds, r.PreviewEditKinds) || r.MutationPresentation == nil {
		t.Fatal("native confirmation binding missing", r)
	}
	if !slices.Contains(r.EditValueKinds, contract.Decimal) || !slices.Contains(r.EditValueKinds, contract.DateTime) || slices.Contains(r.EditValueKinds, contract.JSON) {
		t.Fatal("native types silently substituted")
	}
	copy := l.Runtime()
	copy.EditKinds[0] = "foreign"
	copy.PreviewEditKinds[0] = "foreign"
	copy.EditValueKinds[0] = contract.JSON
	copy.MutationPresentation.Title = "foreign"
	again := l.Runtime()
	if !slices.Equal(r.EditKinds, again.EditKinds) || !slices.Equal(r.PreviewEditKinds, again.PreviewEditKinds) || !slices.Equal(r.EditValueKinds, again.EditValueKinds) || !reflect.DeepEqual(r.MutationPresentation, again.MutationPresentation) {
		t.Fatal("caller mutated original runtime publication")
	}
	p := &dataWritePlan{owner: l, snapshot: contract.DataMutationSnapshot{Object: l.object}}
	l.writePlan = p
	if _, err := r.SubmitEdits(nil, s.ProfileID(), "original", "items", p); !errors.Is(err, connections.ErrInvalid) || p.consumed || l.writePlan != p {
		t.Fatal("nil context consumed original plan", err)
	}
	_ = p.Close()
	s.profile.TDengineOptions.ReadOnly = true
	read := l.Runtime()
	if contract.BoundDataCapabilities(read).EditableTables || s.Capabilities().EditableTables {
		t.Fatal("readonly session advertised editing")
	}
	if _, err := read.PreviewEdits(context.Background(), s.ProfileID(), "original", "items", []contract.DataEdit{{Kind: "insert"}}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("readonly dispatched native preview", err)
	}
}
