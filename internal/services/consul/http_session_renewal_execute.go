package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

func renewalSession(body []byte, id string) (contract.KVHTTPSessionRenewalSession, error) {
	var nodes []map[string]json.RawMessage
	var s contract.KVHTTPSessionRenewalSession
	if jsondocument.Decode(body, &nodes, responseLimit) != nil || len(nodes) != 1 {
		return s, ErrProtocol
	}
	fields := nodes[0]
	for key, target := range map[string]*string{"ID": &s.ID, "Node": &s.Node, "TTL": &s.TTL, "Behavior": &s.Behavior} {
		if json.Unmarshal(fields[key], target) != nil || *target == "" {
			return s, ErrProtocol
		}
	}
	if raw, ok := fields["Name"]; ok && (bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &s.Name) != nil) {
		return s, ErrProtocol
	}
	if bytes.Equal(bytes.TrimSpace(fields["LockDelay"]), []byte("null")) || json.Unmarshal(fields["LockDelay"], &s.LockDelay) != nil ||
		s.ID != id || s.Behavior != "release" && s.Behavior != "delete" {
		return s, ErrProtocol
	}
	return s, nil
}

func sessionRenewalObservation(r Response, id string, sequence int) contract.KVHTTPSessionRenewalObservation {
	h := documentResult(contract.KVRequest{}, r).HTTP
	o := contract.KVHTTPSessionRenewalObservation{HTTP: *h, Sequence: sequence, ReceivedAt: time.Now().UTC()}
	if !h.Ended || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.CaptureError != "" || h.ProjectionError != "" {
		return o
	}
	s, err := renewalSession(h.Body, id)
	if err != nil {
		o.ProjectionError = err.Error()
		return o
	}
	o.Session, o.ProjectionComplete = s, true
	return o
}

func (w *httpSessionRenewalRun) execute(ctx context.Context, done func(), native Request, reserved int64) {
	b := w.lease.base
	terminal := contract.KVHTTPSessionRenewalTerminal{Reason: "stopped"}
	defer func() {
		w.cancel()
		receiptBudgetMu.Lock()
		b.s.reserved -= reserved
		receiptBudgetMu.Unlock()
		w.mu.Lock()
		w.batch.Ended, w.batch.Terminal = true, terminal
		w.mu.Unlock()
		done()
		b.ops.Unlock()
		close(w.done)
	}()
	if err := b.connect(ctx); err != nil {
		terminal.Reason, terminal.Message = "connection-lost", err.Error()
		return
	}
	var initial contract.KVHTTPSessionRenewalSession
	for sequence := 0; sequence <= w.request.MaxRenewals; sequence++ {
		if sequence > 1 {
			timer := time.NewTimer(w.request.Interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err := b.CheckKVLease(); err != nil {
			terminal.Reason, terminal.Message = "lease-lost", err.Error()
			return
		}
		request := native
		if sequence == 0 {
			request = Request{Method: "GET", Path: "/v1/session/info/" + w.request.SessionID}
		}
		p, err := b.client.Prepare(request)
		if err != nil {
			terminal.Reason, terminal.Message = "request-rejected", err.Error()
			return
		}
		r, err := b.client.Execute(ctx, p)
		o := sessionRenewalObservation(r, w.request.SessionID, sequence)
		if sequence > 0 && r.Attempted {
			terminal.Attempts++
			if r.Acknowledged && r.Succeeded {
				terminal.Acknowledged++
			}
		}
		if r.WriteOutcomeUnknown {
			b.writeUnknown.Store(true)
			terminal.WriteOutcomeUnknown = true
		}
		cost := resultCost(&contract.KVResult{HTTP: &o.HTTP}) + int64(len(o.Session.ID)+len(o.Session.Name)+len(o.Session.Node)+len(o.Session.TTL)+len(o.Session.Behavior)+len(o.ProjectionError))
		// Every dispatch was prepaid, including a final complete or partial
		// result above the authored byte budget. No earlier evidence is evicted.
		if cost > pendingReceiptBudget {
			panic("Consul HTTP session renewal receipt reservation invariant")
		}
		receiptBudgetMu.Lock()
		b.s.reserved -= cost
		b.s.retained += cost
		b.cacheBytes += cost
		reserved -= cost
		receiptBudgetMu.Unlock()
		w.mu.Lock()
		w.batch.RetainedBytes += cost
		w.batch.TerminalOverflow = w.batch.RetainedBytes > w.request.MaxBytes
		overflow := w.batch.TerminalOverflow
		w.batch.Observations = append(w.batch.Observations, o)
		w.batch.Terminal = terminal
		w.pending <- o
		w.mu.Unlock()
		if !b.client.Alive() {
			err = b.markLost(err)
		}
		switch {
		case terminal.WriteOutcomeUnknown:
			terminal.Reason = "write-unknown"
			if err != nil {
				terminal.Message = err.Error()
			}
			return
		case err != nil:
			terminal.Reason, terminal.Message = "request-failed", err.Error()
			return
		case !o.ProjectionComplete:
			terminal.Reason = "session-incomplete"
			return
		case overflow:
			terminal.Reason = "byte-limit"
			return
		}
		ttl, ttlErr := time.ParseDuration(o.Session.TTL)
		if ttlErr != nil || ttl <= w.request.Interval {
			terminal.Reason = "ttl-unusable"
			return
		}
		if sequence == 0 {
			initial = o.Session
		} else if o.Session != initial {
			terminal.Reason = "session-changed"
			return
		}
	}
	terminal.Reason = "renewal-limit"
}

var _ contract.KVHTTPSessionRenewalLease = (*httpSessionRenewalLease)(nil)
var _ contract.KVHTTPSessionRenewalRun = (*httpSessionRenewalRun)(nil)
