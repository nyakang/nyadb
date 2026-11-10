package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"time"
)

func eventFrameSize(f contract.KVEventFrame) int {
	size := 256 + len(f.Native) + len(f.JSON) + len(f.JSONCaptureError) + len(f.CancelReason)
	for _, e := range f.Events {
		size += 128 + len(e.Entry.Key) + len(e.Entry.Value) + len(e.Type)
		if e.Previous != nil {
			size += 128 + len(e.Previous.Key) + len(e.Previous.Value)
		}
	}
	return size
}
func historyReceiptSize(h contract.KVHistoryResult) int {
	size := 512 + len(h.Authored.Key) + len(h.Window.Key) + len(h.Initial.Native) + len(h.Initial.JSON)
	for _, f := range h.Frames {
		size += eventFrameSize(f)
	}
	size += eventFrameSize(contract.KVEventFrame{Events: h.Events})
	if h.Initial.Reply != nil {
		for _, e := range h.Initial.Reply.Entries {
			size += 128 + len(e.Key) + len(e.Value)
		}
	}
	return size
}

type eventDisplay struct {
	events            []contract.KVEvent
	count             int64
	header            contract.KVHeader
	created, progress bool
	truncated         bool
}

func (v *eventDisplay) add(events []contract.KVEvent) {
	for _, e := range events {
		v.count++
		// Only presentation copies are shortened. Complete original frames and
		// typed values remain in EventReceipt and its independent export.
		original := e
		if len(e.Entry.Key) > 1024 || len(e.Entry.Value) > 1024 {
			v.truncated = true
		}
		e.Entry.Key = bytes.Clone(e.Entry.Key[:min(len(e.Entry.Key), 1024)])
		e.Entry.Value = bytes.Clone(e.Entry.Value[:min(len(e.Entry.Value), 1024)])
		if e.Previous != nil {
			previous := *original.Previous
			e.Previous = &previous
			if len(e.Previous.Key) > 1024 || len(e.Previous.Value) > 1024 {
				v.truncated = true
			}
			e.Previous.Key = bytes.Clone(e.Previous.Key[:min(len(e.Previous.Key), 1024)])
			e.Previous.Value = bytes.Clone(e.Previous.Value[:min(len(e.Previous.Value), 1024)])
		}
		if len(v.events) == 500 {
			copy(v.events, v.events[1:])
			v.events[499] = e
		} else {
			v.events = append(v.events, e)
		}
	}
}
func (v *eventDisplay) packet(b contract.KVWatchBatch) string {
	raw, _ := json.MarshalIndent(struct {
		Count          int64                    `json:"observed_events,string"`
		DisplayLimited bool                     `json:"display_limited"`
		ValuesClipped  bool                     `json:"values_clipped"`
		Created        bool                     `json:"created"`
		Progress       bool                     `json:"progress"`
		Header         eventHeaderRecord        `json:"latest_native_header"`
		Ended          bool                     `json:"ended"`
		Incomplete     bool                     `json:"incomplete"`
		Terminal       contract.KVEventTerminal `json:"terminal"`
		Events         []eventRecord            `json:"events"`
	}{v.count, v.count > 500, v.truncated, v.created, v.progress, eventHeader(v.header), b.Ended, b.Incomplete, b.Terminal, eventRecords(v.events)}, "", "  ")
	return clipped(string(raw))
}
func eventHistoryPreview(h contract.KVHistoryResult) string {
	v := eventDisplay{header: h.ProgressHeader}
	v.add(h.Events)
	return v.packet(contract.KVWatchBatch{Ended: true, Incomplete: h.CaptureIncomplete || !h.Complete, Terminal: h.Terminal})
}
func eventWatchWorker(ctx context.Context, lease contract.KVEventsLease, r contract.KVWatchRequest, updates chan<- eventPacket, op *eventOperation, invalidate func()) eventOutcome {
	run, err := lease.StartWatch(ctx, r.Clone())
	if run == nil {
		return eventOutcome{eventPacket: eventPacket{err: err, batch: contract.KVWatchBatch{Ended: true, Incomplete: true, Terminal: contract.KVEventTerminal{Reason: "not_started"}}}}
	}
	defer run.Close()
	if !sameWatchRequest(run.Request(), r) {
		err = errors.Join(err, contract.ErrRevoked)
	}
	display := eventDisplay{}
	charged := 0
	bounded := false
	accept := func(b contract.KVWatchBatch) contract.KVWatchBatch {
		out := contract.KVWatchBatch{Ended: b.Ended, Incomplete: b.Incomplete, Terminal: b.Terminal}
		for _, f := range b.Frames {
			size := eventFrameSize(f)
			if bounded || size > eventReceiptBytes-charged {
				bounded = true
				continue
			}
			charged += size
			out.Frames = append(out.Frames, f.Clone())
			display.header = f.Header
			display.created = display.created || f.Created
			display.progress = display.progress || f.Progress
			display.add(f.Events)
		}
		return out
	}
	finish := func(pending contract.KVWatchBatch, cause error) eventOutcome {
		stopErr := run.Stop(context.Background()) // original acknowledgement, even when its request context is canceled
		for {
			b := accept(run.Poll())
			pending.Frames = append(pending.Frames, b.Frames...)
			pending.Ended, pending.Incomplete, pending.Terminal = b.Ended, b.Incomplete, b.Terminal
			if len(b.Frames) == 0 {
				break
			}
		}
		if bounded {
			pending.Ended = true
			pending.Incomplete = true
			pending.Terminal = contract.KVEventTerminal{Reason: "document_bounds"}
			cause = errors.Join(cause, jsondocument.ErrBounds)
		}
		return eventOutcome{eventPacket: eventPacket{batch: pending, preview: display.packet(pending), err: errors.Join(cause, stopErr)}}
	}
	if err != nil {
		return finish(contract.KVWatchBatch{}, err)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		b := accept(run.Poll())
		if bounded || b.Ended {
			return finish(b, nil)
		}
		if len(b.Frames) > 0 {
			packet := eventPacket{batch: b, preview: display.packet(b)}
			select {
			case updates <- packet:
				if invalidate != nil {
					invalidate()
				}
			case <-ctx.Done():
				return finish(b, ctx.Err())
			case <-op.stop:
				return finish(b, nil)
			}
		}
		select {
		case <-ctx.Done():
			return finish(contract.KVWatchBatch{}, ctx.Err())
		case <-op.stop:
			return finish(contract.KVWatchBatch{}, nil)
		case <-op.progress:
			if err := run.Progress(); err != nil {
				return finish(contract.KVWatchBatch{}, err)
			}
		case <-ticker.C:
		}
	}
}
