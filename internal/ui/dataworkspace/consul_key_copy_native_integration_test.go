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

func consulCopyEntry(t *testing.T, w contract.KVRequestLease, key string) contract.KVHTTPEntry {
	t.Helper()
	r := consulMutationRead(t, w, key)
	if r.HTTP == nil {
		t.Fatal("original source receipt missing")
	}
	entries := r.HTTP.Entries
	if r.HTTP.Txn != nil {
		entries = r.HTTP.Txn.Results
	}
	if len(entries) != 1 {
		t.Fatal("original source/target entry missing", key)
	}
	return entries[0]
}
func consulCopyPut(t *testing.T, w contract.KVRequestLease, key string, value []byte) {
	t.Helper()
	flags := uint64(math.MaxUint64)
	r, err := consul.KeyPutRequest(key, value, &flags, nil)
	if err != nil {
		t.Fatal(err)
	}
	consulMutationExecute(t, w, r)
}
func consulCopyRow(t *testing.T, d *Document, key string) int {
	t.Helper()
	for row, cells := range d.page.Rows {
		if cells[0].Value != nil && cells[0].Value.Text == key {
			return row
		}
	}
	t.Fatal("original copied key not present", key)
	return -1
}
func consulCopyValuesSnapshot(t *testing.T, s *State, d *Document, row int) {
	t.Helper()
	full, err := s.runtimeFor(d).RowReader(context.Background(), d.profileID, d.database, d.table, *d.page.RowRefs[row])
	if err != nil {
		t.Fatal(err)
	}
	original := cloneSnapshot(full.Snapshot)
	original.Mode = "values"
	original.Fingerprints = nil
	original.Original = nil
	for _, name := range original.Columns {
		v := full.Values[name]
		c := contract.Cell{Kind: v.Kind, Text: v.Text, Bytes: bytes.Clone(v.Bytes)}
		if v.Null {
			c.Kind = contract.Null
		}
		if v.Binary {
			c.Kind = contract.Binary
		}
		original.Original = append(original.Original, c)
	}
	d.page.RowSnapshots[row] = original
}
func consulCopyPreview(t *testing.T, s *State, d *Document, row int, destination string) {
	t.Helper()
	s.beginDataKeyCopy(d, row)
	if d.keyCopy == nil {
		t.Fatal("public original copy target form unavailable")
	}
	d.keyCopy.destination = destination
	s.stageDataKeyCopy(ui.Services{}, d)
	waitConsulMutation(t, s, d)
	if !d.mutation.open || d.mutation.plan == nil || d.edit.Err != "" {
		t.Fatal("public original copy confirmation missing", d.edit.Err)
	}
}

func TestConsulNativePublicGridKeyCopyBothPayloadsCompleteSourceDestinationConfirmationsAndContinuedEdits(t *testing.T) {
	base, secret := consulGridProfile(t)
	for _, mode := range []string{"values", "fingerprints"} {
		t.Run(mode, func(t *testing.T) {
			p := base
			p.ConsulOptions.Prefix = "native/public-copy//" + strings.ReplaceAll(t.Name(), "/", "-") + "/中文/"
			_, s, d, active, w := consulMutationState(t, p, secret)
			for _, empty := range []bool{false, true} {
				source, destination := p.ConsulOptions.Prefix+strings.Repeat("long", 300), p.ConsulOptions.Prefix+"target//"
				if empty {
					source = p.ConsulOptions.Prefix + "empty"
					destination += "empty"
				}
				value := bytes.Repeat([]byte{0, 255}, 256<<10)
				if empty {
					value = nil
				}
				consulCopyPut(t, w, source, value)
				consulMutationLoad(t, s, d, true)
				row := 0
				if empty {
					row = consulCopyRow(t, d, source)
				}
				if mode == "values" {
					consulCopyValuesSnapshot(t, s, d, row)
				}
				before := consulCopyEntry(t, w, source)
				s.beginDataKeyCopy(d, row)
				if d.keyCopy == nil || !empty && (!d.keyCopy.sourceTruncated || d.keyCopy.destination != "") {
					t.Fatal("truncated key became copy authority or default destination")
				}
				d.keyCopy.destination = destination
				stage := "key-copy-" + mode
				if empty {
					stage += "-empty"
				}
				captureConsulGrid(t, s, d, stage+"-form")
				s.stageDataKeyCopy(ui.Services{}, d)
				waitConsulMutation(t, s, d)
				if !d.mutation.open || d.edit.Err != "" {
					t.Fatal("original key copy preview rejected", mode, d.edit.Err)
				}
				if len(d.mutation.snapshot.Edits) != 1 || d.mutation.snapshot.Edits[0].Kind != "copy" || d.mutation.snapshot.Edits[0].Original.Mode != mode || len(d.mutation.snapshot.Edits[0].Values) != 1 {
					t.Fatal("copy was downgraded or authored preview bytes")
				}
				var native struct{ BeforeImages []contract.KVHTTPEntry }
				if json.Unmarshal([]byte(d.mutation.snapshot.NativeJSON), &native) != nil || len(native.BeforeImages) != 1 || !reflect.DeepEqual(native.BeforeImages[0], before) {
					t.Fatal("complete original source was lost from confirmation")
				}
				captureConsulGrid(t, s, d, stage+"-confirmation")
				consulMutationCopy(t, s, d, true)
				s.cancelDataMutation(d)
				if !d.HasDraft() {
					t.Fatal("copy cancellation discarded conditional draft")
				}
				consulMutationPreview(t, s, d)
				s.confirmDataMutation(ui.Services{}, d)
				priorReceipt := d.nativeReceipt.JSON
				active.ProfileID = p.ID + 1
				select {
				case completion := <-d.edit.Results:
					if completion.Err != nil || completion.Saved == nil || !completion.Saved.CommitConfirmed {
						t.Fatal("original copy executor failed", completion.Err)
					}
					channel := make(chan mysqlEditResult, 1)
					channel <- completion
					d.edit.Results = channel
				case <-time.After(20 * time.Second):
					t.Fatal("original copy did not acknowledge")
				}
				s.Consume(ui.Services{})
				if !d.HasDraft() || d.nativeReceipt.JSON != priorReceipt {
					t.Fatal("parked copy result consumed by foreign profile")
				}
				active.ProfileID = p.ID
				waitConsulMutation(t, s, d)
				if d.HasDraft() || d.edit.Uncertain != nil || d.nativeReceipt.JSON == "" || !reflect.DeepEqual(before, consulCopyEntry(t, w, source)) {
					t.Fatal("confirmed copy changed original source", d.edit.Err)
				}
				target := consulCopyEntry(t, w, destination)
				if !bytes.Equal(target.Value, value) || target.Flags != math.MaxUint64 || target.CreateIndex != target.ModifyIndex || target.ModifyIndex <= before.ModifyIndex {
					t.Fatal("copied target lost native values or identity")
				}
				row = consulCopyRow(t, d, destination)
				if !mysqlDataCanEdit(d, row, 2) {
					t.Fatal("generated copy target stayed read-only")
				}
				captureConsulGrid(t, s, d, stage+"-saved")
				consulMutationCopy(t, s, d, false)
				consulMutationExport(t, s, d)
				consulMutationEdit(t, s, d, row, "flags", contract.DataValue{Kind: contract.Integer, Text: "9007199254740993"})
				consulMutationPreview(t, s, d)
				s.confirmDataMutation(ui.Services{}, d)
				waitConsulMutation(t, s, d)
				row = consulCopyRow(t, d, destination)
				d.selection.Clear()
				d.selection.Add(row)
				s.deleteMySQLDataRecords(ui.Services{}, d, row)
				consulMutationPreview(t, s, d)
				s.confirmDataMutation(ui.Services{}, d)
				waitConsulMutation(t, s, d)
				if !reflect.DeepEqual(before, consulCopyEntry(t, w, source)) {
					t.Fatal("editing/deleting copied target changed source")
				}
				captureConsulGrid(t, s, d, stage+"-source-preserved")
			}
		})
	}
}

func TestConsulNativePublicGridKeyCopySourceDestinationLockAndPreviewConflictsRetainOriginalDrafts(t *testing.T) {
	base, secret := consulGridProfile(t)
	for _, change := range []string{"source-before-preview", "destination-before-preview", "source-after-preview", "destination-after-preview", "locked"} {
		t.Run(change, func(t *testing.T) {
			p := base
			p.ConsulOptions.Prefix = "native/public-copy-conflict/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
			_, s, d, _, w := consulMutationState(t, p, secret)
			source, destination := p.ConsulOptions.Prefix+"source", p.ConsulOptions.Prefix+"target"
			consulCopyPut(t, w, source, []byte{0, 255})
			consulMutationLoad(t, s, d, false)
			if change == "locked" {
				create := consul.Request{Method: "PUT", Path: "/v1/session/create", Body: []byte(`{"Behavior":"release","TTL":"10s","LockDelay":"0s"}`)}
				receipt := consulMutationExecute(t, w, create)
				id := receipt.HTTP.SessionID
				v := []byte{0, 255}
				flags := uint64(math.MaxUint64)
				r, err := consul.TxnRequest([]consul.TxnOperation{{KV: &consul.TxnKV{Verb: "lock", Key: source, Value: &v, Flags: &flags, Session: &id}}})
				if err != nil {
					t.Fatal(err)
				}
				consulMutationExecute(t, w, r)
				consulMutationLoad(t, s, d, true)
				if s.canCopyDataKey(d, 0) {
					t.Fatal("original Session-locked source was copyable")
				}
				captureConsulGrid(t, s, d, "key-copy-conflict-locked")
				return
			}
			s.beginDataKeyCopy(d, 0)
			d.keyCopy.destination = destination
			key := source
			if strings.HasPrefix(change, "destination") {
				key = destination
			}
			if strings.HasSuffix(change, "before-preview") {
				consulCopyPut(t, w, key, []byte("foreign"))
			}
			s.stageDataKeyCopy(ui.Services{}, d)
			waitConsulMutation(t, s, d)
			if strings.HasSuffix(change, "after-preview") {
				if !d.mutation.open {
					t.Fatal("copy preview unavailable", d.edit.Err)
				}
				consulCopyPut(t, w, key, []byte("foreign"))
				s.confirmDataMutation(ui.Services{}, d)
				waitConsulMutation(t, s, d)
			}
			if !d.HasDraft() || d.edit.Uncertain != nil || d.edit.Err == "" || string(consulCopyEntry(t, w, key).Value) != "foreign" {
				t.Fatal("copy conflict lost known atomic failure/draft", d.edit.Err)
			}
			if key == source {
				r := consulMutationRead(t, w, destination)
				if r.HTTP != nil && r.HTTP.Txn != nil && len(r.HTTP.Txn.Results) > 0 {
					t.Fatal("copy conflict partially created target")
				}
			}
			captureConsulGrid(t, s, d, "key-copy-conflict-"+change)
			s.discardMySQLData(d)
			if len(d.page.Rows) != 1 || d.HasDraft() {
				t.Fatal("copy conflict discard retained target draft")
			}
		})
	}
}

func TestConsulNativePublicGridKeyCopyProcessedReplyParkingGenerationLeaveAndNoReplay(t *testing.T) {
	base, secret := consulGridProfile(t)
	for _, mode := range []string{"lost", "partial", "missing", "http500", "http408", "cancel", "close", "replace", "parked"} {
		t.Run(mode, func(t *testing.T) {
			p := base
			p.ConsulOptions.Prefix = "native/public-copy-fault/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
			_, _, _, _, observer := consulMutationState(t, p, secret)
			source, destination := p.ConsulOptions.Prefix+"source", p.ConsulOptions.Prefix+"target"
			consulCopyPut(t, observer, source, []byte{0, 255})
			before := consulCopyEntry(t, observer, source)
			armed := &atomic.Bool{}
			fault := mode
			if mode == "parked" {
				fault = "lost"
			}
			proxy, calls, processed := consulMutationRelay(t, p, secret, fault, armed)
			m, s, d, active, _ := consulMutationState(t, proxy, secret)
			consulMutationLoad(t, s, d, false)
			consulCopyPreview(t, s, d, 0, destination)
			armed.Store(true)
			s.confirmDataMutation(ui.Services{}, d)
			results := d.edit.Results
			if mode == "parked" {
				active.ProfileID = p.ID + 1
			}
			select {
			case <-processed:
			case <-time.After(20 * time.Second):
				t.Fatal("original copy was not processed")
			}
			switch mode {
			case "cancel":
				d.edit.Cancel()
			case "close":
				s.Close(d)
			case "replace":
				m.Disconnect(p.ID)
				fresh, err := m.ConnectSession(context.Background(), proxy, secret)
				if err != nil {
					t.Fatal(err)
				}
				active.Generation = fresh.Generation()
			}
			var completion mysqlEditResult
			select {
			case completion = <-results:
			case <-time.After(20 * time.Second):
				t.Fatal("original copy executor did not settle")
			}
			if completion.Saved == nil || completion.Saved.CommitConfirmed || !(completion.Saved.CommitUncertain || completion.Saved.Incomplete) || completion.Saved.NativeJSON == "" {
				t.Fatal("lost copy receipt promoted success", completion.Err)
			}
			if !reflect.DeepEqual(before, consulCopyEntry(t, observer, source)) || !bytes.Equal(consulCopyEntry(t, observer, destination).Value, []byte{0, 255}) {
				t.Fatal("fault did not follow actual copy, or source changed")
			}
			if mode != "close" {
				ch := make(chan mysqlEditResult, 1)
				ch <- completion
				d.edit.Results = ch
				if mode == "parked" {
					s.Consume(ui.Services{})
					if !d.edit.Busy || d.nativeReceipt.JSON != "" {
						t.Fatal("foreign profile consumed copy receipt")
					}
					active.ProfileID = p.ID
				}
				waitConsulMutation(t, s, d)
				if !d.HasDraft() || d.edit.Uncertain == nil || d.nativeReceipt.JSON == "" || d.page.RowRefs[len(d.page.RowRefs)-1] != nil {
					t.Fatal("unknown copy lost original draft or receipt")
				}
				original := d.nativeReceipt.JSON
				consulMutationCopy(t, s, d, false)
				consulMutationExport(t, s, d)
				d.nativeReceipt.Open = true
				captureConsulGrid(t, s, d, "key-copy-unknown-"+mode)
				d.nativeReceipt.Open = false
				if mode != "replace" {
					if _, err := s.runtimeFor(d).Count(context.Background(), p.ID, "kv", "keys"); err != nil {
						t.Fatal(err)
					}
				}
				if d.nativeReceipt.JSON != original {
					t.Fatal("read replaced original unknown copy")
				}
				s.saveMySQLData(ui.Services{}, d)
				if d.mutation.open || !s.GuardLeave(d, func(ui.Services) { t.Fatal("unknown copy left without decision") }) {
					t.Fatal("unknown copy lost replay/leave barrier")
				}
				captureConsulGrid(t, s, d, "key-copy-after-read-"+mode)
			}
			if calls.Load() != 1 {
				t.Fatal("original copy replayed", calls.Load())
			}
		})
	}
}

func TestConsulNativePublicGridKeyCopyOriginalInputParkingCancelCloseAndLatePreview(t *testing.T) {
	p, secret := consulGridProfile(t)
	p.ConsulOptions.Prefix = "native/public-copy-input/" + t.Name() + "/"
	m, s, d, active, w := consulMutationState(t, p, secret)
	source := p.ConsulOptions.Prefix + "source"
	consulCopyPut(t, w, source, []byte{0, 255})
	consulMutationLoad(t, s, d, false)
	before := consulCopyEntry(t, w, source)
	s.beginDataKeyCopy(d, 0)
	captured := cloneSnapshot(d.keyCopy.source)
	active.ProfileID = p.ID + 1
	s.Consume(ui.Services{})
	if d.keyCopy == nil {
		t.Fatal("parking lost original source form")
	}
	active.ProfileID = p.ID
	s.cancelDataKeyCopy(d)
	if d.HasDraft() || !reflect.DeepEqual(before, consulCopyEntry(t, w, source)) {
		t.Fatal("form cancel changed native source")
	}
	s.beginDataKeyCopy(d, 0)
	fresh, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	active.Generation = fresh.Generation()
	s.Consume(ui.Services{})
	if d.keyCopy != nil || d.edit.Busy || d.HasDraft() {
		t.Fatal("original target form followed replacement")
	}
	s.Close(d)
	if !m.IsConnected(p.ID) {
		t.Fatal("old copy form close retired replacement")
	}
	if captured.Ref.Object.ProfileID != p.ID {
		t.Fatal("form source scope lost")
	}
}
