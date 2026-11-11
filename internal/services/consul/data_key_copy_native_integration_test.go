package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeCopyPreview(t *testing.T, l *dataLease, source *contract.RowSnapshot, destination string) contract.DataMutationPlan {
	t.Helper()
	p, err := l.previewKeyCopy(context.Background(), l.object.ProfileID, l.object.Catalog, l.object.Name, contract.DataKeyCopy{Source: source, Destination: destination})
	if err != nil {
		t.Fatal("original native conditional copy preview", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func nativeCopyPut(t *testing.T, c *Client, key string, value []byte) {
	t.Helper()
	r, err := KeyPutRequest(key, value, u64(math.MaxUint64), nil)
	if err != nil {
		t.Fatal(err)
	}
	nativeDo(t, c, r)
}

func TestConsulNativeDataKeyCopyOriginalBytesFlagsBothPayloadsSourcePreservationAndContinuedEdits(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	for _, mode := range []string{"values", "fingerprints"} {
		t.Run(mode, func(t *testing.T) {
			cfg.Prefix = nativeDataPrefix(t) + "//中文/"
			_, l := nativeDataSession(t, cfg, token)
			for _, empty := range []bool{false, true} {
				source, destination := cfg.Prefix+"source", cfg.Prefix+"destination//"
				if empty {
					source += "-empty"
					destination += "-empty"
				}
				value := bytes.Repeat([]byte{0, 255}, valueLimit/2)
				if empty {
					value = nil
				}
				nativeCopyPut(t, writer, source, value)
				original := nativeMutationOriginal(t, l, source, mode)
				before := nativeRead(t, writer, source)
				plan := nativeCopyPreview(t, l, original, destination)
				inspection := plan.Snapshot()
				inspection.Edits[0].Values["key"] = dataText("foreign/key")
				if plan.Snapshot().Edits[0].Values["key"].Text != destination || !strings.Contains(plan.Snapshot().NativeJSON, "BeforeImages") {
					t.Fatal("copy inspection changed original authority/evidence")
				}
				out := nativeMutationSubmit(t, l, plan)
				got := nativeRead(t, writer, destination)
				if !reflect.DeepEqual(before, nativeRead(t, writer, source)) || !bytes.Equal(got.Value, value) || got.Flags != math.MaxUint64 || got.CreateIndex != got.ModifyIndex || got.ModifyIndex <= nativeRead(t, writer, source).ModifyIndex || out.Outcomes[0].Kind != "copy" {
					t.Fatal("copy changed source or exact native target")
				}
				if _, err := l.submit(context.Background(), 91, "kv", "keys", plan); !errors.Is(err, contract.ErrKVPlanUsed) {
					t.Fatal("copy plan replayed", err)
				}
				// Original unchanged source observations remain usable; generated
				// destination references permit ordinary subsequent editing/deletion.
				nativeMutationSubmit(t, l, nativeMutationPreview(t, l, contract.DataEdit{Kind: "update", Original: out.Outcomes[0].Saved, Values: map[string]contract.DataValue{"flags": {Kind: contract.Integer, Text: "9007199254740993"}}}))
				second := nativeCopyPreview(t, l, original, destination+"second")
				nativeMutationSubmit(t, l, second)
				nativeMutationSubmit(t, l, nativeMutationPreview(t, l, contract.DataEdit{Kind: "delete", Original: nativeMutationOriginal(t, l, destination, mode)}))
				if !nativeMutationAbsent(t, writer, destination) || !bytes.Equal(nativeRead(t, writer, source).Value, value) {
					t.Fatal("target deletion altered original source")
				}
			}
		})
	}
}

func TestConsulNativeDataKeyCopyOriginalSourceDestinationAndLockConflictsNeverPartiallyWrite(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	for _, change := range []string{"source-before-preview", "destination-before-preview"} {
		t.Run(change, func(t *testing.T) {
			cfg.Prefix = nativeDataPrefix(t) + "//"
			source, destination := cfg.Prefix+"source", cfg.Prefix+"destination"
			nativeCopyPut(t, writer, source, []byte("original"))
			_, l := nativeDataSession(t, cfg, token)
			request := contract.DataKeyCopy{Source: nativeMutationOriginal(t, l, source, "fingerprints"), Destination: destination}
			key := source
			if change == "destination-before-preview" {
				key = destination
			}
			nativeCopyPut(t, writer, key, []byte("foreign"))
			p, err := l.previewKeyCopy(context.Background(), 91, "kv", "keys", request)
			if p != nil || !errors.Is(err, contract.ErrDataConflict) || l.uncertain {
				t.Fatal("changed observation published a copy preview", err)
			}
			if string(nativeRead(t, writer, key).Value) != "foreign" {
				t.Fatal("failed preview changed native key")
			}
			if change == "source-before-preview" && !nativeMutationAbsent(t, writer, destination) {
				t.Fatal("failed preview created destination")
			}
		})
	}
	for _, change := range []string{"value", "flags", "delete", "recreate", "destination", "acquire", "release", "destroy", "expire"} {
		t.Run(change, func(t *testing.T) {
			cfg.Prefix = nativeDataPrefix(t) + "//"
			source, destination := cfg.Prefix+"source", cfg.Prefix+"destination"
			nativeCopyPut(t, writer, source, []byte("original"))
			_, l := nativeDataSession(t, cfg, token)
			plan := nativeCopyPreview(t, l, nativeMutationOriginal(t, l, source, "fingerprints"), destination)
			switch change {
			case "value":
				nativeCopyPut(t, writer, source, []byte("foreign"))
			case "flags":
				r, _ := KeyPutRequest(source, []byte("original"), u64(9007199254740993), nil)
				nativeDo(t, writer, r)
			case "delete", "recreate":
				r, _ := KeyDeleteRequest(source, nil, false)
				nativeDo(t, writer, r)
				if change == "recreate" {
					nativeCopyPut(t, writer, source, []byte("original"))
				}
			case "destination":
				nativeCopyPut(t, writer, destination, []byte("foreign"))
			default:
				id := createSession(t, writer, "release")
				r, _ := nativeMutationLockRequest(source, []byte("original"), u64(math.MaxUint64), id, false)
				nativeDo(t, writer, r)
				if change == "release" {
					r, _ = nativeMutationLockRequest(source, []byte("original"), u64(math.MaxUint64), id, true)
					nativeDo(t, writer, r)
				}
				if change == "destroy" {
					nativeDo(t, writer, Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
				}
				if change == "expire" {
					deadline := time.Now().Add(30 * time.Second)
					for nativeRead(t, writer, source).Session != "" {
						if time.Now().After(deadline) {
							t.Fatal("original Session did not expire")
						}
						time.Sleep(100 * time.Millisecond)
					}
				}
			}
			var beforeSource Entry
			if change != "delete" {
				beforeSource = nativeRead(t, writer, source)
			}
			out, err := l.submit(context.Background(), 91, "kv", "keys", plan)
			if !errors.Is(err, contract.ErrDataConflict) || out == nil || out.CommitConfirmed || out.CommitUncertain || out.Incomplete || len(out.Outcomes) != 0 || l.uncertain {
				t.Fatal("copy conflict did not retain original known atomic failure", err)
			}
			if change == "delete" && !nativeMutationAbsent(t, writer, source) {
				t.Fatal("copy conflict recreated deleted original source")
			}
			if change != "delete" && !reflect.DeepEqual(beforeSource, nativeRead(t, writer, source)) {
				t.Fatal("copy conflict altered original source")
			}
			if change != "destination" && !nativeMutationAbsent(t, writer, destination) {
				t.Fatal("copy conflict partially created destination")
			}
			if change == "destination" && string(nativeRead(t, writer, destination).Value) != "foreign" {
				t.Fatal("copy conflict overwrote foreign destination")
			}
		})
	}
}

func TestConsulNativeDataKeyCopyProcessedOriginalReplyFaultsCancellationCloseAndNoReplay(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	for _, mode := range []string{"lost", "partial", "missing", "wrong-value", "wrong-key", "wrong-flags", "wrong-index", "wrong-source-value", "wrong-source-index", "wrong-source-session", "missing-source", "missing-field", "wrong-check", "missing-check", "extra-result", "http500", "http408", "cancel", "close", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			cfg.Prefix = nativeDataPrefix(t) + "//"
			source, destination := cfg.Prefix+"source", cfg.Prefix+"destination"
			nativeCopyPut(t, writer, source, []byte{0, 255})
			before := nativeRead(t, writer, source)
			proxy, calls, processed := nativeMutationRelay(t, cfg, token, mode)
			s, l := nativeDataSession(t, proxy, token)
			request := contract.DataKeyCopy{Source: nativeMutationOriginal(t, l, source, "fingerprints"), Destination: destination}
			plan := nativeCopyPreview(t, l, request.Source, destination)
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
			case <-time.After(20 * time.Second):
				t.Fatal("original copy not processed")
			}
			switch mode {
			case "cancel":
				cancel()
			case "close":
				l.Close()
			case "session-close":
				s.Close()
			}
			var got completion
			select {
			case got = <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("original copy executor did not acknowledge termination")
			}
			if !errors.Is(got.err, contract.ErrDataCommitUncertain) || got.out == nil || got.out.CommitConfirmed || !(got.out.CommitUncertain || got.out.Incomplete) || got.out.NativeJSON == "" {
				t.Fatal("lost copy receipt became successful", got.err)
			}
			if !reflect.DeepEqual(before, nativeRead(t, writer, source)) || !bytes.Equal(nativeRead(t, writer, destination).Value, []byte{0, 255}) {
				t.Fatal("fault did not follow actual copy; original source changed")
			}
			if mode != "close" && mode != "session-close" {
				unknown := l.write.recoveryOwned().Unknown
				if unknown == nil {
					t.Fatal("original unknown receipt missing")
				}
				if _, err := l.count(context.Background(), 91, "kv", "keys"); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(unknown, l.write.recoveryOwned().Unknown) {
					t.Fatal("auxiliary read replaced original unknown copy")
				}
				if _, err := l.previewKeyCopy(context.Background(), 91, "kv", "keys", request); !errors.Is(err, contract.ErrDataCommitUncertain) {
					t.Fatal("unknown copy authorized a new plan", err)
				}
				if _, err := l.submit(context.Background(), 91, "kv", "keys", plan); !errors.Is(err, contract.ErrDataCommitUncertain) {
					t.Fatal("original copy replayed", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("native copy retried", calls.Load())
			}
			l.Close()
			receiptBudgetMu.Lock()
			reserved := s.reserved
			receiptBudgetMu.Unlock()
			if reserved != 0 || l.write.last != nil || l.write.unknown != nil {
				t.Fatal("original copy close retained cache/budget")
			}
		})
	}
}

func TestConsulNativeDataKeyCopyForeignOriginalPlanRevocationAndLockedSource(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	cfg.Prefix = nativeDataPrefix(t) + "/"
	source, destination := cfg.Prefix+"source", cfg.Prefix+"destination"
	nativeCopyPut(t, writer, source, []byte{255, 0})
	s, l := nativeDataSession(t, cfg, token)
	request := contract.DataKeyCopy{Source: nativeMutationOriginal(t, l, source, "values"), Destination: destination}
	plan := nativeCopyPreview(t, l, request.Source, destination)
	_, peer := nativeDataSession(t, cfg, token)
	if _, err := peer.submit(context.Background(), 91, "kv", "keys", plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("original copy plan crossed document", err)
	}
	plan.Close()
	if _, err := l.submit(context.Background(), 91, "kv", "keys", plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("closed original copy plan accepted", err)
	}
	plan = nativeCopyPreview(t, l, request.Source, destination)
	newPlan := nativeCopyPreview(t, l, request.Source, destination)
	if _, err := l.submit(context.Background(), 91, "kv", "keys", plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("replaced original copy plan accepted", err)
	}
	newPlan.Close()
	id := createSession(t, writer, "release")
	r, _ := nativeMutationLockRequest(source, []byte{255, 0}, u64(math.MaxUint64), id, false)
	nativeDo(t, writer, r)
	locked := nativeMutationOriginal(t, l, source, "fingerprints")
	if _, err := l.previewKeyCopy(context.Background(), 91, "kv", "keys", contract.DataKeyCopy{Source: locked, Destination: destination}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("original locked source copied", err)
	}
	r, _ = nativeMutationLockRequest(source, []byte{255, 0}, u64(math.MaxUint64), id, true)
	nativeDo(t, writer, r)
	plan = nativeCopyPreview(t, l, nativeMutationOriginal(t, l, source, "fingerprints"), destination)
	s.Close()
	if _, err := l.submit(context.Background(), 91, "kv", "keys", plan); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original session copied", err)
	}
	if !nativeMutationAbsent(t, writer, destination) {
		t.Fatal("revoked copy wrote destination")
	}
}

func TestConsulNativeDataKeyCopyRestrictedOriginalTokenDenialRetainsCompleteReceipt(t *testing.T) {
	writer, cfg, _ := nativeClient(t)
	token := os.Getenv("NYADB_CONSUL_READ_TOKEN")
	if token == "" {
		return
	}
	cfg.Auth, cfg.Prefix = "token", "allowed/"+nativeDataPrefix(t)+"/"
	source, destination := cfg.Prefix+"source", cfg.Prefix+"destination"
	nativeCopyPut(t, writer, source, []byte{0, 255})
	_, l := nativeDataSession(t, cfg, token)
	plan, err := l.previewKeyCopy(context.Background(), 91, "kv", "keys", contract.DataKeyCopy{Source: nativeMutationOriginal(t, l, source, "fingerprints"), Destination: destination})
	r := l.write.LastResult()
	if err == nil || plan != nil || l.uncertain || r.HTTP == nil || r.HTTP.Status != 409 || r.HTTP.Txn == nil || r.WriteOutcomeUnknown || !strings.Contains(strings.ToLower(err.Error()), "permission") {
		t.Fatal("original copy ACL denial not retained", err)
	}
	if !nativeMutationAbsent(t, writer, destination) || !bytes.Equal(nativeRead(t, writer, source).Value, []byte{0, 255}) {
		t.Fatal("denied copy changed native data")
	}
}

func TestConsulNativeDataKeyCopyOriginalServerRequestLimitNeverSplitsOrReplays(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	// Prepare the complete source through regular KV PUT under the unchanged
	// native transaction limit; special-key preparation itself uses a txn.
	cfg.Prefix = nativeDataPrefix(t) + "/"
	source, destination := cfg.Prefix+"source", cfg.Prefix+"destination"
	value := bytes.Repeat([]byte{0, 255}, valueLimit/2)
	nativeCopyPut(t, writer, source, value)
	proxy, calls, _ := nativeMutationRelay(t, cfg, token, "passthrough")
	_, l := nativeDataSession(t, proxy, token)
	request := contract.DataKeyCopy{Source: nativeMutationOriginal(t, l, source, "fingerprints"), Destination: destination}
	plan := nativeCopyPreview(t, l, request.Source, destination)
	out, err := l.submit(context.Background(), 91, "kv", "keys", plan)
	if os.Getenv("NYADB_CONSUL_TXN_MAX_REQUEST") == "8388608" {
		if err != nil || out == nil || !out.CommitConfirmed || !bytes.Equal(nativeRead(t, writer, destination).Value, value) {
			t.Fatal("configured full copy failed", err)
		}
	} else {
		var receipt contract.KVResult
		if out == nil || json.Unmarshal([]byte(out.NativeJSON), &receipt) != nil || err == nil || out.CommitConfirmed || out.CommitUncertain || out.Incomplete || receipt.HTTP == nil || receipt.HTTP.Status != 413 || !receipt.HTTP.Acknowledged || !nativeMutationAbsent(t, writer, destination) {
			t.Fatal("native default limit changed/split/replayed copy", err)
		}
	}
	if !bytes.Equal(nativeRead(t, writer, source).Value, value) {
		t.Fatal("native copy limit changed original source")
	}
	if _, err := l.submit(context.Background(), 91, "kv", "keys", plan); err == nil {
		t.Fatal("original bounded copy plan replayed")
	}
	if calls.Load() != 1 {
		t.Fatal("bounded original copy was split or replayed", calls.Load())
	}
}
