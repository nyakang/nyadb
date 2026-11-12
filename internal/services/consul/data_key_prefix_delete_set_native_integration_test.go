package consul

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativePrefixSetCapture(t *testing.T, l *dataLease, prefix string) *dataKeyPrefixDeletionSet {
	t.Helper()
	raw, err := l.captureKeyPrefixDelete(context.Background(), l.object.ProfileID, l.object.Catalog, l.object.Name, contract.DataKeyPrefixDelete{Object: l.object, Prefix: prefix})
	if err != nil {
		t.Fatal("original complete prefix-set capture", err)
	}
	set := raw.(*dataKeyPrefixDeletionSet)
	t.Cleanup(func() { set.Close() })
	return set
}
func nativePrefixSetSeed(t *testing.T, writer *Client, prefix string, count int) []string {
	t.Helper()
	keys := make([]string, count)
	for i := range keys {
		keys[i] = prefix + fmt.Sprintf("%04d", i)
		nativeCopyPut(t, writer, keys[i], []byte{0, 255})
	}
	return keys
}

func TestConsulNativeDataKeyPrefixDeleteSetCompleteOriginalCaptureExplicitBatchesAndLaterKeys(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	cfg.Prefix = nativeDataPrefix(t) + "//中文/"
	_, l := nativeDataSession(t, cfg, token)
	keys := nativePrefixSetSeed(t, writer, cfg.Prefix, 43)
	value := bytes.Repeat([]byte{0, 255}, valueLimit/2)
	nativeCopyPut(t, writer, keys[0], value)
	set := nativePrefixSetCapture(t, l, cfg.Prefix)
	original := set.Snapshot()
	if len(original.Candidates) != 43 || original.BatchSize != 21 || !strings.Contains(original.NativeJSON, keys[42]) {
		t.Fatal("complete original set absent")
	}
	inspection := set.Snapshot()
	inspection.Candidates[0].Ref.Values[0][0] = '!'
	if set.Snapshot().Candidates[0].Ref.Values[0][0] == '!' {
		t.Fatal("inspection changed original signature")
	}
	later := cfg.Prefix + "later"
	nativeCopyPut(t, writer, later, []byte("later"))
	for index, count := range []int{21, 21, 1} {
		if index < 2 {
			if _, err := set.PreviewBatch(context.Background(), index+1); !errors.Is(err, ErrInvalid) {
				t.Fatal("later batch skipped confirmation", err)
			}
		}
		plan := prefixSetBatch(t, set, index)
		snapshot := plan.Snapshot()
		if len(snapshot.Edits) != count || snapshot.KeyPrefixDeleteBatch == nil || snapshot.KeyPrefixDeleteBatch.Total != 43 || snapshot.KeyPrefixDeleteBatch.Index != index {
			t.Fatal("batch lost complete original identity")
		}
		out := nativeMutationSubmit(t, l, plan)
		state := set.Snapshot()
		if out.Applied != count || state.NextBatch != index+1 || len(state.Receipts) != index+1 || state.Uncertain || state.InFlight {
			t.Fatal("batch advanced without corresponding original receipt")
		}
		expected := dataPrefixSetBudget
		for _, receipt := range state.Receipts {
			expected += int64(len(receipt))
		}
		receiptBudgetMu.Lock()
		reserved := l.session.reserved
		receiptBudgetMu.Unlock()
		if reserved != expected {
			t.Fatal("complete native batch ledger escaped the session budget", reserved, expected)
		}
		for _, key := range keys[index*21 : min((index+1)*21, len(keys))] {
			if !nativeMutationAbsent(t, writer, key) {
				t.Fatal("captured key survived", key)
			}
		}
		if _, err := set.PreviewBatch(context.Background(), index); !errors.Is(err, ErrInvalid) {
			t.Fatal("completed batch replayed", err)
		}
	}
	if string(nativeRead(t, writer, later).Value) != "later" || set.Snapshot().NativeJSON != original.NativeJSON || len(set.Snapshot().Candidates) != 43 {
		t.Fatal("batches recaptured later keys or lost original full observation")
	}
	if l.Runtime().CaptureKeyPrefixDelete == nil {
		t.Fatal("complete capture callback unavailable")
	}
	set.Close()
	receiptBudgetMu.Lock()
	reserved := l.session.reserved
	receiptBudgetMu.Unlock()
	if reserved != 0 {
		t.Fatal("native set reservation leaked", reserved)
	}
}

func TestConsulNativeDataKeyPrefixDeleteSetLaterConflictsPreserveOriginalPartialProgress(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	for _, change := range []string{"value", "flags", "delete", "recreate", "lock"} {
		t.Run(change, func(t *testing.T) {
			cfg.Prefix = nativeDataPrefix(t) + "/"
			_, l := nativeDataSession(t, cfg, token)
			keys := nativePrefixSetSeed(t, writer, cfg.Prefix, 43)
			set := nativePrefixSetCapture(t, l, cfg.Prefix)
			nativeMutationSubmit(t, l, prefixSetBatch(t, set, 0))
			second := prefixSetBatch(t, set, 1)
			changed := keys[41]
			switch change {
			case "value":
				nativeCopyPut(t, writer, changed, []byte("changed"))
			case "flags":
				request, _ := KeyPutRequest(changed, []byte{0, 255}, u64(7), nil)
				nativeDo(t, writer, request)
			case "delete", "recreate":
				request, _ := KeyDeleteRequest(changed, nil, false)
				nativeDo(t, writer, request)
				if change == "recreate" {
					nativeCopyPut(t, writer, changed, []byte{0, 255})
				}
			case "lock":
				session := createSession(t, writer, "release")
				request, _ := nativeMutationLockRequest(changed, []byte{0, 255}, nil, session, false)
				nativeDo(t, writer, request)
			}
			out, err := l.submit(context.Background(), 91, "kv", "keys", second)
			if !errors.Is(err, contract.ErrDataConflict) || out == nil || out.CommitConfirmed || out.CommitUncertain || out.Applied != 0 {
				t.Fatal("later batch conflict classified incorrectly", err)
			}
			state := set.Snapshot()
			if state.NextBatch != 1 || state.Uncertain || !state.Stopped || len(state.Receipts) != 2 {
				t.Fatal("later conflict hid original partial progress")
			}
			for _, key := range keys[:21] {
				if !nativeMutationAbsent(t, writer, key) {
					t.Fatal("later failure rolled back earlier committed batch")
				}
			}
			if !bytes.Equal(nativeRead(t, writer, keys[21]).Value, []byte{0, 255}) || !bytes.Equal(nativeRead(t, writer, keys[42]).Value, []byte{0, 255}) {
				t.Fatal("later conflict partially applied deletion")
			}
			if _, err = set.PreviewBatch(context.Background(), 1); !errors.Is(err, contract.ErrDataConflict) {
				t.Fatal("batch retry substituted a new observation", err)
			}
		})
	}
}

func TestConsulNativeDataKeyPrefixDeleteSetProcessedOriginalRepliesStopFurtherBatchesAndReplay(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	for _, mode := range []string{"lost", "partial", "missing", "http500", "http408", "cancel", "set-close", "lease-close"} {
		t.Run(mode, func(t *testing.T) {
			cfg.Prefix = nativeDataPrefix(t) + "/"
			keys := nativePrefixSetSeed(t, writer, cfg.Prefix, 43)
			fault := mode
			if mode == "set-close" || mode == "lease-close" {
				fault = "cancel"
			}
			proxy, calls, processed := nativeMutationRelay(t, cfg, token, fault)
			_, l := nativeDataSession(t, proxy, token)
			set := nativePrefixSetCapture(t, l, cfg.Prefix)
			initial := set.Snapshot()
			plan := prefixSetBatch(t, set, 0)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				out *contract.DataEditResult
				err error
			}
			done := make(chan completion, 1)
			go func() { out, err := l.submit(ctx, 91, "kv", "keys", plan); done <- completion{out, err} }()
			select {
			case <-processed:
			case <-time.After(10 * time.Second):
				t.Fatal("original batch not processed")
			}
			switch mode {
			case "cancel":
				cancel()
			case "set-close":
				set.Close()
			case "lease-close":
				l.Close()
			}
			var result completion
			select {
			case result = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original batch did not acknowledge")
			}
			if result.out == nil || result.out.CommitConfirmed || !(result.out.CommitUncertain || result.out.Incomplete) || !errors.Is(result.err, contract.ErrDataCommitUncertain) {
				t.Fatal("processed batch fault promoted success", result.err)
			}
			for _, key := range keys[:21] {
				if !nativeMutationAbsent(t, writer, key) {
					t.Fatal("fault preceded actual committed batch")
				}
			}
			if !bytes.Equal(nativeRead(t, writer, keys[21]).Value, []byte{0, 255}) {
				t.Fatal("fault started a later batch")
			}
			if mode != "set-close" && mode != "lease-close" {
				state := set.Snapshot()
				if !state.Uncertain || state.InFlight || state.NextBatch != 0 || len(state.Receipts) != 1 || state.NativeJSON != initial.NativeJSON || !reflect.DeepEqual(state.Candidates, initial.Candidates) {
					t.Fatal("unknown batch lost complete original set/receipt")
				}
				original := l.write.recoveryOwned().Unknown
				dataTestPage(t, l, contract.DataOptions{Limit: 50})
				if !reflect.DeepEqual(original, l.write.recoveryOwned().Unknown) {
					t.Fatal("later successful read replaced original unknown receipt")
				}
				for _, index := range []int{0, 1} {
					if _, err := set.PreviewBatch(context.Background(), index); !errors.Is(err, contract.ErrDataCommitUncertain) {
						t.Fatal("unknown batch authorized replay or a later batch", err)
					}
				}
			}
			if calls.Load() != 1 {
				t.Fatal("original batch retried or split")
			}
		})
	}
}

func TestConsulNativeDataKeyPrefixDeleteSetCaptureBoundsAndOriginalLockedCandidates(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	cfg.Prefix = nativeDataPrefix(t) + "/"
	_, l := nativeDataSession(t, cfg, token)
	keys := nativePrefixSetSeed(t, writer, cfg.Prefix, 1)
	session := createSession(t, writer, "release")
	lock, _ := nativeMutationLockRequest(keys[0], []byte{0, 255}, nil, session, false)
	nativeDo(t, writer, lock)
	if set, err := l.captureKeyPrefixDelete(context.Background(), 91, "kv", "keys", contract.DataKeyPrefixDelete{Object: l.object, Prefix: cfg.Prefix}); set != nil || !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("locked original capture admitted", err)
	}
	cfg.Prefix = nativeDataPrefix(t) + "/bytes/"
	_, l = nativeDataSession(t, cfg, token)
	value := bytes.Repeat([]byte{0, 255}, valueLimit/2)
	for i := 0; i < 8; i++ {
		nativeCopyPut(t, writer, cfg.Prefix+fmt.Sprintf("%02d", i), value)
	}
	if set, err := l.captureKeyPrefixDelete(context.Background(), 91, "kv", "keys", contract.DataKeyPrefixDelete{Object: l.object, Prefix: cfg.Prefix}); set != nil || !errors.Is(err, ErrBounds) {
		t.Fatal("oversized complete observation admitted", err)
	}
	cfg.Prefix = nativeDataPrefix(t) + "/count/"
	_, l = nativeDataSession(t, cfg, token)
	for start := 0; start <= dataPrefixSetCandidateLimit; start += 64 {
		ops := []TxnOperation{}
		for i := start; i < min(start+64, dataPrefixSetCandidateLimit+1); i++ {
			value := []byte{}
			ops = append(ops, TxnOperation{KV: &TxnKV{Verb: "set", Key: cfg.Prefix + fmt.Sprintf("%04d", i), Value: &value}})
		}
		request, err := TxnRequest(ops)
		if err != nil {
			t.Fatal(err)
		}
		nativeDo(t, writer, request)
	}
	if set, err := l.captureKeyPrefixDelete(context.Background(), 91, "kv", "keys", contract.DataKeyPrefixDelete{Object: l.object, Prefix: cfg.Prefix}); set != nil || !errors.Is(err, ErrBounds) {
		t.Fatal("over-count set was truncated instead of rejected", err)
	}
	receiptBudgetMu.Lock()
	reserved := l.session.reserved
	receiptBudgetMu.Unlock()
	if reserved != 0 {
		t.Fatal("rejected native capture leaked budget", reserved)
	}
}

func TestConsulNativeDataKeyPrefixDeleteSetACLFilteredCaptureAndDeniedOriginalBatch(t *testing.T) {
	writer, cfg, _ := nativeClient(t)
	token := os.Getenv("NYADB_CONSUL_READ_TOKEN")
	if token == "" {
		return
	}
	t.Run("filtered", func(t *testing.T) {
		visible, hidden := "allowed/"+nativeDataPrefix(t), "allowed-hidden/"+nativeDataPrefix(t)
		nativeCopyPut(t, writer, visible, []byte("visible"))
		nativeCopyPut(t, writer, hidden, []byte("hidden"))
		cfg.Auth, cfg.Prefix = "token", "allowed"
		_, l := nativeDataSession(t, cfg, token)
		set, err := l.captureKeyPrefixDelete(context.Background(), 91, "kv", "keys", contract.DataKeyPrefixDelete{Object: l.object, Prefix: "allowed"})
		last := l.write.LastResult()
		if set != nil || !errors.Is(err, contract.ErrUnsupported) || last.HTTP == nil || last.HTTP.Metadata.ACLFiltered == nil || !*last.HTTP.Metadata.ACLFiltered || l.uncertain {
			t.Fatal("filtered set treated as complete", err)
		}
	})
	t.Run("denied-submit", func(t *testing.T) {
		cfg.Auth, cfg.Prefix = "token", "allowed/"+nativeDataPrefix(t)+"/"
		keys := nativePrefixSetSeed(t, writer, cfg.Prefix, 43)
		_, l := nativeDataSession(t, cfg, token)
		set := nativePrefixSetCapture(t, l, cfg.Prefix)
		plan := prefixSetBatch(t, set, 0)
		out, err := l.submit(context.Background(), 91, "kv", "keys", plan)
		state := set.Snapshot()
		if err == nil || out == nil || out.CommitConfirmed || out.CommitUncertain || out.Applied != 0 || state.NextBatch != 0 || state.Uncertain || !state.Stopped || len(state.Receipts) != 1 || !strings.Contains(strings.ToLower(err.Error()), "permission") {
			t.Fatal("denied original batch lost native evidence", err)
		}
		if _, err := set.PreviewBatch(context.Background(), 0); err == nil || !strings.Contains(strings.ToLower(err.Error()), "permission") || len(set.Snapshot().Receipts) != 1 {
			t.Fatal("known denied capture reacquired batch authority", err)
		}
		for _, key := range keys {
			if !bytes.Equal(nativeRead(t, writer, key).Value, []byte{0, 255}) {
				t.Fatal("denied batch changed a source key")
			}
		}
	})
}
