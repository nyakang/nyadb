package kvworkspace

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// A continuous run retains its original authorization and all attempt receipts;
// an unknown run stays independent of later one-shot read receipts.
type RenewalReceipt struct {
	Scope    contract.KVScope
	Authored contract.KVRequest
	Batch    contract.KVRenewalBatch
	Error    string
	preview  string
}

func (r RenewalReceipt) Clone() RenewalReceipt {
	r.Scope = r.Scope.Clone()
	r.Batch = r.Batch.Clone()
	return r
}
func (d *Document) LastRenewalReceipt() (RenewalReceipt, bool) {
	if d.renewalReceipt == nil {
		return RenewalReceipt{}, false
	}
	return d.renewalReceipt.Clone(), true
}
func (d *Document) UnknownRenewalReceipt() (RenewalReceipt, bool) {
	if d.renewalUnknown == nil {
		return RenewalReceipt{}, false
	}
	return d.renewalUnknown.Clone(), true
}
func (d *Document) RenewalConfirmation() (contract.KVRenewalRequest, bool) {
	if d.renewalConfirmation == nil {
		return contract.KVRenewalRequest{}, false
	}
	return d.renewalConfirmation.plan.Snapshot(), true
}

type renewalFrameRecord struct {
	Sequence         uint64            `json:"sequence,string"`
	Attempted        bool              `json:"attempted"`
	Completed        bool              `json:"completed"`
	Unknown          bool              `json:"write_outcome_unknown"`
	ID               int64             `json:"lease_id,string"`
	TTL              int64             `json:"ttl,string"`
	Header           eventHeaderRecord `json:"native_header"`
	Code             string            `json:"code"`
	Native           []byte            `json:"native_protobuf"`
	JSON             string            `json:"protobuf_json"`
	JSONCaptureError string            `json:"json_capture_error,omitempty"`
}

func renewalRecords(receipts []contract.KVRenewalReceipt) []renewalFrameRecord {
	out := make([]renewalFrameRecord, len(receipts))
	for i, r := range receipts {
		out[i] = renewalFrameRecord{r.Sequence, r.Attempted, r.Completed, r.WriteOutcomeUnknown, r.LeaseID, r.TTL, eventHeader(r.Header), r.Code, r.Native, r.JSON, r.JSONCaptureError}
	}
	return out
}

type renewalTerminalRecord struct {
	Reason       string `json:"reason"`
	Code         string `json:"code"`
	Message      string `json:"message"`
	Attempts     uint64 `json:"attempts,string"`
	Acknowledged uint64 `json:"acknowledged,string"`
	Unknown      bool   `json:"write_outcome_unknown"`
	Incomplete   bool   `json:"capture_incomplete"`
}

func renewalTerminal(t contract.KVRenewalTerminal) renewalTerminalRecord {
	return renewalTerminalRecord{t.Reason, t.Code, t.Message, t.Attempts, t.Acknowledged, t.WriteOutcomeUnknown, t.CaptureIncomplete}
}
func renewalPreview(b contract.KVRenewalBatch) string {
	receipts := b.Receipts[max(0, len(b.Receipts)-32):]
	projected := make([]contract.KVRenewalReceipt, len(receipts))
	for i, r := range receipts {
		r.Native = nil
		r.JSON = clipped(r.JSON[:min(len(r.JSON), 1024)])
		projected[i] = r
	}
	raw, _ := json.MarshalIndent(struct {
		ID       string                `json:"original_lease_id"`
		Ended    bool                  `json:"ended"`
		Terminal renewalTerminalRecord `json:"terminal"`
		Receipts []renewalFrameRecord  `json:"latest_receipts"`
	}{strconv.FormatInt(b.Request.LeaseID, 10), b.Ended, renewalTerminal(b.Terminal), renewalRecords(projected)}, "", "  ")
	return clipped(string(raw))
}
func (s *State) RenewalPreview(d *Document) string {
	if !s.owned(d) || d.renewalReceipt == nil {
		return ""
	}
	return d.renewalReceipt.preview
}
func EncodeRenewalRecord(r RenewalReceipt) ([]byte, error) {
	if renewalReceiptSize(r.Batch) > renewalReceiptBytes || len(r.Authored.JSON) > inputLimit {
		return nil, jsondocument.ErrBounds
	}
	raw, err := json.MarshalIndent(struct {
		Version        int                   `json:"version"`
		Kind           string                `json:"kind"`
		ProfileID      int64                 `json:"profile_id,string"`
		Catalog        string                `json:"catalog"`
		Schema         string                `json:"schema"`
		Generation     uint64                `json:"original_generation,string"`
		Prefix         []byte                `json:"browse_prefix"`
		Header         eventHeaderRecord     `json:"original_native_header"`
		ProductVersion string                `json:"original_product_version"`
		Authored       contract.KVRequest    `json:"authored_request"`
		ID             int64                 `json:"original_lease_id,string"`
		Ended          bool                  `json:"ended"`
		Terminal       renewalTerminalRecord `json:"terminal"`
		Receipts       []renewalFrameRecord  `json:"receipts"`
		Error          string                `json:"error,omitempty"`
	}{1, "etcd-lease-renewal", r.Scope.Namespace.ProfileID, r.Scope.Namespace.Catalog, r.Scope.Namespace.Schema, r.Scope.Generation, r.Scope.Prefix, eventHeader(r.Scope.Header), r.Scope.Version, r.Authored, r.Batch.Request.LeaseID, r.Batch.Ended, renewalTerminal(r.Batch.Terminal), renewalRecords(r.Batch.Receipts), r.Error}, "", "  ")
	if len(raw) > 64<<20 {
		return nil, jsondocument.ErrBounds
	}
	return raw, err
}
func (s *State) RenewalRecord(d *Document) ([]byte, error) {
	if !s.owned(d) || d.renewalReceipt == nil {
		return nil, jsondocument.ErrInvalid
	}
	return EncodeRenewalRecord(d.renewalReceipt.Clone())
}
func (s *State) CopyRenewalRecord(services ui.Services, d *Document) {
	if !s.active(d) || d.io != nil || d.renewalReceipt == nil {
		return
	}
	captured := d.renewalReceipt.Clone()
	s.fileJob(d, "copy-renewal", func(ctx context.Context) fileResult {
		if ctx.Err() != nil {
			return fileResult{err: ctx.Err()}
		}
		raw, err := EncodeRenewalRecord(captured)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return fileResult{clipboard: string(raw), err: err}
	})
	d.io.clipboard = services.WriteClipboard
}
func (s *State) ExportRenewalRecord(d *Document) {
	if !s.active(d) || d.io != nil || d.renewalReceipt == nil {
		return
	}
	f := s.deps.Files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	captured := d.renewalReceipt.Clone()
	s.fileJob(d, "export-renewal", func(ctx context.Context) fileResult {
		if ctx.Err() != nil {
			return fileResult{err: ctx.Err()}
		}
		raw, err := EncodeRenewalRecord(captured)
		if err != nil {
			return fileResult{err: err}
		}
		path, err := f.SavePath("lease-renewal.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
