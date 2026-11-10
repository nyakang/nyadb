package etcd

import (
	"bytes"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func nativeWatchRequest(r contract.KVWatchRequest) *pb.WatchCreateRequest {
	p := &pb.WatchCreateRequest{Key: bytes.Clone(r.Key), RangeEnd: bytes.Clone(r.RangeEnd), StartRevision: r.StartRevision, WatchId: r.WatchID, PrevKv: r.PrevKV, ProgressNotify: r.ProgressNotify}
	if r.NoPut {
		p.Filters = append(p.Filters, pb.WatchCreateRequest_NOPUT)
	}
	if r.NoDelete {
		p.Filters = append(p.Filters, pb.WatchCreateRequest_NODELETE)
	}
	return p
}

func eventEntry(e *mvccpb.KeyValue) contract.KVEntry {
	return contract.KVEntry{Key: bytes.Clone(e.Key), Value: bytes.Clone(e.Value), CreateRevision: e.CreateRevision, ModRevision: e.ModRevision, Version: e.Version, Lease: e.Lease}
}

func projectEvent(e *mvccpb.Event) contract.KVEvent {
	result := contract.KVEvent{Type: e.Type.String(), Entry: eventEntry(e.Kv)}
	if e.PrevKv != nil {
		p := eventEntry(e.PrevKv)
		result.Previous = &p
	}
	return result
}

func projectEventFrame(f WatchFrame) contract.KVEventFrame {
	r := contract.KVEventFrame{Native: bytes.Clone(f.Native), Header: contract.KVHeader{ClusterID: f.Header.ClusterID, MemberID: f.Header.MemberID, Revision: f.Header.Revision, RaftTerm: f.Header.RaftTerm}, WatchID: f.WatchID, Created: f.Created, Canceled: f.Canceled, Progress: f.Progress, CompactRevision: f.CompactRevision, CancelReason: f.CancelReason}
	for _, e := range f.Events {
		r.Events = append(r.Events, projectEvent(e))
	}
	original := &pb.WatchResponse{}
	if err := proto.Unmarshal(f.Native, original); err != nil {
		r.JSONCaptureError = "original event frame JSON projection failed; full protobuf retained"
		return r
	}
	budget := 1 << 20
	if !jsonBound(original.ProtoReflect(), &budget, 0) {
		r.JSONCaptureError = "event frame JSON projection exceeds 1 MiB; full protobuf retained"
		return r
	}
	encoded, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(original)
	if err != nil || len(encoded) > 1<<20 {
		r.JSONCaptureError = "event frame JSON projection exceeds 1 MiB or is invalid; full protobuf retained"
	} else {
		r.JSON = string(encoded)
	}
	return r
}

func eventTerminal(t WatchTerminal) contract.KVEventTerminal {
	r := contract.KVEventTerminal{Reason: t.Reason, Code: status.Code(t.Cause).String()}
	if t.Cause != nil {
		r.Message = t.Cause.Error()
	}
	return r
}

func projectHistory(r HistoryResult) contract.KVHistoryResult {
	initialJSON, _ := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(&pb.RangeRequest{Key: r.Authored.Key, Revision: r.Authored.EndRevision, Limit: 1, KeysOnly: true})
	out := contract.KVHistoryResult{Authored: contract.KVHistoryRequest{Key: bytes.Clone(r.Authored.Key), StartRevision: r.Authored.StartRevision, EndRevision: r.Authored.EndRevision, Limit: r.Authored.Limit}, Window: contract.KVHistoryRequest{Key: bytes.Clone(r.Window.Key), StartRevision: r.Window.StartRevision, EndRevision: r.Window.EndRevision, Limit: r.Window.Limit}, Initial: projectKVResult(contract.KVRequest{Method: "/etcdserverpb.KV/Range", JSON: string(initialJSON)}, r.Initial), Complete: r.Complete, Truncated: r.Truncated, CaptureIncomplete: r.CaptureIncomplete, ProgressHeader: contract.KVHeader{ClusterID: r.ProgressHeader.ClusterID, MemberID: r.ProgressHeader.MemberID, Revision: r.ProgressHeader.Revision, RaftTerm: r.ProgressHeader.RaftTerm}, ProgressRequests: r.ProgressRequests, Terminal: eventTerminal(r.Terminal)}
	for _, f := range r.Frames {
		out.Frames = append(out.Frames, projectEventFrame(f))
	}
	for _, e := range r.Events {
		out.Events = append(out.Events, projectEvent(e))
	}
	return out
}
