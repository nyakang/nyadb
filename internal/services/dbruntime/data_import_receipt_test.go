package dbruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type importReceiptHandle struct {
	handle
	lease *importReceiptLease
}
type importReceiptLease struct {
	receipt    contract.DataImportBatchReceipt
	calls      int
	generation uint64
}

func (h *importReceiptHandle) Namespace(db string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: h.id, Catalog: db}
}
func (h *importReceiptHandle) DataDocumentCapabilities() contract.Capabilities {
	return contract.Capabilities{Import: true}
}
func (h *importReceiptHandle) ReserveDataDocument(contract.ObjectRef) (contract.DataDocumentLease, error) {
	return h.lease, nil
}
func (l *importReceiptLease) Close() error { return nil }
func (l *importReceiptLease) Runtime() contract.DataRuntime {
	return contract.DataRuntime{Capabilities: contract.Capabilities{Import: true}, ImportAtomicBatches: true, StreamImporter: func(_ context.Context, _ int64, _, _ string, r contract.DataImportStreamRequest) (*contract.DataImportStreamResult, error) {
		l.calls++
		l.generation = r.Generation
		e := r.RecordReceipt(l.receipt)
		return &contract.DataImportStreamResult{Incremental: true, LastReceipt: &l.receipt, CommittedRows: 1}, e
	}, Importer: func(context.Context, int64, string, string, contract.DataImportRequest) (*contract.DataImportResult, error) {
		return &contract.DataImportResult{Receipts: []contract.DataImportBatchReceipt{l.receipt}}, nil
	}}
}
func TestDataImportReceiptTranslatesOriginalGenerationWithoutChangingNativeEvidence(t *testing.T) {
	m := dbruntime.New()
	defer m.Close()
	var h *importReceiptHandle
	m.Register(connections.SQLite, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h = &importReceiptHandle{handle: handle{id: p.ID, caps: contract.Capabilities{Import: true}}, lease: &importReceiptLease{}}
		return h, nil
	}))
	if _, e := m.ConnectSession(context.Background(), profile(1), ""); e != nil {
		t.Fatal(e)
	}
	public, e := m.ConnectSession(context.Background(), profile(1), "")
	if e != nil {
		t.Fatal(e)
	}
	object := m.Namespace(1, "main").Object("table", "destination")
	h.lease.receipt = contract.DataImportBatchReceipt{Object: object, Generation: 1, Sequence: 3, FirstRow: 129, LastRow: 192, State: "committed", NativeJSON: `{"original": 9007199254740993}`}
	l, e := m.Data(1).ReserveDocument(object)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	r := l.Runtime()
	if !r.ImportAtomicBatches || r.StreamImporter == nil || public.Generation() == 1 {
		t.Fatal("document import feature or distinct public generation missing")
	}
	var recorded contract.DataImportBatchReceipt
	request := contract.DataImportStreamRequest{Generation: public.Generation(), RecordReceipt: func(receipt contract.DataImportBatchReceipt) error { recorded = receipt; return nil }}
	out, e := r.StreamImporter(context.Background(), 1, "main", "destination", request)
	if e != nil || recorded.Generation != public.Generation() || out.LastReceipt.Generation != public.Generation() || h.lease.generation != 1 || out.LastReceipt.NativeJSON != h.lease.receipt.NativeJSON || h.lease.receipt.Generation != 1 || out.LastReceipt.FirstRow != 129 {
		t.Fatal("public/native generation or original receipt changed", e)
	}
	finite, e := r.Importer(context.Background(), 1, "main", "destination", contract.DataImportRequest{})
	if e != nil || finite.Receipts[0].Generation != public.Generation() || finite.Receipts[0].NativeJSON != h.lease.receipt.NativeJSON {
		t.Fatal("finite import generation/evidence changed", e)
	}
	request.Generation = public.Generation() + 1
	if _, e = r.StreamImporter(context.Background(), 1, "main", "destination", request); !errors.Is(e, contract.ErrRevoked) || h.lease.calls != 1 {
		t.Fatal("stale generation reached provider", e)
	}
	request.Generation = public.Generation()
	h.lease.receipt.Generation = 9
	if _, e = r.StreamImporter(context.Background(), 1, "main", "destination", request); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("foreign native receipt generation accepted", e)
	}
	h.lease.receipt.Generation = 1
	h.lease.receipt.Object.ProfileID = 2
	if _, e = r.StreamImporter(context.Background(), 1, "main", "destination", request); !errors.Is(e, connections.ErrInvalid) {
		t.Fatal("foreign original receipt profile accepted", e)
	}
}
