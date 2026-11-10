package kvworkspace

import (
	"context"
	"errors"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

const renewalReceiptBytes = 8 << 20

func renewalFrameSize(r contract.KVRenewalReceipt) int {
	return 192 + len(r.Native) + len(r.JSON) + len(r.JSONCaptureError) + len(r.Code)
}
func renewalReceiptSize(b contract.KVRenewalBatch) int {
	size := 256 + len(b.Terminal.Reason) + len(b.Terminal.Code) + len(b.Terminal.Message)
	for _, r := range b.Receipts {
		size += renewalFrameSize(r)
	}
	return size
}
func renewalWorker(ctx context.Context, lease contract.KVRenewalLease, plan contract.KVRenewalPreparation, request contract.KVRenewalRequest, updates chan<- renewalPacket, op *renewalOperation, invalidate func()) renewalOutcome {
	run, err := lease.Start(ctx, plan)
	if run == nil {
		return renewalOutcome{renewalPacket: renewalPacket{err: err, batch: contract.KVRenewalBatch{Request: request, Ended: true, Terminal: contract.KVRenewalTerminal{Reason: "not_started"}}}}
	}
	defer run.Close()
	malformed := run.Request() != request
	charged := 256
	sequence := uint64(0)
	bounded := false
	var display []contract.KVRenewalReceipt
	accept := func(b contract.KVRenewalBatch) contract.KVRenewalBatch {
		out := contract.KVRenewalBatch{Request: request, Ended: b.Ended, Terminal: b.Terminal}
		if b.Request != request || len(b.Receipts) > 64 {
			malformed = true
		}
		for _, r := range b.Receipts {
			if r.Sequence != sequence+1 || !r.Attempted || r.Completed && r.WriteOutcomeUnknown || r.Completed && r.LeaseID != request.LeaseID {
				malformed = true
			}
			sequence = r.Sequence
			size := renewalFrameSize(r)
			if bounded || len(r.Native) > 64<<10 || size > renewalReceiptBytes-charged {
				bounded = true
				continue
			}
			charged += size
			out.Receipts = append(out.Receipts, r.Clone())
			// Only this optional presentation copy is limited. Captured receipts and
			// export keep every original protobuf byte and complete JSON projection.
			v := r
			v.Native = nil
			v.JSON = v.JSON[:min(len(v.JSON), 1024)]
			v.JSONCaptureError = clipped(v.JSONCaptureError)
			display = append(display, v)
			if len(display) > 32 {
				copy(display, display[1:])
				display = display[:32]
			}
		}
		return out
	}
	preview := func(b contract.KVRenewalBatch) string { b.Receipts = display; return renewalPreview(b) }
	finish := func(pending contract.KVRenewalBatch, cause error) renewalOutcome {
		stopErr := run.Stop(context.Background())
		for {
			b := accept(run.Poll())
			pending.Request = request
			pending.Receipts = append(pending.Receipts, b.Receipts...)
			pending.Ended, pending.Terminal = b.Ended, b.Terminal
			if len(b.Receipts) == 0 {
				break
			}
		}
		if bounded {
			pending.Terminal.CaptureIncomplete = true
			cause = errors.Join(cause, jsondocument.ErrBounds)
		}
		if malformed {
			pending.Terminal.Reason = "invalid_original_receipt"
			pending.Terminal.CaptureIncomplete = true
			pending.Terminal.WriteOutcomeUnknown = true
			cause = errors.Join(cause, contract.ErrRevoked)
		}
		return renewalOutcome{renewalPacket: renewalPacket{batch: pending, preview: preview(pending), err: errors.Join(cause, stopErr)}}
	}
	if err != nil || malformed {
		return finish(contract.KVRenewalBatch{Request: request}, err)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		b := accept(run.Poll())
		if bounded || malformed || b.Ended {
			return finish(b, nil)
		}
		if len(b.Receipts) > 0 {
			packet := renewalPacket{batch: b, preview: preview(b)}
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
			return finish(contract.KVRenewalBatch{Request: request}, ctx.Err())
		case <-op.stop:
			return finish(contract.KVRenewalBatch{Request: request}, nil)
		case <-ticker.C:
		}
	}
}
