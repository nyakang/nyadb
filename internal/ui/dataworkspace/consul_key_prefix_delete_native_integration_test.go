package dataworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func prefixNativeDelete(t *testing.T, key string) consul.Request {
	t.Helper()
	r, err := consul.KeyDeleteRequest(key, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func consulPrefixAssertAbsent(t *testing.T, writer contract.KVRequestLease, key string) {
	t.Helper()
	request, err := consul.TxnRequest([]consul.TxnOperation{{KV: &consul.TxnKV{Verb: "check-not-exists", Key: key}}})
	if err != nil {
		t.Fatal(err)
	}
	result := consulMutationExecute(t, writer, request)
	if result.HTTP.Txn == nil || !result.HTTP.Acknowledged || !result.HTTP.EOF || len(result.HTTP.Txn.Errors) != 0 {
		t.Fatal("original explicit absence receipt missing")
	}
}

func waitConsulPrefix(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for d.prefixDelete != nil && (d.prefixDelete.previewResults != nil || d.prefixDelete.results != nil) || d.loading {
		s.Consume(ui.Services{})
		if time.Now().After(deadline) {
			t.Fatal("original prefix executor stalled", d.edit.Err)
		}
		time.Sleep(time.Millisecond)
	}
}

func consulPrefixPreview(t *testing.T, s *State, d *Document, prefix string) {
	t.Helper()
	s.DeleteKeyPrefix(d)
	if d.prefixDelete == nil {
		t.Fatal("public prefix input unavailable", d.edit.Err)
	}
	d.prefixDelete.prefix = prefix
	s.previewKeyPrefixDelete(ui.Services{}, d)
	waitConsulPrefix(t, s, d)
	if d.prefixDelete == nil || !d.prefixDelete.previewOpen || d.edit.Err != "" {
		t.Fatal("public prefix confirmation unavailable", d.edit.Err)
	}
}

func TestConsulNativePublicGridKeyPrefixDeleteCompleteOriginalCandidatesOutsidePageAndLaterKeys(t *testing.T) {
	base, secret := consulGridProfile(t)
	for _, large := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "large"}[large], func(t *testing.T) {
			p := base
			p.ConsulOptions.Prefix = "native/public-prefix//" + strings.ReplaceAll(t.Name(), "/", "-") + "/中文/"
			_, s, d, active, w := consulMutationState(t, p, secret)
			prefix := p.ConsulOptions.Prefix + "candidate//"
			value := []byte{}
			if large {
				value = bytes.Repeat([]byte{0, 255}, 256<<10)
			}
			keys := []string{prefix, prefix + "猫"}
			for _, key := range keys {
				consulCopyPut(t, w, key, value)
			}
			consulCopyPut(t, w, p.ConsulOptions.Prefix+"aaa-visible", []byte("visible"))
			d.limit = 1
			consulMutationLoad(t, s, d, false)
			if len(d.page.Rows) != 1 {
				t.Fatal("bounded visible fixture missing")
			}
			before := []contract.KVHTTPEntry{consulCopyEntry(t, w, keys[0]), consulCopyEntry(t, w, keys[1])}
			s.DeleteKeyPrefix(d)
			if d.prefixDelete == nil || !d.HasDraft() {
				t.Fatal("prefix form not guarded")
			}
			d.prefixDelete.prefix = prefix
			stage := "prefix-delete-" + map[bool]string{false: "empty", true: "large"}[large]
			captureConsulGrid(t, s, d, stage+"-form")
			s.previewKeyPrefixDelete(ui.Services{}, d)
			waitConsulPrefix(t, s, d)
			if d.prefixDelete == nil || !d.prefixDelete.previewOpen || len(d.prefixDelete.snapshot.Edits) != 2 || len(d.edit.Drafts) != 0 {
				t.Fatal("prefix preview depended on visible page", d.edit.Err)
			}
			var native struct {
				BeforeImages []contract.KVHTTPEntry
				Scope        json.RawMessage
			}
			if json.Unmarshal([]byte(d.prefixDelete.snapshot.NativeJSON), &native) != nil || !reflect.DeepEqual(before, native.BeforeImages) || len(native.Scope) == 0 {
				t.Fatal("original complete values/flags/scope omitted")
			}
			for _, b := range native.BeforeImages {
				if b.Flags != math.MaxUint64 || !bytes.Equal(b.Value, value) {
					t.Fatal("prefix capture lost original bytes/Flags")
				}
			}
			captureConsulGrid(t, s, d, stage+"-confirmation")
			later := prefix + "later"
			consulCopyPut(t, w, later, []byte("preserve later"))
			s.confirmKeyPrefixDelete(ui.Services{}, d)
			m := d.prefixDelete
			active.ProfileID = p.ID + 1
			select {
			case completion := <-m.results:
				if completion.Err != nil {
					t.Fatal(completion.Err)
				}
				ch := make(chan mysqlEditResult, 1)
				ch <- completion
				m.results = ch
			case <-time.After(20 * time.Second):
				t.Fatal("prefix executor stalled")
			}
			s.Consume(ui.Services{})
			if !d.edit.Busy || d.nativeReceipt.JSON != "" {
				t.Fatal("foreign profile consumed original prefix receipt")
			}
			active.ProfileID = p.ID
			waitConsulPrefix(t, s, d)
			if d.HasDraft() || d.prefixDelete != nil || d.edit.Uncertain != nil || d.nativeReceipt.JSON == "" || d.prefixEvidence.JSON == "" {
				t.Fatal("prefix complete receipt failed to settle", d.edit.Err)
			}
			for _, key := range keys {
				consulPrefixAssertAbsent(t, w, key)
			}
			if string(consulCopyEntry(t, w, later).Value) != "preserve later" || string(consulCopyEntry(t, w, p.ConsulOptions.Prefix+"aaa-visible").Value) != "visible" {
				t.Fatal("prefix deletion escaped original captured set")
			}
			d.prefixEvidence.Open = true
			captureConsulGrid(t, s, d, stage+"-observation")
			d.prefixEvidence.Open = false
			d.nativeReceipt.Open = true
			captureConsulGrid(t, s, d, stage+"-saved")
			d.nativeReceipt.Open = false
			captureConsulGrid(t, s, d, stage+"-later-preserved")
		})
	}
}

func TestConsulNativePublicGridKeyPrefixDeleteConflictsBoundsAndLocksRetainOriginalEvidence(t *testing.T) {
	base, secret := consulGridProfile(t)
	for _, change := range []string{"changed", "deleted", "too-many"} {
		t.Run(change, func(t *testing.T) {
			p := base
			p.ConsulOptions.Prefix = "native/public-prefix-conflict/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
			_, s, d, _, w := consulMutationState(t, p, secret)
			key := p.ConsulOptions.Prefix + "candidate"
			consulCopyPut(t, w, key, []byte{0, 255})
			consulMutationLoad(t, s, d, false)
			if change == "too-many" {
				for i := 0; i < 22; i++ {
					consulCopyPut(t, w, key+string(rune('a'+i)), []byte("bound"))
				}
				s.DeleteKeyPrefix(d)
				d.prefixDelete.prefix = p.ConsulOptions.Prefix
				s.previewKeyPrefixDelete(ui.Services{}, d)
				waitConsulPrefix(t, s, d)
				if d.prefixDelete != nil || d.edit.Err == "" {
					t.Fatal("over-bound candidate set was silently split")
				}
			} else {
				consulPrefixPreview(t, s, d, p.ConsulOptions.Prefix)
				original := d.prefixEvidence.JSON
				if change == "changed" {
					consulCopyPut(t, w, key, []byte("foreign"))
				} else {
					consulMutationExecute(t, w, prefixNativeDelete(t, key))
				}
				s.confirmKeyPrefixDelete(ui.Services{}, d)
				waitConsulPrefix(t, s, d)
				if d.prefixDelete == nil || !d.prefixDelete.open || d.edit.Uncertain != nil || d.edit.Err == "" || d.prefixEvidence.JSON != original {
					t.Fatal("known prefix conflict lost original input/evidence", d.edit.Err)
				}
				if change == "changed" && string(consulCopyEntry(t, w, key).Value) != "foreign" {
					t.Fatal("conflict deleted changed candidate")
				}
				s.cancelKeyPrefixDelete(d)
			}
			captureConsulGrid(t, s, d, "prefix-delete-conflict-"+change)
		})
	}
}

func TestConsulNativePublicGridKeyPrefixDeleteProcessedReplyParkingGenerationAndNoReplay(t *testing.T) {
	base, secret := consulGridProfile(t)
	for _, mode := range []string{"lost", "partial", "missing", "http500", "http408", "cancel", "close", "replace", "parked"} {
		t.Run(mode, func(t *testing.T) {
			p := base
			p.ConsulOptions.Prefix = "native/public-prefix-fault/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
			_, _, _, _, observer := consulMutationState(t, p, secret)
			key := p.ConsulOptions.Prefix + "candidate"
			consulCopyPut(t, observer, key, []byte{0, 255})
			armed := &atomic.Bool{}
			fault := mode
			if mode == "parked" {
				fault = "lost"
			}
			proxy, calls, processed := consulMutationRelay(t, p, secret, fault, armed)
			manager, s, d, active, _ := consulMutationState(t, proxy, secret)
			consulMutationLoad(t, s, d, false)
			consulPrefixPreview(t, s, d, p.ConsulOptions.Prefix)
			original := d.prefixEvidence.JSON
			armed.Store(true)
			s.confirmKeyPrefixDelete(ui.Services{}, d)
			m := d.prefixDelete
			results := m.results
			if mode == "parked" {
				active.ProfileID = p.ID + 1
			}
			select {
			case <-processed:
			case <-time.After(20 * time.Second):
				t.Fatal("original prefix write not processed")
			}
			switch mode {
			case "cancel":
				s.stopMySQLDataEdit(d)
				if !d.edit.Busy {
					t.Fatal("cancel settled before acknowledgement")
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
			var completion mysqlEditResult
			select {
			case completion = <-results:
			case <-time.After(20 * time.Second):
				t.Fatal("original prefix acknowledgement stalled")
			}
			if completion.Saved == nil || completion.Saved.CommitConfirmed || !(completion.Saved.CommitUncertain || completion.Saved.Incomplete) || completion.Saved.NativeJSON == "" {
				t.Fatal("processed prefix fault promoted success", completion.Err)
			}
			consulPrefixAssertAbsent(t, observer, key)
			if mode != "close" {
				ch := make(chan mysqlEditResult, 1)
				ch <- completion
				m.results = ch
				if mode == "parked" {
					s.Consume(ui.Services{})
					if !d.edit.Busy || d.nativeReceipt.JSON != "" {
						t.Fatal("parked prefix consumed by foreign profile")
					}
					active.ProfileID = p.ID
				}
				waitConsulPrefix(t, s, d)
				if !d.HasDraft() || d.edit.Uncertain == nil || d.prefixEvidence.JSON != original || d.nativeReceipt.JSON == "" {
					t.Fatal("unknown prefix lost original complete evidence")
				}
				d.nativeReceipt.Open = true
				captureConsulGrid(t, s, d, "prefix-delete-unknown-"+mode)
				d.nativeReceipt.Open = false
				consulMutationRead(t, observer, key)
				s.Save(ui.Services{}, d)
				s.DeleteKeyPrefix(d)
				s.confirmKeyPrefixDelete(ui.Services{}, d)
				if calls.Load() != 1 || d.prefixEvidence.JSON != original || d.edit.Uncertain == nil {
					t.Fatal("later read authorized prefix replay")
				}
				d.prefixEvidence.Open = true
				captureConsulGrid(t, s, d, "prefix-delete-after-read-"+mode)
				d.prefixEvidence.Open = false
			}
		})
	}
}

func TestConsulNativePublicGridKeyPrefixDeleteOriginalInputParkingCancelCloseAndLatePreview(t *testing.T) {
	p, secret := consulGridProfile(t)
	p.ConsulOptions.Prefix = "native/public-prefix-input/" + t.Name() + "/"
	m, s, d, active, w := consulMutationState(t, p, secret)
	consulCopyPut(t, w, p.ConsulOptions.Prefix+"key", []byte("original"))
	consulMutationLoad(t, s, d, false)
	s.DeleteKeyPrefix(d)
	d.prefixDelete.prefix = p.ConsulOptions.Prefix
	active.ProfileID = p.ID + 1
	s.Consume(ui.Services{})
	if d.prefixDelete == nil {
		t.Fatal("parking dropped original prefix input")
	}
	active.ProfileID = p.ID
	s.cancelKeyPrefixDelete(d)
	if d.HasDraft() {
		t.Fatal("cancel retained prefix input")
	}
	preview := d.boundRuntime.PreviewKeyPrefixDelete
	entered, release := make(chan struct{}), make(chan struct{})
	d.boundRuntime.PreviewKeyPrefixDelete = func(ctx context.Context, id int64, db, table string, r contract.DataKeyPrefixDelete) (contract.DataMutationPlan, error) {
		close(entered)
		<-release
		return preview(ctx, id, db, table, r)
	}
	s.DeleteKeyPrefix(d)
	d.prefixDelete.prefix = p.ConsulOptions.Prefix
	s.previewKeyPrefixDelete(ui.Services{}, d)
	<-entered
	fresh, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	active.Generation = fresh.Generation()
	close(release)
	waitConsulPrefix(t, s, d)
	if d.prefixDelete != nil || d.edit.Busy || d.edit.Err == "" {
		t.Fatal("late prefix preview adopted replacement session")
	}
	s.Close(d)
	if d.prefixEvidence.JSON != "" || d.prefixDelete != nil || !m.IsConnected(p.ID) {
		t.Fatal("old prefix close damaged replacement or retained caches")
	}
}
