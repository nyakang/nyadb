package kvworkspace

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// EventReceipt is separate from both the latest KV reply and the immutable
// unknown-write reply. History completion proves only its captured window.
type EventReceipt struct {
	Scope    contract.KVScope
	Authored contract.KVRequest
	Watch    *contract.KVWatchRequest
	History  *contract.KVHistoryResult
	Batch    contract.KVWatchBatch
	Error    string
	preview  string
}

func (r EventReceipt) Clone() EventReceipt {
	r.Scope = r.Scope.Clone()
	r.Batch = r.Batch.Clone()
	if r.Watch != nil {
		v := r.Watch.Clone()
		r.Watch = &v
	}
	if r.History != nil {
		v := r.History.Clone()
		r.History = &v
	}
	return r
}
func (d *Document) LastEventReceipt() (EventReceipt, bool) {
	if d.eventReceipt == nil {
		return EventReceipt{}, false
	}
	return d.eventReceipt.Clone(), true
}

type eventHeaderRecord struct {
	ClusterID uint64 `json:"cluster_id,string"`
	MemberID  uint64 `json:"member_id,string"`
	Revision  int64  `json:"revision,string"`
	RaftTerm  uint64 `json:"raft_term,string"`
}

func eventHeader(h contract.KVHeader) eventHeaderRecord {
	return eventHeaderRecord{h.ClusterID, h.MemberID, h.Revision, h.RaftTerm}
}

type eventEntryRecord struct {
	Key            []byte `json:"key"`
	Value          []byte `json:"value"`
	CreateRevision int64  `json:"create_revision,string"`
	ModRevision    int64  `json:"mod_revision,string"`
	Version        int64  `json:"version,string"`
	Lease          int64  `json:"lease,string"`
}

func eventEntry(e contract.KVEntry) eventEntryRecord {
	return eventEntryRecord{e.Key, e.Value, e.CreateRevision, e.ModRevision, e.Version, e.Lease}
}

type eventRecord struct {
	Type     string            `json:"type"`
	Entry    eventEntryRecord  `json:"entry"`
	Previous *eventEntryRecord `json:"previous,omitempty"`
}

func eventRecords(events []contract.KVEvent) []eventRecord {
	out := make([]eventRecord, len(events))
	for i, e := range events {
		out[i] = eventRecord{Type: e.Type, Entry: eventEntry(e.Entry)}
		if e.Previous != nil {
			p := eventEntry(*e.Previous)
			out[i].Previous = &p
		}
	}
	return out
}

type eventFrameRecord struct {
	Native           []byte            `json:"native_protobuf"`
	JSON             string            `json:"protobuf_json"`
	JSONCaptureError string            `json:"json_capture_error,omitempty"`
	Header           eventHeaderRecord `json:"native_header"`
	WatchID          int64             `json:"watch_id,string"`
	Created          bool              `json:"created"`
	Canceled         bool              `json:"canceled"`
	Progress         bool              `json:"progress"`
	CompactRevision  int64             `json:"compact_revision,string"`
	CancelReason     string            `json:"cancel_reason,omitempty"`
	Events           []eventRecord     `json:"events"`
}

func eventFrameRecords(frames []contract.KVEventFrame) []eventFrameRecord {
	out := make([]eventFrameRecord, len(frames))
	for i, f := range frames {
		out[i] = eventFrameRecord{f.Native, f.JSON, f.JSONCaptureError, eventHeader(f.Header), f.WatchID, f.Created, f.Canceled, f.Progress, f.CompactRevision, f.CancelReason, eventRecords(f.Events)}
	}
	return out
}

type eventWindowRecord struct {
	Key           []byte `json:"key"`
	StartRevision int64  `json:"start_revision,string"`
	EndRevision   int64  `json:"end_revision,string"`
	Limit         int    `json:"limit"`
}

func eventWindow(r contract.KVHistoryRequest) eventWindowRecord {
	return eventWindowRecord{r.Key, r.StartRevision, r.EndRevision, r.Limit}
}

func encodeEventReceipt(r EventReceipt) ([]byte, error) {
	type scopeRecord struct {
		ProfileID  int64             `json:"profile_id,string"`
		Generation uint64            `json:"generation,string"`
		Catalog    string            `json:"catalog"`
		Schema     string            `json:"schema"`
		Prefix     []byte            `json:"prefix"`
		Version    string            `json:"version"`
		Header     eventHeaderRecord `json:"header"`
	}
	record := struct {
		Version      int                      `json:"version"`
		Scope        scopeRecord              `json:"scope"`
		Method       string                   `json:"method"`
		AuthoredJSON string                   `json:"authored_json"`
		Watch        any                      `json:"watch,omitempty"`
		History      any                      `json:"history,omitempty"`
		Frames       []eventFrameRecord       `json:"frames"`
		Ended        bool                     `json:"ended"`
		Incomplete   bool                     `json:"incomplete"`
		Terminal     contract.KVEventTerminal `json:"terminal"`
		Error        string                   `json:"error,omitempty"`
	}{Version: 1, Scope: scopeRecord{r.Scope.Namespace.ProfileID, r.Scope.Generation, r.Scope.Namespace.Catalog, r.Scope.Namespace.Schema, r.Scope.Prefix, r.Scope.Version, eventHeader(r.Scope.Header)}, Method: r.Authored.Method, AuthoredJSON: r.Authored.JSON, Frames: eventFrameRecords(r.Batch.Frames), Ended: r.Batch.Ended, Incomplete: r.Batch.Incomplete, Terminal: r.Batch.Terminal, Error: r.Error}
	if r.Watch != nil {
		w := r.Watch
		record.Watch = struct {
			Key            []byte `json:"key"`
			RangeEnd       []byte `json:"range_end"`
			StartRevision  int64  `json:"start_revision,string"`
			WatchID        int64  `json:"watch_id,string"`
			PrevKV         bool   `json:"prev_kv"`
			ProgressNotify bool   `json:"progress_notify"`
			NoPut          bool   `json:"no_put"`
			NoDelete       bool   `json:"no_delete"`
		}{w.Key, w.RangeEnd, w.StartRevision, w.WatchID, w.PrevKV, w.ProgressNotify, w.NoPut, w.NoDelete}
	}
	if r.History != nil {
		h := r.History
		initial, err := encodeOperationReceipt(Receipt{Scope: r.Scope, Authored: h.Initial.Request, Result: h.Initial})
		if err != nil {
			return nil, err
		}
		record.History = struct {
			Authored          eventWindowRecord        `json:"authored"`
			Window            eventWindowRecord        `json:"window"`
			Initial           json.RawMessage          `json:"initial"`
			Frames            []eventFrameRecord       `json:"frames"`
			Events            []eventRecord            `json:"events"`
			Complete          bool                     `json:"complete"`
			Truncated         bool                     `json:"display_truncated"`
			CaptureIncomplete bool                     `json:"capture_incomplete"`
			ProgressHeader    eventHeaderRecord        `json:"progress_header"`
			ProgressRequests  int                      `json:"progress_requests"`
			Terminal          contract.KVEventTerminal `json:"terminal"`
		}{eventWindow(h.Authored), eventWindow(h.Window), initial, eventFrameRecords(h.Frames), eventRecords(h.Events), h.Complete, h.Truncated, h.CaptureIncomplete, eventHeader(h.ProgressHeader), h.ProgressRequests, h.Terminal}
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if len(raw) > 128<<20 {
		return nil, jsondocument.ErrBounds
	}
	return raw, err
}

func (s *State) EventRecord(d *Document) ([]byte, error) {
	if !s.owned(d) || d.eventReceipt == nil {
		return nil, jsondocument.ErrInvalid
	}
	return encodeEventReceipt(*d.eventReceipt)
}
func (s *State) EventPreview(d *Document) string {
	if !s.owned(d) || d.eventReceipt == nil {
		return ""
	}
	return d.eventReceipt.preview
}
func (s *State) ExportEventRecord(d *Document) {
	if !s.active(d) || d.io != nil || d.eventReceipt == nil {
		return
	}
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	// Capture before the native picker or disk wait, including an active Watch.
	captured := d.eventReceipt.Clone()
	s.fileJob(d, "export", func(ctx context.Context) fileResult {
		raw, err := encodeEventReceipt(captured)
		if err != nil {
			return fileResult{err: err}
		}
		if err = ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		path, err := f.SavePath("events-" + strconv.FormatUint(captured.Scope.Generation, 10) + ".nyakv-receipt.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
