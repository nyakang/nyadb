package dbruntime_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func runtimeClearOutcome(id string, r contract.SchemaClearRequest, verified bool) contract.SchemaDesignOutcome {
	out := contract.SchemaDesignOutcome{State: "committed", Verified: verified, Rows: 3}
	actual := r.Original.Clone()
	out.Actual = &actual
	out.Receipt = &contract.SchemaDesignWriteReceipt{DocumentID: id, Sequence: 1, Operation: "clear", Original: r.Original.Clone(), Destination: r.Original.Target.Object.Name, ResetIdentity: r.ResetIdentity, State: out.State, Verified: verified, Rows: out.Rows, Actual: &actual, Statements: []string{"DELETE FROM main.t"}, NativeJSON: `{"results":[{"rows_affected":3}],"raft_index":9007199254740993}`}
	return out
}

func TestSchemaClearDocumentTranslatesOriginalGenerationWithoutRetargetAndClonesReceipt(t *testing.T) {
	var native contract.SchemaDesignOutcome
	m, target := schemaLeaseRuntime(t, func(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
		l := &schemaDocumentFake{}
		l.ops.CanClear = func(tgt contract.SchemaDesignTarget) bool { return tgt == r.Target }
		l.ops.Snapshot = func(_ context.Context, tgt contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
			return schemaLeaseSnapshot(tgt), nil
		}
		l.ops.Clear = func(_ context.Context, request contract.SchemaClearRequest) contract.SchemaDesignOutcome {
			if request.Original.Target.Generation != 973 || !request.ResetIdentity || request.Original.Target != r.Target {
				t.Error("clear did not capture native generation and original identity")
			}
			native = runtimeClearOutcome(r.DocumentID, request, true)
			request.Original.Schema.Table.Clauses[0].SQL = "provider mutation"
			return native
		}
		return l, nil
	}, contract.SchemaDesignDocumentFeatures{CanClear: func(contract.SchemaDesignTarget) bool { return true }})
	l := reserveRuntimeSchema(t, m, "original-clear", target)
	request := contract.SchemaClearRequest{Original: schemaLeaseSnapshot(target), ResetIdentity: true}
	before := request.Clone()
	ops := l.Operations()
	if !ops.CanClear(target) {
		t.Fatal("clear missing on original document")
	}
	out := ops.Clear(context.Background(), request)
	if out.Error != nil || !out.Verified || out.Actual.Target != target || out.Receipt.Original.Target != target || !out.Receipt.ResetIdentity || !reflect.DeepEqual(before, request) || native.Receipt.Original.Target.Generation != 973 {
		t.Fatal("clear changed identity, input, original receipt, or generation", out.Error)
	}
	retained := l.OriginalOutcome()
	out.Receipt.NativeJSON = "caller mutation"
	out.Receipt.Original.Schema.Table.Clauses[0].SQL = "caller mutation"
	if _, err := ops.Snapshot(context.Background(), target); err != nil {
		t.Fatal("clear retargeted its original document", err)
	}
	if !reflect.DeepEqual(retained, l.OriginalOutcome()) {
		t.Fatal("independent read or caller replaced original clear evidence")
	}
}

func TestSchemaClearUnknownOrMismatchedPolicyRetainsNativeEvidenceAndBlocksAllWrites(t *testing.T) {
	for _, mode := range []string{"unknown", "reset-policy", "foreign-target"} {
		t.Run(mode, func(t *testing.T) {
			var writes atomic.Int32
			m, target := schemaLeaseRuntime(t, func(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
				l := &schemaDocumentFake{}
				l.ops.Snapshot = func(_ context.Context, tgt contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
					return schemaLeaseSnapshot(tgt), nil
				}
				can := func(contract.SchemaDesignTarget) bool { return true }
				l.ops.CanClear, l.ops.CanApply, l.ops.CanDrop, l.ops.CanCopy, l.ops.CanRename = can, can, can, can, can
				l.ops.Clear = func(_ context.Context, request contract.SchemaClearRequest) contract.SchemaDesignOutcome {
					writes.Add(1)
					out := runtimeClearOutcome(r.DocumentID, request, true)
					switch mode {
					case "unknown":
						out.State, out.Verified, out.Actual = "uncertain", false, nil
						out.Receipt.State, out.Receipt.Verified, out.Receipt.Actual, out.Receipt.NativeJSON = "uncertain", false, nil, ""
					case "reset-policy":
						out.Receipt.ResetIdentity = !request.ResetIdentity
					case "foreign-target":
						out.Actual.Target.Object.Name = "foreign"
					}
					return out
				}
				l.ops.Apply = func(context.Context, contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
					t.Error("unknown clear replayed as apply")
					return contract.SchemaDesignOutcome{}
				}
				l.ops.Drop = func(context.Context, contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
					t.Error("unknown clear replayed as drop")
					return contract.SchemaDesignOutcome{}
				}
				l.ops.Copy = func(context.Context, contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
					t.Error("unknown clear replayed as copy")
					return contract.SchemaDesignOutcome{}
				}
				l.ops.Rename = func(context.Context, contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
					t.Error("unknown clear replayed as rename")
					return contract.SchemaDesignOutcome{}
				}
				return l, nil
			}, contract.SchemaDesignDocumentFeatures{CanClear: func(contract.SchemaDesignTarget) bool { return true }})
			l := reserveRuntimeSchema(t, m, mode, target)
			ops := l.Operations()
			request := contract.SchemaClearRequest{Original: schemaLeaseSnapshot(target), ResetIdentity: true}
			out := ops.Clear(context.Background(), request)
			if out.Verified || out.Receipt == nil || mode != "unknown" && out.Error == nil {
				t.Fatal("unconfirmed clear accepted", out.Error)
			}
			if mode == "reset-policy" && out.Receipt.ResetIdentity {
				t.Fatal("mismatched original receipt was rewritten")
			}
			retained := l.OriginalOutcome()
			if _, err := ops.Snapshot(context.Background(), target); err != nil {
				t.Fatal(err)
			}
			results := []contract.SchemaDesignOutcome{ops.Clear(context.Background(), request), ops.Apply(context.Background(), contract.SchemaDesignRequest{Original: request.Original, Desired: request.Original.Schema.Table}), ops.Drop(context.Background(), request.Original), ops.Copy(context.Background(), contract.SchemaCopyRequest{Original: request.Original, Destination: "copied"}), ops.Rename(context.Background(), contract.SchemaRenameRequest{Original: request.Original, Destination: "renamed"})}
			for _, next := range results {
				if next.State != "not_executed" || !errors.Is(next.Error, contract.ErrSchemaCommitUncertain) || next.Receipt != nil {
					t.Fatal("unknown clear replayed or relabelled", next)
				}
			}
			if writes.Load() != 1 || !reflect.DeepEqual(retained, l.OriginalOutcome()) || ops.CanClear(target) || ops.CanApply(target) || ops.CanDrop(target) || ops.CanCopy(target) || ops.CanRename(target) {
				t.Fatal("unknown clear barrier lost original evidence")
			}
		})
	}
}
