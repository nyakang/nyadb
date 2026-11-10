package etcd

import (
	"bytes"
	"context"
	"errors"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"google.golang.org/protobuf/proto"
)

type HistoryRequest struct {
	Key                        []byte
	StartRevision, EndRevision int64
	Limit                      int
}

func (r HistoryRequest) clone() HistoryRequest { r.Key = bytes.Clone(r.Key); return r }

// Complete describes only the captured exact-key revision window. Frames keep
// the original receipts, which may also contain later events; Events is the
// bounded window projection and never pretends to be an all-history archive.
type HistoryResult struct {
	Authored, Window    HistoryRequest
	Initial             Response
	Frames              []WatchFrame
	Events              []*mvccpb.Event
	Complete, Truncated bool // Truncated only limits the newest-event projection.
	CaptureIncomplete   bool // Native receipts exceeded the owned capture budget.
	ProgressHeader      Header
	ProgressRequests    int
	Terminal            WatchTerminal
}

func (r HistoryResult) Clone() HistoryResult {
	r.Authored = r.Authored.clone()
	r.Window = r.Window.clone()
	r.Initial = r.Initial.clone()
	r.Frames = append([]WatchFrame(nil), r.Frames...)
	for i := range r.Frames {
		r.Frames[i] = r.Frames[i].clone()
	}
	r.Events = append([]*mvccpb.Event(nil), r.Events...)
	for i := range r.Events {
		r.Events[i] = proto.Clone(r.Events[i]).(*mvccpb.Event)
	}
	return r
}

func validateHistoryRequest(r HistoryRequest) error {
	if !validKey(r.Key) || r.StartRevision < 0 || r.EndRevision < 0 || r.Limit < 1 || r.Limit > 500 || r.EndRevision > 0 && r.StartRevision > r.EndRevision {
		return ErrInvalid
	}
	return nil
}
func historyStart(requested, target int64) int64 {
	if requested > 0 {
		return requested
	}
	return max(int64(1), target-9999)
}
func (c *Client) History(parent context.Context, input HistoryRequest) (result HistoryResult, err error) {
	request := input.clone()
	result.Authored = request.clone()
	result.Window = request.clone()
	if err = validateHistoryRequest(request); err != nil {
		return result, err
	}
	if err = parent.Err(); err != nil {
		return result, err
	}
	if !c.operation.TryLock() {
		return result, ErrBusy
	}
	owned := true
	defer func() {
		if owned {
			c.operation.Unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(parent, c.config.RequestTimeout)
	defer cancel()
	plan, err := c.Prepare(&pb.RangeRequest{Key: bytes.Clone(request.Key), Revision: request.EndRevision, Limit: 1, KeysOnly: true})
	if err != nil {
		return result, err
	}
	result.Initial, err = c.executeOwned(ctx, plan)
	if err != nil {
		return result, err
	}
	original, ok := result.Initial.Native().(*pb.RangeResponse)
	if !ok || original.More || len(original.Kvs) > 1 || original.Count != int64(len(original.Kvs)) {
		return result, ErrProtocol
	}
	if request.EndRevision == 0 {
		request.EndRevision = result.Initial.Header.Revision
	}
	target := request.EndRevision
	if len(original.Kvs) == 1 {
		kv := original.Kvs[0]
		if kv == nil || !bytes.Equal(kv.Key, request.Key) || kv.CreateRevision <= 0 || kv.ModRevision < kv.CreateRevision || kv.ModRevision > request.EndRevision || kv.Version <= 0 {
			return result, ErrProtocol
		}
		target = kv.ModRevision
	}
	request.StartRevision = historyStart(request.StartRevision, target)
	if request.StartRevision > request.EndRevision || request.EndRevision > result.Initial.Header.Revision {
		return result, ErrInvalid
	}
	result.Window = request.clone()
	totalBytes := 4*proto.Size(original) + 128
	totalEvents := 0
	if totalBytes > watchByteLimit {
		result.CaptureIncomplete = true
		return result, ErrBounds
	}
	owned = false // transferred to the original receive executor
	run, startErr := c.startWatchOwned(ctx, &pb.WatchCreateRequest{Key: bytes.Clone(request.Key), WatchId: 1, StartRevision: request.StartRevision, PrevKv: true, ProgressNotify: true})
	if run == nil {
		return result, startErr
	}
	record := func(frames []WatchFrame) error {
		for _, frame := range frames {
			charge := len(frame.Native)*4 + 512*len(frame.Events) + 128
			if len(result.Frames) >= watchBatchLimit || totalEvents+len(frame.Events) > watchEventLimit || totalBytes+charge > watchByteLimit {
				result.CaptureIncomplete = true
				return ErrBounds
			}
			result.Frames = append(result.Frames, frame)
			totalBytes += charge
			totalEvents += len(frame.Events)
			for _, event := range frame.Events {
				revision := event.Kv.ModRevision
				if revision < request.StartRevision || revision > request.EndRevision {
					continue
				}
				if len(result.Events) >= request.Limit {
					result.Truncated = true
					copy(result.Events, result.Events[1:])
					result.Events[len(result.Events)-1] = event
				} else {
					result.Events = append(result.Events, event)
				}
			}
			if frame.Progress && frame.Header.Revision >= request.EndRevision {
				result.Complete = true
				result.ProgressHeader = frame.Header
			}
		}
		return nil
	}
	recordPending := func() error {
		for {
			frames := run.Poll()
			if len(frames) == 0 {
				return nil
			}
			if err := record(frames); err != nil {
				return err
			}
		}
	}
	if startErr != nil {
		run.cancel()
		<-run.done
		err = recordPending()
		result.Terminal, _ = run.Terminal()
		if errors.Is(startErr, ErrBounds) || errors.Is(result.Terminal.Cause, ErrBounds) {
			result.CaptureIncomplete = true
		}
		_ = run.Close()
		return result, errors.Join(startErr, err)
	}
	defer func() {
		stopErr := run.Stop(context.Background())
		// Include the actual original cancellation receipt and any queued complete
		// frames. A later transport failure does not erase an already proven window.
		captureErr := recordPending()
		result.Terminal, _ = run.Terminal()
		if errors.Is(result.Terminal.Cause, ErrBounds) || errors.Is(captureErr, ErrBounds) {
			result.CaptureIncomplete = true
		}
		_ = run.Close()
		err = errors.Join(err, stopErr, captureErr)
	}()
	progress := time.NewTicker(100 * time.Millisecond)
	defer progress.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		if err = recordPending(); err != nil {
			return result, err
		}
		if result.Complete {
			return result, nil
		}
		if terminal, ended := run.Terminal(); ended {
			result.Terminal = terminal
			return result, terminal.Cause
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-progress.C:
			// A finite schedule on this same stream. A missing acknowledgement
			// never causes a new Watch or an inferred complete/empty window.
			if result.ProgressRequests >= 64 {
				continue
			}
			result.ProgressRequests++
			if err = run.Progress(); err != nil {
				return result, err
			}
		case <-poll.C:
		}
	}
}
