package consul

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// The fixture maintains real state and checks the whole temporary transaction
// before publishing deletions. Tests observe durable progress and later keys.
type prefixSetFixture struct {
	mu                    sync.Mutex
	entries               map[string]map[string]any
	captures, writes      atomic.Int32
	missing               bool
	pauseRead, pauseWrite atomic.Bool
	entered               chan struct{}
}

func prefixSetLease(t *testing.T, count int) (*dataLease, *prefixSetFixture) {
	t.Helper()
	store := &prefixSetFixture{entries: map[string]map[string]any{}}
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("scope/set/%04d", i)
		store.entries[key] = dataTestEntry(key, []byte{0, 255})
	}
	f := &dataFixture{}
	f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("X-Consul-Results-Filtered-By-Acls", "false")
		store.mu.Lock()
		defer store.mu.Unlock()
		if r.Method == "GET" {
			prefix := strings.TrimPrefix(r.URL.Path, "/v1/kv/")
			if prefix == "scope/set/" {
				store.captures.Add(1)
			}
			keys := []string{}
			for key := range store.entries {
				if strings.HasPrefix(key, prefix) {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			entries := []map[string]any{}
			for _, key := range keys {
				entries = append(entries, store.entries[key])
			}
			json.NewEncoder(w).Encode(entries)
			return true
		}
		var ops []TxnOperation
		if r.URL.Path != "/v1/txn" || json.NewDecoder(r.Body).Decode(&ops) != nil {
			w.WriteHeader(400)
			return true
		}
		if len(ops) > 0 && ops[0].KV.Verb == "get" && store.pauseRead.CompareAndSwap(true, false) {
			close(store.entered)
			<-r.Context().Done()
			return true
		}
		staged := map[string]map[string]any{}
		for key, value := range store.entries {
			staged[key] = value
		}
		results := []map[string]any{}
		write := false
		for i, operation := range ops {
			op := operation.KV
			entry, exists := staged[op.Key]
			conflict := false
			switch op.Verb {
			case "get-tree":
				if op.Key == "scope/set/" {
					store.captures.Add(1)
				}
				keys := []string{}
				for key := range staged {
					if strings.HasPrefix(key, op.Key) {
						keys = append(keys, key)
					}
				}
				sort.Strings(keys)
				for _, key := range keys {
					results = append(results, map[string]any{"KV": staged[key]})
				}
			case "get":
				if exists {
					results = append(results, map[string]any{"KV": entry})
				}
			case "check-index":
				conflict = !exists || op.Index == nil || entry["ModifyIndex"].(uint64) != *op.Index
				if !conflict {
					projection := map[string]any{}
					for name, value := range entry {
						projection[name] = value
					}
					projection["Value"] = nil
					results = append(results, map[string]any{"KV": projection})
				}
			case "delete-cas":
				write = true
				conflict = !exists || op.Index == nil || entry["ModifyIndex"].(uint64) != *op.Index
				if !conflict {
					delete(staged, op.Key)
				}
			case "check-not-exists":
				conflict = exists
			default:
				w.WriteHeader(400)
				return true
			}
			if conflict {
				w.WriteHeader(409)
				json.NewEncoder(w).Encode(map[string]any{"Results": []any{}, "Errors": []map[string]any{{"OpIndex": i, "What": "original comparison failed"}}})
				return true
			}
		}
		if write {
			store.entries = staged
			store.writes.Add(1)
			if store.pauseWrite.CompareAndSwap(true, false) {
				close(store.entered)
				<-r.Context().Done()
				return true
			}
		}
		if write && store.missing {
			w.Write([]byte(`{"Results":null,"Errors":null}`))
			return true
		}
		json.NewEncoder(w).Encode(map[string]any{"Results": results, "Errors": []any{}})
		return true
	}
	return dataTestLease(t, dataTestSession(t, "scope/", f)), store
}

func TestDataKeyPrefixDeleteSetCloseRevokesPendingPreviewAndOriginalProcessedSubmit(t *testing.T) {
	for _, phase := range []string{"preview", "submit"} {
		t.Run(phase, func(t *testing.T) {
			l, f := prefixSetLease(t, 43)
			set := capturePrefixSet(t, l)
			f.entered = make(chan struct{})
			type completion struct {
				plan   contract.DataMutationPlan
				result *contract.DataEditResult
				err    error
			}
			done := make(chan completion, 1)
			if phase == "preview" {
				f.pauseRead.Store(true)
				go func() {
					plan, err := set.PreviewBatch(context.Background(), 0)
					done <- completion{plan: plan, err: err}
				}()
			} else {
				plan := prefixSetBatch(t, set, 0)
				f.pauseWrite.Store(true)
				go func() {
					result, err := l.submit(context.Background(), 11, "kv", "keys", plan)
					done <- completion{result: result, err: err}
				}()
			}
			select {
			case <-f.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("original executor did not enter")
			}
			if phase == "submit" && !set.Snapshot().InFlight {
				t.Fatal("set advanced before executor acknowledgement")
			}
			set.Close()
			if len(set.Snapshot().Candidates) != 0 || set.Snapshot().NativeJSON != "" {
				t.Fatal("closed capture retained caches")
			}
			select {
			case completion := <-done:
				if completion.err == nil || completion.plan != nil {
					t.Fatal("closed operation retained authority")
				}
				if phase == "submit" && (completion.result == nil || !completion.result.CommitUncertain || !errors.Is(completion.err, contract.ErrDataCommitUncertain)) {
					t.Fatal("original processed write cancellation promoted known result", completion.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original executor did not acknowledge cancellation")
			}
			receiptBudgetMu.Lock()
			reserved := l.session.reserved
			receiptBudgetMu.Unlock()
			if reserved != 0 {
				t.Fatal("late worker retained capture/plan reservation", reserved)
			}
			if phase == "submit" {
				if _, err := l.captureKeyPrefixDelete(context.Background(), 11, "kv", "keys", contract.DataKeyPrefixDelete{Object: l.object, Prefix: "scope/set/"}); !errors.Is(err, contract.ErrDataCommitUncertain) {
					t.Fatal("closed processed write permitted fresh capture", err)
				}
			} else {
				if _, err := l.captureKeyPrefixDelete(context.Background(), 11, "kv", "keys", contract.DataKeyPrefixDelete{Object: l.object, Prefix: "scope/set/"}); !errors.Is(err, contract.ErrKVLeaseLost) {
					t.Fatal("canceled original read silently acquired a fresh executor", err)
				}
			}
		})
	}
}

func TestDataKeyPrefixDeleteSetRejectsForeignInputAndReservesCapturePlusFirstPlanBeforeIO(t *testing.T) {
	l, f := prefixSetLease(t, 43)
	before := f.captures.Load()
	request := contract.DataKeyPrefixDelete{Object: l.object, Prefix: "scope/set/"}
	for _, fault := range []string{"root", "prefix", "object", "profile"} {
		t.Run(fault, func(t *testing.T) {
			foreign := request
			id := int64(11)
			switch fault {
			case "root":
				foreign.Prefix = ""
			case "prefix":
				foreign.Prefix = "foreign/"
			case "object":
				foreign.Object.Name = "foreign"
			case "profile":
				id = 12
			}
			if capture, err := l.captureKeyPrefixDelete(context.Background(), id, "kv", "keys", foreign); capture != nil || err == nil {
				t.Fatal("foreign capture reached original executor")
			}
		})
	}
	receiptBudgetMu.Lock()
	originalBudget := l.session.budget
	l.session.budget = l.session.retained + l.session.reserved + dataPrefixSetBudget + dataPlanBudget + dataPrefixReceiptBudget + pendingReceiptBudget - 1
	receiptBudgetMu.Unlock()
	if capture, err := l.captureKeyPrefixDelete(context.Background(), 11, "kv", "keys", request); capture != nil || !errors.Is(err, ErrBounds) {
		t.Fatal("capture did not reserve its original evidence and first plan", err)
	}
	receiptBudgetMu.Lock()
	l.session.budget = originalBudget
	reserved := l.session.reserved
	receiptBudgetMu.Unlock()
	if f.captures.Load() != before || reserved != 0 {
		t.Fatal("invalid capture made I/O or leaked reservation")
	}
}

func capturePrefixSet(t *testing.T, l *dataLease) *dataKeyPrefixDeletionSet {
	t.Helper()
	capture, err := l.captureKeyPrefixDelete(context.Background(), 11, "kv", "keys", contract.DataKeyPrefixDelete{Object: l.object, Prefix: "scope/set/"})
	if err != nil {
		t.Fatal(err)
	}
	set := capture.(*dataKeyPrefixDeletionSet)
	t.Cleanup(func() { set.Close() })
	return set
}

func prefixSetBatch(t *testing.T, set *dataKeyPrefixDeletionSet, index int) contract.DataMutationPlan {
	t.Helper()
	plan, err := set.PreviewBatch(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestDataKeyPrefixDeleteSetOneOriginalCaptureExplicitOrderedBatchesAndLaterKeys(t *testing.T) {
	l, f := prefixSetLease(t, 43)
	set := capturePrefixSet(t, l)
	original := set.Snapshot()
	if len(original.Candidates) != 43 || original.BatchSize != 21 || original.NextBatch != 0 || len(original.Receipts) != 0 || !strings.Contains(original.NativeJSON, "scope/set/0042") {
		t.Fatal("complete original candidate capture missing")
	}
	original.Candidates[0].Ref.Values[0][0] = '!'
	original.Candidates[0].Ref.TypedValues[0].Text = "foreign"
	if set.Snapshot().Candidates[0].Ref.Values[0][0] == '!' || set.Snapshot().Candidates[0].Ref.TypedValues[0].Text == "foreign" {
		t.Fatal("capture inspection changed original authority")
	}
	if _, err := set.PreviewBatch(context.Background(), 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("later batch skipped original confirmation", err)
	}
	first := prefixSetBatch(t, set, 0)
	if _, err := set.PreviewBatch(context.Background(), 0); !errors.Is(err, contract.ErrKVBusy) {
		t.Fatal("two original batch plans admitted", err)
	}
	first.Close()
	first = prefixSetBatch(t, set, 0)
	f.mu.Lock()
	f.entries["scope/set/later"] = dataTestEntry("scope/set/later", []byte("preserved"))
	f.mu.Unlock()
	for index, count := range []int{21, 21, 1} {
		plan := first
		if index > 0 {
			plan = prefixSetBatch(t, set, index)
		}
		snapshot := plan.Snapshot()
		if len(snapshot.Edits) != count || snapshot.KeyPrefixDeleteBatch == nil || snapshot.KeyPrefixDeleteBatch.Index != index || snapshot.KeyPrefixDeleteBatch.Total != 43 || snapshot.KeyPrefixDeleteBatch.Offset != index*21 {
			t.Fatal("batch descriptor lost original full-set scope")
		}
		snapshot.KeyPrefixDeleteBatch.Total = 1
		if plan.Snapshot().KeyPrefixDeleteBatch.Total != 43 {
			t.Fatal("batch inspection narrowed complete capture")
		}
		result, err := l.submit(context.Background(), 11, "kv", "keys", plan)
		if err != nil || !result.CommitConfirmed || result.Applied != count {
			t.Fatal("original batch not established", err)
		}
		state := set.Snapshot()
		if state.NextBatch != index+1 || state.InFlight || state.Uncertain || len(state.Receipts) != index+1 {
			t.Fatal("complete receipt failed to advance exactly one batch")
		}
		if _, err = set.PreviewBatch(context.Background(), index); !errors.Is(err, ErrInvalid) {
			t.Fatal("completed batch reacquired authority", err)
		}
	}
	f.mu.Lock()
	remaining := len(f.entries)
	_, later := f.entries["scope/set/later"]
	f.mu.Unlock()
	if remaining != 1 || !later || f.captures.Load() != 1 || f.writes.Load() != 3 {
		t.Fatal("batches recaptured later keys or hid partial progress")
	}
	if l.Runtime().CaptureKeyPrefixDelete != nil {
		t.Fatal("private batch capability leaked to public runtime")
	}
	set.Close()
	receiptBudgetMu.Lock()
	reserved := l.session.reserved
	receiptBudgetMu.Unlock()
	if reserved != 0 {
		t.Fatal("capture/plan budget leaked", reserved)
	}
}

func TestDataKeyPrefixDeleteSetLaterConflictKeepsEarlierCommitAndOriginalReceiptLedger(t *testing.T) {
	l, f := prefixSetLease(t, 43)
	set := capturePrefixSet(t, l)
	first := prefixSetBatch(t, set, 0)
	if _, err := l.submit(context.Background(), 11, "kv", "keys", first); err != nil {
		t.Fatal(err)
	}
	second := prefixSetBatch(t, set, 1)
	f.mu.Lock()
	f.entries["scope/set/0041"]["ModifyIndex"] = uint64(99)
	f.mu.Unlock()
	result, err := l.submit(context.Background(), 11, "kv", "keys", second)
	if !errors.Is(err, contract.ErrDataConflict) || result.CommitConfirmed || result.CommitUncertain {
		t.Fatal("known batch conflict classified incorrectly", err)
	}
	state := set.Snapshot()
	if state.NextBatch != 1 || len(state.Receipts) != 2 || state.Uncertain || !state.Stopped {
		t.Fatal("later conflict hid original partial progress")
	}
	f.mu.Lock()
	remaining := len(f.entries)
	_, earlier := f.entries["scope/set/0000"]
	_, survivor := f.entries["scope/set/0021"]
	f.mu.Unlock()
	if remaining != 22 || earlier || !survivor || f.writes.Load() != 1 {
		t.Fatal("later conflict partially applied or rolled back a prior transaction")
	}
	if _, err = set.PreviewBatch(context.Background(), 1); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("retry replaced original changed candidate", err)
	}
	if f.captures.Load() != 1 {
		t.Fatal("batch retry silently recaptured prefix")
	}
}

func TestDataKeyPrefixDeleteSetUnknownCompleteCaptureAndReceiptsForbidFurtherBatches(t *testing.T) {
	l, f := prefixSetLease(t, 43)
	set := capturePrefixSet(t, l)
	first := prefixSetBatch(t, set, 0)
	f.mu.Lock()
	f.missing = true
	f.mu.Unlock()
	result, err := l.submit(context.Background(), 11, "kv", "keys", first)
	if !errors.Is(err, contract.ErrDataCommitUncertain) || result.CommitConfirmed || !result.Incomplete {
		t.Fatal("missing batch correspondence promoted commit", err)
	}
	state := set.Snapshot()
	if !state.Uncertain || state.NextBatch != 0 || state.InFlight || len(state.Candidates) != 43 || len(state.Receipts) != 1 {
		t.Fatal("unknown batch lost original set or advanced progress")
	}
	if _, err = set.PreviewBatch(context.Background(), 1); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("unknown batch authorized a later write", err)
	}
	if _, err = set.PreviewBatch(context.Background(), 0); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("unknown batch replayed", err)
	}
	set.Close()
	if _, err = l.captureKeyPrefixDelete(context.Background(), 11, "kv", "keys", state.Request); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("closing capture bypassed original executor uncertainty", err)
	}
	f.mu.Lock()
	remaining := len(f.entries)
	f.mu.Unlock()
	if remaining != 22 || f.writes.Load() != 1 || f.captures.Load() != 1 {
		t.Fatal("unknown fixture did not actually apply the original first batch")
	}
}

func TestDataKeyPrefixDeleteSetReservesCompleteReceiptBeforeIOAndRetainsEveryBatch(t *testing.T) {
	l, f := prefixSetLease(t, 43)
	set := capturePrefixSet(t, l)
	plan := prefixSetBatch(t, set, 0)
	receiptBudgetMu.Lock()
	originalBudget := l.session.budget
	l.session.budget = l.session.retained + l.session.reserved + dataPrefixReceiptBudget + pendingReceiptBudget - 1
	receiptBudgetMu.Unlock()
	if out, err := l.submit(context.Background(), 11, "kv", "keys", plan); out != nil || !errors.Is(err, ErrBounds) {
		t.Fatal("unreserved original receipt reached the executor", err)
	}
	if f.writes.Load() != 0 || set.Snapshot().InFlight || len(plan.Snapshot().Edits) != 21 {
		t.Fatal("receipt budget rejection consumed the original plan")
	}
	receiptBudgetMu.Lock()
	l.session.budget = originalBudget
	receiptBudgetMu.Unlock()
	for index := 0; index < 3; index++ {
		if index > 0 {
			plan = prefixSetBatch(t, set, index)
		}
		if _, err := l.submit(context.Background(), 11, "kv", "keys", plan); err != nil {
			t.Fatal(err)
		}
		state := set.Snapshot()
		expected := dataPrefixSetBudget
		for _, receipt := range state.Receipts {
			expected += int64(len(receipt))
		}
		receiptBudgetMu.Lock()
		reserved := l.session.reserved
		receiptBudgetMu.Unlock()
		if reserved != expected {
			t.Fatal("original batch ledger escaped the session budget", reserved, expected)
		}
	}
	set.Close()
	receiptBudgetMu.Lock()
	reserved := l.session.reserved
	receiptBudgetMu.Unlock()
	if reserved != 0 {
		t.Fatal("complete batch ledger reservation leaked", reserved)
	}
}
