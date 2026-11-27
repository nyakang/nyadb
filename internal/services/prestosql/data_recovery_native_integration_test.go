package prestosql

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestPrestoSQLPrivateDataNativeUnknownWriteRecovery(t *testing.T) {
	f, p, c, _ := nativeProvider(t)
	relay := newProviderRelay(t, f.Endpoint, `INSERT INTO "memory"."default"."uncertain_provider65"`)
	c.Endpoint = relay.server.URL
	s, q, ctx := nativeDataSession(t, p, c)
	queryNative(t, q, "", `CREATE TABLE memory.default.uncertain_provider65 (id bigint)`)
	l, r, label := nativeDataDocument(t, s, "uncertain_provider65")
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "65"}}}}
	plan, err := r.PreviewEdits(ctx, 65, label, "uncertain_provider65", edits)
	if err != nil {
		t.Fatal(err)
	}
	relay.armed.Store(true)
	result, err := r.SubmitEdits(ctx, 65, label, "uncertain_provider65", plan)
	if !errors.Is(err, contract.ErrDataCommitUncertain) || result == nil || !result.CommitUncertain || !result.Incomplete || result.Applied != 0 || result.Sequential.Statements[0].State != contract.DataWriteUnknown {
		t.Fatal("completed but withheld write was not unknown", err, result)
	}
	if l.unresolved == nil || len(l.unresolved.OriginalResponse()) == 0 {
		t.Fatal("original unknown receipt discarded")
	}
	retained := string(l.unresolved.OriginalResponse())
	relay.mu.Lock()
	completed := append(json.RawMessage(nil), relay.completed...)
	relay.mu.Unlock()
	var original struct {
		UpdateCount int
		Stats       struct{ State string }
	}
	if json.Unmarshal(completed, &original) != nil || original.UpdateCount != 1 || original.Stats.State != "FINISHED" {
		t.Fatal("relay did not capture an actual completed write")
	}
	if _, err = r.PreviewEdits(ctx, 65, label, "uncertain_provider65", edits); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("unknown write admitted more writes", err)
	}
	if _, err = r.SubmitEdits(ctx, 65, label, "uncertain_provider65", plan); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("unknown original plan replayed", err)
	}
	count, err := r.Count(ctx, 65, label, "uncertain_provider65")
	if err != nil || count != 1 {
		t.Fatal("independent observation", err, count)
	}
	if string(l.unresolved.OriginalResponse()) != retained {
		t.Fatal("later count replaced unknown receipt")
	}
	if _, err = r.Loader(ctx, 65, label, "uncertain_provider65", 0, 10); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("lost data owner implicitly recovered", err)
	}
	_, recovery, _ := nativeDataDocument(t, s, "uncertain_provider65")
	page, err := recovery.Loader(ctx, 65, label, "uncertain_provider65", 0, 10)
	if err != nil || len(page.Rows) != 1 || page.Rows[0][0].Text != "65" || relay.writes.Load() != 1 {
		t.Fatal("explicit recovery replayed write", err)
	}
	providerProof(t, f, "data-unknown-recovery.json", map[string]any{"original_completed_server_response": completed, "unknown_receipt": result, "later_count": count, "recovery_page": page, "writes_dispatched": relay.writes.Load(), "original_unknown_receipt_retained": true})
	queryNative(t, q, "", `DROP TABLE memory.default.uncertain_provider65`)
	_ = s.Close()
	if err = p.Store.Remove(ctx, "prestosql", c.DriverVersion); err != nil {
		t.Fatal(err)
	}
}

func TestPrestoSQLPrivateDataNativeCountCancellationPreservesPage(t *testing.T) {
	f, p, c, e := nativeProvider(t)
	s, _, ctx := nativeDataSession(t, p, c)
	n := contract.NamespaceRef{ProfileID: 65, Catalog: "tpch", Schema: "sf100"}
	lease, err := s.ReserveDataDocument(n.Object("table", "lineitem"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	r, label := lease.Runtime(), namespaceLabel(n)
	page, err := r.Loader(ctx, 65, label, "lineitem", 0, 1)
	if err != nil || len(page.Rows) != 1 {
		t.Fatal("original data page", err)
	}
	active, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := r.Count(active, 65, label, "lineitem"); done <- err }()
	providerRunning(t, f.Endpoint, `SELECT count(*) FROM "tpch"."sf100"."lineitem"`)
	cancel()
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("count executor did not acknowledge cancellation")
	}
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatal("count cancellation lost", err)
	}
	// Metadata is owner zero; reserving the unused SQL document is owner one,
	// the primary data document owner two, and its auxiliary count owner three.
	receipt := e.originalFailure("data-count-3")
	acknowledged := receipt.CancellationRequested && receipt.ExecutorAcknowledged && len(receipt.CancellationResponse()) > 0
	if !acknowledged {
		t.Fatal("original count cancellation receipt missing", receipt)
	}
	again, err := r.Loader(ctx, 65, label, "lineitem", 0, 1)
	if err != nil || len(again.Rows) != 1 || !s.Alive() {
		t.Fatal("auxiliary count retired original page", err)
	}
	providerProof(t, f, "data-count-cancel.json", map[string]any{"before": page, "after": again, "count_receipt": receipt, "original_page_owner_preserved": true})
	_ = s.Close()
	if err = p.Store.Remove(ctx, "prestosql", c.DriverVersion); err != nil {
		t.Fatal(err)
	}
}
