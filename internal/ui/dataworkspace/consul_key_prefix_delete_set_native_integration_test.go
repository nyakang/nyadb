package dataworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func consulPrefixSetSeed(t *testing.T, w contract.KVRequestLease, prefix string, large bool) []string {
	t.Helper()
	keys := make([]string, 43)
	ops := make([]consul.TxnOperation, 43)
	for i := range keys {
		keys[i] = prefix + fmt.Sprintf("%04d//猫", i)
		if i == 42 {
			keys[i] += "\nlast"
		}
		value := []byte{}
		if large && i == 0 {
			value = bytes.Repeat([]byte{0, 255}, 256<<10)
		}
		flags := ^uint64(0)
		ops[i] = consul.TxnOperation{KV: &consul.TxnKV{Verb: "set", Key: keys[i], Value: &value, Flags: &flags}}
	}
	request, err := consul.TxnRequest(ops)
	if err != nil {
		t.Fatal(err)
	}
	result := consulMutationExecute(t, w, request)
	if result.HTTP == nil || !result.HTTP.Acknowledged || result.HTTP.Txn == nil || len(result.HTTP.Txn.Errors) != 0 {
		t.Fatal("original 43-key seed failed")
	}
	return keys
}
func waitConsulPrefixSet(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for d.prefixSet != nil && d.prefixSet.pending() || d.loading {
		s.Consume(ui.Services{})
		if time.Now().After(deadline) {
			t.Fatal("original batch acknowledgement stalled", d.edit.Err)
		}
		time.Sleep(time.Millisecond)
	}
}
func captureConsulPrefixSet(t *testing.T, s *State, d *Document, prefix string) {
	t.Helper()
	s.DeleteKeyPrefixSet(d)
	if d.prefixSet == nil {
		t.Fatal("public complete capture unavailable", d.edit.Err)
	}
	d.prefixSet.prefix = prefix
	s.captureKeyPrefixSet(ui.Services{}, d)
	waitConsulPrefixSet(t, s, d)
	if d.prefixSet == nil || !d.prefixSet.reviewOpen || len(d.prefixSet.initial.Candidates) != 43 {
		t.Fatal("complete native capture narrowed", d.edit.Err)
	}
}
func previewConsulPrefixSet(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.previewKeyPrefixSetBatch(ui.Services{}, d)
	waitConsulPrefixSet(t, s, d)
	if d.prefixSet == nil || !d.prefixSet.confirmOpen {
		t.Fatal("explicit native batch confirmation absent", d.edit.Err)
	}
}
func submitConsulPrefixSet(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.confirmKeyPrefixSetBatch(ui.Services{}, d)
	waitConsulPrefixSet(t, s, d)
}
func TestConsulNativePublicGridPrefixSetCompleteExplicitBatchesOriginalReceiptsAndLaterKeys(t *testing.T) {
	base, secret := consulGridProfile(t)
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprintf("large-%t", large), func(t *testing.T) {
			p := base
			p.ConsulOptions.Prefix = "native/public-prefix-set/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
			_, s, d, _, w := consulMutationState(t, p, secret)
			prefix := p.ConsulOptions.Prefix + "captured//"
			keys := consulPrefixSetSeed(t, w, prefix, large)
			consulCopyPut(t, w, p.ConsulOptions.Prefix+"aaa-visible", []byte("visible"))
			d.limit = 1
			consulMutationLoad(t, s, d, false)
			s.DeleteKeyPrefixSet(d)
			d.prefixSet.prefix = prefix
			stage := fmt.Sprintf("prefix-set-large-%t", large)
			captureConsulGrid(t, s, d, stage+"-form")
			s.captureKeyPrefixSet(ui.Services{}, d)
			waitConsulPrefixSet(t, s, d)
			if d.prefixSet == nil || len(d.prefixSet.initial.Candidates) != 43 || len(d.page.Rows) != 1 || len(d.edit.Drafts) != 0 {
				t.Fatal("capture replaced mature bounded page", d.edit.Err)
			}
			original := d.prefixSetObservation.JSON
			var captured struct{ Original contract.KVHTTPResult }
			if json.Unmarshal([]byte(original), &captured) != nil {
				t.Fatal("original capture JSON invalid")
			}
			entries := captured.Original.Entries
			if captured.Original.Txn != nil {
				entries = captured.Original.Txn.Results
			}
			if len(entries) != 43 || entries[0].Flags != ^uint64(0) || large && len(entries[0].Value) != 512<<10 {
				t.Fatal("original capture lost complete native bytes/flags")
			}
			captureConsulGrid(t, s, d, stage+"-review")
			later := prefix + "later"
			consulCopyPut(t, w, later, []byte("later"))
			for index, count := range []int{21, 21, 1} {
				previewConsulPrefixSet(t, s, d)
				if d.prefixSet.preview.KeyPrefixDeleteBatch.Index != index || len(d.prefixSet.preview.Edits) != count {
					t.Fatal("batch order/count changed")
				}
				if index == 0 {
					captureConsulGrid(t, s, d, stage+"-confirmation")
				}
				submitConsulPrefixSet(t, s, d)
				if len(d.prefixSetProgress.Batches) != index+1 || d.prefixSetProgress.ConfirmedBatches != index+1 || d.prefixSetObservation.JSON != original {
					t.Fatal("original ledger overwritten", d.edit.Err)
				}
				record := d.prefixSetProgress.Batches[index]
				var result contract.KVResult
				if record.Outcome != "confirmed" || record.Count != count || json.Unmarshal([]byte(record.NativeJSON), &result) != nil || result.HTTP == nil || !result.HTTP.Acknowledged || result.WriteOutcomeUnknown {
					t.Fatal("full corresponding receipt absent")
				}
				if index < 2 && (d.prefixSet == nil || !d.prefixSet.reviewOpen || d.prefixSet.pending()) {
					t.Fatal("batch ran automatically or refresh dropped capture")
				}
				for _, key := range keys[index*21 : min((index+1)*21, len(keys))] {
					consulPrefixAssertAbsent(t, w, key)
				}
			}
			if d.prefixSet != nil || d.HasDraft() || string(consulCopyEntry(t, w, later).Value) != "later" {
				t.Fatal("captured set included later key or failed to settle")
			}
			d.prefixSetLedger.Open = true
			captureConsulGrid(t, s, d, stage+"-ledger")
			d.prefixSetLedger.Open = false
			d.prefixSetSelectedReceipt.JSON = d.prefixSetProgress.Batches[0].NativeJSON
			d.prefixSetSelectedReceipt.Open = true
			captureConsulGrid(t, s, d, stage+"-receipt")
			d.prefixSetSelectedReceipt.Open = false
			d.prefixSetObservation.Open = true
			captureConsulGrid(t, s, d, stage+"-observation")
			d.prefixSetObservation.Open = false
		})
	}
}
func TestConsulNativePublicGridPrefixSetLateConflictsKeepEarlierCommitsAndUnstartedKeys(t *testing.T) {
	base, secret := consulGridProfile(t)
	for _, change := range []string{"value", "flags", "delete", "recreate", "lock"} {
		t.Run(change, func(t *testing.T) {
			p := base
			p.ConsulOptions.Prefix = "native/public-prefix-set-conflict/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
			_, s, d, _, w := consulMutationState(t, p, secret)
			keys := consulPrefixSetSeed(t, w, p.ConsulOptions.Prefix, false)
			consulMutationLoad(t, s, d, false)
			captureConsulPrefixSet(t, s, d, p.ConsulOptions.Prefix)
			previewConsulPrefixSet(t, s, d)
			submitConsulPrefixSet(t, s, d)
			first := d.prefixSetProgress.Batches[0].NativeJSON
			previewConsulPrefixSet(t, s, d)
			key := keys[21]
			switch change {
			case "value":
				consulCopyPut(t, w, key, []byte("changed"))
			case "flags":
				flags := uint64(1)
				r, err := consul.KeyPutRequest(key, []byte{}, &flags, nil)
				if err != nil {
					t.Fatal(err)
				}
				consulMutationExecute(t, w, r)
			case "delete":
				consulMutationExecute(t, w, prefixNativeDelete(t, key))
			case "recreate":
				consulMutationExecute(t, w, prefixNativeDelete(t, key))
				consulCopyPut(t, w, key, []byte{})
			case "lock":
				result := consulMutationExecute(t, w, consul.Request{Method: "PUT", Path: "/v1/session/create", Body: []byte(`{"LockDelay":"0s","Behavior":"release","TTL":"10s"}`)})
				id, value, flags := result.HTTP.SessionID, []byte{}, ^uint64(0)
				r, err := consul.TxnRequest([]consul.TxnOperation{{KV: &consul.TxnKV{Verb: "lock", Key: key, Session: &id, Value: &value, Flags: &flags}}})
				if err != nil {
					t.Fatal(err)
				}
				consulMutationExecute(t, w, r)
			}
			submitConsulPrefixSet(t, s, d)
			if d.prefixSet != nil || d.edit.Uncertain != nil || d.prefixSetProgress.ConfirmedBatches != 1 || len(d.prefixSetProgress.Batches) != 2 || d.prefixSetProgress.Batches[0].NativeJSON != first || d.prefixSetProgress.Batches[1].Outcome != "failed" || !d.prefixSetProgress.Stopped {
				t.Fatal("late conflict lost original partial progress", d.edit.Err)
			}
			for _, key := range keys[:21] {
				consulPrefixAssertAbsent(t, w, key)
			}
			for _, key := range keys[22:] {
				if consulCopyEntry(t, w, key).Key != key {
					t.Fatal("conflict partially deleted remaining batch")
				}
			}
			captureConsulGrid(t, s, d, "prefix-set-conflict-"+change+"-ledger")
		})
	}
}
func TestConsulNativePublicGridPrefixSetProcessedLateRepliesCancellationParkingCloseAndNoReplay(t *testing.T) {
	base, secret := consulGridProfile(t)
	for _, mode := range []string{"lost", "partial", "missing", "http500", "http408", "cancel", "close", "replace", "parked"} {
		t.Run(mode, func(t *testing.T) {
			p := base
			p.ConsulOptions.Prefix = "native/public-prefix-set-fault/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
			_, _, _, _, w := consulMutationState(t, p, secret)
			keys := consulPrefixSetSeed(t, w, p.ConsulOptions.Prefix, false)
			armed := &atomic.Bool{}
			fault := mode
			if mode == "parked" {
				fault = "lost"
			}
			proxy, calls, processed := consulMutationRelay(t, p, secret, fault, armed)
			manager, s, d, active, _ := consulMutationState(t, proxy, secret)
			consulMutationLoad(t, s, d, false)
			captureConsulPrefixSet(t, s, d, p.ConsulOptions.Prefix)
			previewConsulPrefixSet(t, s, d)
			submitConsulPrefixSet(t, s, d)
			first, observation := d.prefixSetProgress.Batches[0].NativeJSON, d.prefixSetObservation.JSON
			previewConsulPrefixSet(t, s, d)
			ack := make(chan struct{}, 1)
			originalSubmit := d.prefixSet.submit
			d.prefixSet.submit = func(ctx context.Context, id int64, db, table string, plan contract.DataMutationPlan) (*contract.DataEditResult, error) {
				out, err := originalSubmit(ctx, id, db, table, plan)
				ack <- struct{}{}
				return out, err
			}
			armed.Store(true)
			s.confirmKeyPrefixSetBatch(ui.Services{}, d)
			if mode == "parked" {
				active.ProfileID = p.ID + 1
			}
			select {
			case <-processed:
			case <-time.After(20 * time.Second):
				t.Fatal("late batch not actually committed")
			}
			switch mode {
			case "cancel":
				s.stopKeyPrefixSet(d)
				if !d.edit.Busy {
					t.Fatal("cancel settled before original executor acknowledgement")
				}
			case "close":
				s.Close(d)
			case "replace":
				manager.Disconnect(p.ID)
				fresh, err := manager.ConnectSession(context.Background(), proxy, secret)
				if err != nil {
					t.Fatal(err)
				}
				active.Generation = fresh.Generation()
			}
			select {
			case <-ack:
			case <-time.After(20 * time.Second):
				t.Fatal("original late executor acknowledgement lost")
			}
			for _, key := range keys[:42] {
				consulPrefixAssertAbsent(t, w, key)
			}
			if consulCopyEntry(t, w, keys[42]).Key != keys[42] {
				t.Fatal("unconfirmed next batch ran")
			}
			if mode == "close" {
				if !d.closed || d.prefixSet != nil || d.prefixSetObservation.JSON != "" {
					t.Fatal("closed original retained requests/caches")
				}
				return
			}
			if mode == "parked" {
				s.Consume(ui.Services{})
				if !d.edit.Busy || len(d.prefixSetProgress.Batches) != 1 {
					t.Fatal("foreign profile consumed parked late receipt")
				}
				active.ProfileID = p.ID
			}
			waitConsulPrefixSet(t, s, d)
			if d.edit.Uncertain == nil || !d.HasDraft() || !s.GuardLeave(d, nil) || d.prefixSetObservation.JSON != observation || len(d.prefixSetProgress.Batches) != 2 || d.prefixSetProgress.Batches[0].NativeJSON != first || d.prefixSetProgress.Batches[1].Outcome != "unknown" {
				t.Fatal("unknown late receipt erased earlier progress", d.edit.Err)
			}
			d.prefixSetLedger.Open = true
			captureConsulGrid(t, s, d, "prefix-set-unknown-"+mode+"-ledger")
			d.prefixSetLedger.Open = false
			consulMutationRead(t, w, keys[42])
			s.Save(ui.Services{}, d)
			s.DeleteKeyPrefixSet(d)
			s.previewKeyPrefixSetBatch(ui.Services{}, d)
			s.confirmKeyPrefixSetBatch(ui.Services{}, d)
			if calls.Load() != 1 || d.edit.Uncertain == nil || d.prefixSetProgress.ConfirmedBatches != 1 {
				t.Fatal("later read authorized original write replay")
			}
		})
	}
}
func TestConsulNativePublicGridPrefixSetOriginalInputParkingLateCaptureAndExplicitStop(t *testing.T) {
	p, secret := consulGridProfile(t)
	p.ConsulOptions.Prefix = "native/public-prefix-set-lifecycle/" + t.Name() + "/"
	manager, s, d, active, w := consulMutationState(t, p, secret)
	consulPrefixSetSeed(t, w, p.ConsulOptions.Prefix, false)
	consulMutationLoad(t, s, d, false)
	s.DeleteKeyPrefixSet(d)
	active.ProfileID = p.ID + 1
	s.Consume(ui.Services{})
	if d.prefixSet == nil {
		t.Fatal("parking lost original no-I/O form")
	}
	active.ProfileID = p.ID
	s.stopKeyPrefixSet(d)
	captureConsulPrefixSet(t, s, d, p.ConsulOptions.Prefix)
	previewConsulPrefixSet(t, s, d)
	s.cancelKeyPrefixSetConfirmation(d)
	if d.prefixSet.plan != nil || !d.prefixSet.reviewOpen {
		t.Fatal("cancelled batch retained authority")
	}
	previewConsulPrefixSet(t, s, d)
	submitConsulPrefixSet(t, s, d)
	s.stopKeyPrefixSet(d)
	if d.prefixSet != nil || d.HasDraft() || !d.prefixSetProgress.Stopped || d.prefixSetProgress.ConfirmedBatches != 1 || d.prefixSetObservation.JSON == "" {
		t.Fatal("explicit stop lost original progress")
	}
	capture := d.boundRuntime.CaptureKeyPrefixDelete
	entered, release := make(chan struct{}), make(chan struct{})
	d.boundRuntime.CaptureKeyPrefixDelete = func(ctx context.Context, id int64, db, table string, r contract.DataKeyPrefixDelete) (contract.DataKeyPrefixDeleteSet, error) {
		close(entered)
		<-release
		return capture(ctx, id, db, table, r)
	}
	s.DeleteKeyPrefixSet(d)
	d.prefixSet.prefix = p.ConsulOptions.Prefix
	s.captureKeyPrefixSet(ui.Services{}, d)
	<-entered
	fresh, err := manager.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	active.Generation = fresh.Generation()
	close(release)
	waitConsulPrefixSet(t, s, d)
	if d.prefixSet != nil || d.edit.Busy || d.edit.Err == "" {
		t.Fatal("late capture adopted new generation")
	}
	s.Close(d)
	if !manager.IsConnected(p.ID) {
		t.Fatal("old capture close damaged replacement")
	}
}
