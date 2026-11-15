package dbruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type sequentialRuntimeHandle struct {
	handle
	presentation *contract.NativeWriteReceiptPresentation
	result       *contract.DataEditResult
}

func (h *sequentialRuntimeHandle) DataRuntime() contract.DataRuntime {
	return contract.DataRuntime{Capabilities: h.caps, NativeReceipts: true, SequentialWrites: true, ReceiptPresentation: h.presentation,
		Saver: func(context.Context, int64, string, string, []contract.DataEdit) (*contract.DataEditResult, error) {
			return h.result, contract.ErrDataCommitUncertain
		}}
}

type sequentialFeatureHandle struct{ *sequentialRuntimeHandle }

func (h *sequentialFeatureHandle) DataReads() contract.DataReads { return contract.DataReads{} }
func (h *sequentialFeatureHandle) DataEdits() contract.DataEdits {
	r := h.DataRuntime()
	return contract.DataEdits{NativeReceipts: r.NativeReceipts, SequentialWrites: r.SequentialWrites, ReceiptPresentation: r.ReceiptPresentation, Saver: r.Saver}
}

func TestDataRuntimePreservesOriginalSequentialReceiptAndCopiesPresentation(t *testing.T) {
	for _, modern := range []bool{false, true} {
		name := "aggregate"
		if modern {
			name = "feature"
		}
		t.Run(name, func(t *testing.T) {
			m := dbruntime.New()
			defer m.Close()
			object := contract.NamespaceRef{ProfileID: 1, Catalog: "main"}.Object("table", "owned")
			h := &sequentialRuntimeHandle{handle: handle{id: 1, caps: contract.Capabilities{EditableTables: true}}, presentation: &contract.NativeWriteReceiptPresentation{Title: "original receipt"}, result: &contract.DataEditResult{NativeJSON: `{"original":true}`, Sequential: &contract.DataSequentialWriteReceipt{Object: object, Statements: []contract.DataWriteStatementReceipt{{EditIndex: 0, Kind: "update", Destination: object, State: contract.DataWriteUnknown}}}}}
			m.Register(connections.SQLite, dbruntime.ProviderFunc(func(context.Context, connections.Profile, string) (contract.Session, error) {
				if modern {
					return &sequentialFeatureHandle{h}, nil
				}
				return h, nil
			}))
			ctx := context.Background()
			if err := m.Connect(ctx, profile(1), ""); err != nil {
				t.Fatal(err)
			}
			r := m.Data(1)
			h.presentation.Title = "replacement"
			if !r.NativeReceipts || !r.SequentialWrites || r.ReceiptPresentation == nil || r.ReceiptPresentation.Title != "original receipt" {
				t.Fatal("runtime discarded original write semantics or retained mutable presentation")
			}
			receipt, err := r.Saver(ctx, 1, "main", "owned", nil)
			if !errors.Is(err, contract.ErrDataCommitUncertain) || receipt != h.result || receipt.CommitConfirmed || receipt.Sequential.Statements[0].State != contract.DataWriteUnknown {
				t.Fatal("runtime replaced original missing acknowledgement with commit confirmation", err)
			}
			if err := m.Disconnect(1); err != nil {
				t.Fatal(err)
			}
			if result, err := r.Saver(ctx, 1, "main", "owned", nil); !errors.Is(err, contract.ErrRevoked) || result != nil {
				t.Fatal("sequential runtime followed replacement generation", err)
			}
		})
	}
}
