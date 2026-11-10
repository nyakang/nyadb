package kvworkspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func nativeEventsState(t *testing.T) (*State, *Document, *dbruntime.Manager, connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_EVENTS_WORKSPACE_IT") != "1" {
		t.Skip("requires owned native event-document fixture")
	}
	p, secret := workspaceProfile(t)
	s, d, m := workspaceNativeState(t, p, secret)
	s.deps.EventsProviderFor = m.KVEvents
	return s, d, m, p, secret
}
func eventWatchInput(key, end []byte, revision int64) contract.KVRequest {
	raw, _ := json.Marshal(map[string]any{"key": base64.StdEncoding.EncodeToString(key), "range_end": base64.StdEncoding.EncodeToString(end), "start_revision": strconv.FormatInt(revision, 10), "watch_id": "9007199254740993", "prev_kv": true})
	return contract.KVRequest{Method: WatchMethod, JSON: string(raw)}
}
func eventHistoryInput(key []byte, start, end int64) contract.KVRequest {
	raw, _ := json.Marshal(map[string]any{"key": base64.StdEncoding.EncodeToString(key), "start_revision": strconv.FormatInt(start, 10), "end_revision": strconv.FormatInt(end, 10), "limit": 500})
	return contract.KVRequest{Method: HistoryMethod, JSON: string(raw)}
}
func nativeEventCount(r *EventReceipt) int {
	n := 0
	if r != nil {
		for _, f := range r.Batch.Frames {
			n += len(f.Events)
		}
	}
	return n
}

func TestEtcdNativeEventsWorkspaceOriginalStreamsHistoryAndCompleteArchives(t *testing.T) {
	s, d, m, _, _ := nativeEventsState(t)
	key := append(workspaceKey(t), 0, 255)
	other := append(bytes.Clone(key), 1)
	value := bytes.Repeat([]byte{0, 255, 7}, 2000)
	grant := workspaceExecute(t, s, d, workspaceRequest(t, "Lease/LeaseGrant", &pb.LeaseGrantRequest{ID: 9007199254740993, TTL: 120}))
	leaseID := grant.Result.Reply.Lease.ID
	if leaseID != 9007199254740993 {
		t.Fatal("native lease narrowed")
	}
	put := workspaceExecute(t, s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("before"), Lease: leaseID}))
	revision := put.Result.Header.Revision
	admin := s.Open(2, d.ProfileID(), DefaultRequest())
	if admin == nil {
		t.Fatal("independent request document missing")
	}
	workspaceInput(s, d, eventWatchInput(key, append(bytes.Clone(key), 255), revision+1))
	authored := d.Request()
	scope := d.Scope()
	s.Run(d)
	eventEventually(t, func() bool { s.Consume(); return d.eventReceipt != nil && len(d.eventReceipt.Batch.Frames) > 0 })
	if !d.Busy() || d.NeedsRecovery() {
		t.Fatal("original Watch handshake did not retain stream", d.Error())
	}
	txn := workspaceExecute(t, s, admin, workspaceRequest(t, "KV/Txn", &pb.TxnRequest{Success: []*pb.RequestOp{
		{Request: &pb.RequestOp_RequestPut{RequestPut: &pb.PutRequest{Key: key, Value: value, Lease: leaseID}}},
		{Request: &pb.RequestOp_RequestPut{RequestPut: &pb.PutRequest{Key: other, Value: []byte{0, 255}, Lease: leaseID}}},
	}}))
	deleted := workspaceExecute(t, s, admin, workspaceRequest(t, "KV/DeleteRange", &pb.DeleteRangeRequest{Key: other, PrevKv: true}))
	eventEventually(t, func() bool { s.Consume(); return nativeEventCount(d.eventReceipt) == 3 })
	s.ProgressEvents(d)
	eventEventually(t, func() bool {
		s.Consume()
		for _, f := range d.eventReceipt.Batch.Frames {
			if f.Progress {
				return true
			}
		}
		return false
	})
	s.StopEvents(d)
	consume(t, s, d)
	r := d.eventReceipt
	if r == nil || r.Authored != authored || !sameEventScope(r.Scope, scope) || !r.Batch.Ended || r.Batch.Incomplete || nativeEventCount(r) != 3 {
		t.Fatal("original Watch final receipt changed", d.Error())
	}
	var events []contract.KVEvent
	for _, frame := range r.Batch.Frames {
		native := &pb.WatchResponse{}
		if proto.Unmarshal(frame.Native, native) != nil || native.WatchId != frame.WatchID || !frame.Progress && native.WatchId != 9007199254740993 || frame.Progress && native.WatchId != -1 {
			t.Fatal("complete original frame changed")
		}
		events = append(events, frame.Events...)
	}
	if events[0].Entry.ModRevision != txn.Result.Header.Revision || events[1].Entry.ModRevision != txn.Result.Header.Revision || events[0].Previous == nil || string(events[0].Previous.Value) != "before" || !bytes.Equal(events[0].Entry.Value, value) || events[0].Entry.Lease != leaseID || events[2].Type != "DELETE" || events[2].Entry.ModRevision != deleted.Result.Header.Revision || events[2].Previous == nil || !bytes.Equal(events[2].Previous.Key, other) {
		t.Fatal("native same-revision order/previous/binary/lease changed")
	}
	raw, err := s.EventRecord(d)
	if err != nil || !strings.Contains(string(raw), base64.StdEncoding.EncodeToString(value)) || !strings.Contains(string(raw), `"9007199254740993"`) {
		t.Fatal("complete event archive lost native precision/value", err)
	}
	s.ClearEventReceipt(d)
	consume(t, s, d)
	workspaceInput(s, d, eventHistoryInput(key, revision, txn.Result.Header.Revision))
	historyAuthored := d.Request()
	s.Run(d)
	consume(t, s, d)
	h := d.eventReceipt.History
	if h == nil || !h.Complete || h.CaptureIncomplete || h.Window.EndRevision != txn.Result.Header.Revision || h.Authored.StartRevision != revision || len(h.Events) != 2 || h.ProgressRequests < 1 || h.ProgressHeader.Revision < h.Window.EndRevision || !bytes.Equal(h.Events[1].Entry.Value, value) || d.eventReceipt.Authored != historyAuthored {
		t.Fatal("native fixed window/history progress not proven", d.Error())
	}
	initial := &pb.RangeResponse{}
	if proto.Unmarshal(h.Initial.Native, initial) != nil || len(initial.Kvs) != 1 || initial.Kvs[0].ModRevision != txn.Result.Header.Revision {
		t.Fatal("original Range receipt changed")
	}
	if _, err = m.KVRequests(d.ProfileID()); err != nil {
		t.Fatal("event operations retired independent original requests", err)
	}
	workspaceExecute(t, s, admin, workspaceRequest(t, "Lease/LeaseRevoke", &pb.LeaseRevokeRequest{ID: leaseID}))
}

func TestEtcdNativeEventsWorkspaceParkedStopPublicationRecoveryAndRequestFiles(t *testing.T) {
	s, d, m, p, secret := nativeEventsState(t)
	key := workspaceKey(t)
	put := workspaceExecute(t, s, d, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("before")}))
	admin := s.Open(2, p.ID, DefaultRequest())
	workspaceInput(s, d, eventWatchInput(key, nil, put.Result.Header.Revision+1))
	authored := d.Request()
	s.Run(d)
	eventEventually(t, func() bool { s.Consume(); return len(d.eventReceipt.Batch.Frames) > 0 })
	active := true
	s.deps.Active = func(doc *Document) bool { return doc != d || active }
	active = false
	workspaceExecute(t, s, admin, workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte{0, 255}}))
	op := d.eventJob
	eventEventually(t, func() bool { return len(op.updates) == 1 })
	before := len(d.eventReceipt.Batch.Frames)
	s.Consume()
	if len(d.eventReceipt.Batch.Frames) != before {
		t.Fatal("parked Watch entered active other document")
	}
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	await(t, op.finished)
	s.Consume()
	if d.eventJob != op {
		t.Fatal("publication replacement consumed parked terminal")
	}
	active = true
	consume(t, s, d)
	if !d.NeedsRecovery() || nativeEventCount(d.eventReceipt) != 1 || d.eventReceipt.Scope.Generation >= mustEventGeneration(t, m, p.ID) {
		t.Fatal("late original event lost/substituted publication", d.Error())
	}
	old := d.eventReceipt
	originalScope := old.Scope.Clone()
	s.Recover(d)
	consume(t, s, d)
	if d.NeedsRecovery() || d.eventReceipt != old || !sameEventScope(old.Scope, originalScope) || d.Busy() || d.Request() != authored {
		t.Fatal("explicit recovery replayed/changed original Watch", d.Error())
	}
	requestRaw, err := EncodeRequest(authored)
	if err != nil {
		t.Fatal(err)
	}
	request, err := DecodeRequest(requestRaw)
	if err != nil || request != authored {
		t.Fatal("native event draft version-one round trip failed", err)
	}
	var exported []byte
	s.SetFiles(Files{SavePath: func(string) (string, error) { return "captured-events.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var b bytes.Buffer
		err := write(&b)
		exported = bytes.Clone(b.Bytes())
		return err
	}})
	s.ExportEventRecord(d)
	consume(t, s, d)
	record, err := s.EventRecord(d)
	if err != nil || !bytes.Equal(record, exported) {
		t.Fatal("original complete receipt export changed", err)
	}
	s.ClearEventReceipt(d)
	consume(t, s, d)
	if d.eventReceipt != nil {
		t.Fatal("recovered explicit receipt disposal failed", d.Error())
	}
	// A new Watch is explicit and remains alive after the handshake; stopping
	// this executor never subscribes another or clears the earlier archive.
	s.Run(d)
	eventEventually(t, func() bool { s.Consume(); return len(d.eventReceipt.Batch.Frames) > 0 })
	s.StopEvents(d)
	consume(t, s, d)
	if !d.eventReceipt.Batch.Ended || d.eventReceipt.Batch.Incomplete {
		t.Fatal("explicit restarted stream stop was not acknowledged", d.Error())
	}
	s.Close(d)
	if d.Editor != nil || d.eventReceipt != nil || d.eventLease != nil || d.eventJob != nil {
		t.Fatal("native event document close retained caches")
	}
}
func mustEventGeneration(t *testing.T, m *dbruntime.Manager, id int64) uint64 {
	t.Helper()
	p, err := m.KVEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	return p.KVScope().Generation
}

func TestEtcdNativeEventsWorkspaceHistoryKeepsActualUnknownWriteReceipt(t *testing.T) {
	if os.Getenv("NYADB_ETCD_EVENTS_WORKSPACE_IT") != "1" {
		t.Skip("requires owned native event-document fixture")
	}
	p, secret := workspaceProfile(t)
	key := workspaceKey(t)
	proxy, fault := workspaceProxy(t, p, key, "lost")
	s, d, m := workspaceNativeState(t, proxy, secret)
	s.deps.EventsProviderFor = m.KVEvents
	put := workspaceRequest(t, "KV/Put", &pb.PutRequest{Key: key, Value: []byte("original")})
	workspaceInput(s, d, put)
	s.Run(d)
	s.Confirm(d, true)
	await(t, fault.entered)
	consume(t, s, d)
	await(t, fault.done)
	unknown, ok := d.UnknownReceipt()
	if !ok || !unknown.Result.WriteOutcomeUnknown || fault.calls.Load() != 1 || fault.receipt == nil {
		t.Fatal("actual processed Put lost original unknown receipt")
	}
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	s.Recover(d)
	consume(t, s, d)
	workspaceInput(s, d, eventHistoryInput(key, fault.receipt.Header.Revision, 0))
	s.Run(d)
	consume(t, s, d)
	h := d.eventReceipt.History
	if h == nil || !h.Complete || len(h.Events) != 2 || string(h.Events[0].Entry.Value) != "original" || string(h.Events[1].Entry.Value) != "later-independent" || h.Window.EndRevision <= fault.receipt.Header.Revision {
		t.Fatal("native history did not preserve original and later observation", d.Error())
	}
	retained, ok := d.UnknownReceipt()
	if !ok || !retained.Result.WriteOutcomeUnknown || retained.Authored != unknown.Authored || retained.Scope.Generation != unknown.Scope.Generation || d.last != d.unknown || fault.calls.Load() != 1 {
		t.Fatal("History reclassified/replaced original unknown write")
	}
	s.SetMethod(d, put.Method)
	d.Editor.Buffer.Set(put.JSON)
	s.Run(d)
	if d.Busy() || d.confirmation != nil || fault.calls.Load() != 1 {
		t.Fatal("native history enabled write replay")
	}
	d.selectedUnknown = true
	record, err := s.OperationRecord(d)
	if err != nil || !strings.Contains(string(record), `"write_outcome_unknown": true`) {
		t.Fatal("original unknown export lost native state", err)
	}
}
