package dbruntime_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestSchemaTruncateBindsOriginalOperationAndBlocksUnknownReplay(t *testing.T) {
	for _, fault := range []string{"", "delete-receipt", "unknown", "foreign-original"} {
		t.Run(fault, func(t *testing.T) {
			var calls atomic.Int32
			m, target := schemaLeaseRuntime(t, func(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
				l := &schemaDocumentFake{}
				l.ops.CanTruncate = func(t contract.SchemaDesignTarget) bool { return t == r.Target }
				l.ops.Snapshot = func(_ context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
					return schemaLeaseSnapshot(target), nil
				}
				l.ops.Truncate = func(_ context.Context, original contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
					calls.Add(1)
					if original.Target != r.Target || original.Target.Generation != 973 {
						t.Error("truncate did not capture native original scope")
					}
					out := runtimeClearOutcome(r.DocumentID, contract.SchemaClearRequest{Original: original}, true)
					out.Rows, out.Receipt.Rows = 0, 0
					out.Receipt.Operation = "truncate"
					out.Receipt.Statements = []string{"TRUNCATE TABLE main.t"}
					out.Receipt.NativeJSON = `{"native_ddl_ack":true}`
					switch fault {
					case "delete-receipt":
						out.Receipt.Operation = "clear"
					case "unknown":
						out.State, out.Verified, out.Actual = "uncertain", false, nil
						out.Receipt.State, out.Receipt.Verified, out.Receipt.Actual, out.Receipt.NativeJSON = "uncertain", false, nil, ""
					case "foreign-original":
						out.Receipt.Original.Target.Object.ProfileID++
					}
					original.Schema.Table.Clauses[0].SQL = "provider changed its copy"
					return out
				}
				return l, nil
			}, contract.SchemaDesignDocumentFeatures{CanTruncate: func(contract.SchemaDesignTarget) bool { return true }})
			lease := reserveRuntimeSchema(t, m, "original-truncate", target)
			ops := lease.Operations()
			before := schemaLeaseSnapshot(target)
			captured := before.Clone()
			if !ops.CanTruncate(target) {
				t.Fatal("truncate capability missing")
			}
			out := ops.Truncate(context.Background(), before)
			if !reflect.DeepEqual(before, captured) || out.Receipt == nil || out.Rows != 0 {
				t.Fatal("truncate mutated original or invented deletion count")
			}
			if fault == "" {
				if out.Error != nil || !out.Verified || out.Actual.Target != target || out.Receipt.Original.Target != target || out.Receipt.Operation != "truncate" {
					t.Fatal("native truncate generation or operation changed", out.Error)
				}
			} else if out.Verified || ops.CanTruncate(target) {
				t.Fatal("untrusted truncate acknowledged or remained writable")
			}
			retained := lease.OriginalOutcome()
			out.Receipt.NativeJSON = "caller changed its copy"
			_, _ = ops.Snapshot(context.Background(), target)
			if fault != "" {
				if retry := ops.Truncate(context.Background(), before); retry.State != "not_executed" || !errors.Is(retry.Error, contract.ErrSchemaCommitUncertain) || calls.Load() != 1 {
					t.Fatal("unknown truncate replayed", retry)
				}
			}
			if !reflect.DeepEqual(retained, lease.OriginalOutcome()) {
				t.Fatal("read/caller/retry changed original evidence")
			}
		})
	}
}
