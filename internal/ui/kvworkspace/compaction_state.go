package kvworkspace

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/locales"
)

const CompactMethod = "/etcdserverpb.KV/Compact"

// CompactionObservation is a complete earlier read sequence, not a native CAS
// precondition or evidence that a subsequent Compact completed.
type CompactionObservation struct {
	lease      contract.KVRequestLease
	DocumentID uint64
	Scope      contract.KVScope
	Authored   contract.KVRequest
	Revision   int64
	Physical   bool
	Requests   []contract.KVRequest
	Steps      []Receipt
	Captured   bool
}

func (r CompactionObservation) Clone() CompactionObservation {
	r.Scope = r.Scope.Clone()
	r.Requests = slices.Clone(r.Requests)
	r.Steps = slices.Clone(r.Steps)
	for i := range r.Steps {
		r.Steps[i] = r.Steps[i].Clone()
	}
	return r
}

type CompactionReceipt struct {
	DocumentID  uint64
	Scope       contract.KVScope
	Revision    int64
	Physical    bool
	Observation CompactionObservation
}

func (r *CompactionReceipt) clone() *CompactionReceipt {
	if r == nil {
		return nil
	}
	c := *r
	c.Scope = r.Scope.Clone()
	c.Observation = r.Observation.Clone()
	return &c
}

func (d *Document) CompactionAvailable() bool {
	p, ok := d.lease.(contract.KVMaintenance)
	return ok && slices.Contains(p.MaintenanceMethods(), CompactMethod)
}

func (d *Document) CompactionObservation() (CompactionObservation, bool) {
	if d.compactionObservation == nil {
		return CompactionObservation{}, false
	}
	return d.compactionObservation.Clone(), true
}

func DefaultCompactionRequest() contract.KVRequest {
	return contract.KVRequest{Method: CompactMethod, JSON: "{\n  \"revision\": \"1\",\n  \"physical\": false\n}"}
}

// Read editable fields without turning an unfinished revision into zero.
func compactionInputs(r contract.KVRequest) (string, bool, error) {
	var f map[string]json.RawMessage
	if r.Method != CompactMethod || jsondocument.Decode([]byte(r.JSON), &f, inputLimit) != nil || f == nil || f["revision"] == nil {
		return "", false, jsondocument.ErrInvalid
	}
	for k := range f {
		if k != "revision" && k != "physical" {
			return "", false, jsondocument.ErrInvalid
		}
	}
	text := string(f["revision"])
	if text == "null" {
		return "", false, jsondocument.ErrInvalid
	}
	if len(text) > 0 && text[0] == '"' {
		if json.Unmarshal(f["revision"], &text) != nil {
			return "", false, jsondocument.ErrInvalid
		}
	}
	physical := false
	if raw := f["physical"]; raw != nil {
		if string(raw) == "null" || json.Unmarshal(raw, &physical) != nil {
			return "", false, jsondocument.ErrInvalid
		}
	}
	return text, physical, nil
}

func compactionRequestValues(r contract.KVRequest) (int64, bool, error) {
	text, physical, err := compactionInputs(r)
	if err != nil || len(text) == 0 || len(text) > 19 {
		return 0, false, jsondocument.ErrInvalid
	}
	for _, ch := range text {
		if ch < '0' || ch > '9' {
			return 0, false, jsondocument.ErrInvalid
		}
	}
	revision, err := strconv.ParseInt(text, 10, 64)
	if err != nil || revision <= 0 {
		return 0, false, jsondocument.ErrInvalid
	}
	return revision, physical, nil
}

func (s *State) SetCompactionInputs(d *Document, revision string, physical bool) {
	if !s.owned(d) || d.Busy() || d.method != CompactMethod {
		return
	}
	raw, _ := json.MarshalIndent(struct {
		Revision string `json:"revision"`
		Physical bool   `json:"physical"`
	}{revision, physical}, "", "  ")
	d.Editor.Buffer.Set(string(raw))
	d.confirmation = nil
	d.permit = nil
}

func sameCompactionRequest(a, b contract.KVRequest) bool {
	ar, ap, ae := compactionRequestValues(a)
	br, bp, be := compactionRequestValues(b)
	return ae == nil && be == nil && ar == br && ap == bp
}

func (s *State) ObserveCompaction(d *Document) {
	if !s.check(d) || !d.CompactionAvailable() {
		return
	}
	authored := d.Request()
	revision, physical, err := compactionRequestValues(authored)
	if err != nil {
		d.error = locales.Text("Compact revision 必须为完整正整数")
		return
	}
	// AA== is an explicit single-NUL probe key, not the connection prefix.
	rangeJSON, _ := json.Marshal(map[string]any{"key": "AA==", "revision": strconv.FormatInt(revision, 10), "limit": "1", "keys_only": true})
	requests := []contract.KVRequest{{Method: "/etcdserverpb.Maintenance/Status", JSON: "{}"}, {Method: "/etcdserverpb.KV/Range", JSON: string(rangeJSON)}}
	lease := d.lease
	plans := make([]contract.KVPreparation, 0, 2)
	for i, request := range requests {
		plan, e := lease.Prepare(request)
		if e != nil || plan == nil || plan.Writes() {
			d.error = locales.Text("KV 请求无效")
			return
		}
		requests[i] = plan.Snapshot()
		plans = append(plans, plan)
	}
	capture := CompactionObservation{lease: lease, DocumentID: d.id, Scope: d.scope.Clone(), Authored: authored, Revision: revision, Physical: physical, Requests: slices.Clone(requests)}
	d.confirmation = nil
	s.start(d, "compaction-observe", authored, func(ctx context.Context) outcome {
		var lastErr error
		for i, plan := range plans {
			if err := ctx.Err(); err != nil {
				lastErr = err
				break
			}
			r, e := lease.Execute(ctx, plan)
			step := Receipt{Scope: capture.Scope.Clone(), Authored: requests[i], Result: r.Clone()}
			if e != nil {
				step.Error = e.Error()
			}
			capture.Steps = append(capture.Steps, step)
			lastErr = e
			if e != nil || !r.Completed || r.WriteOutcomeUnknown {
				break
			}
		}
		capture.Captured = len(capture.Steps) == len(capture.Requests)
		return outcome{compactionObservation: &capture, err: lastErr}
	})
}

func compactionObservationReady(d *Document, r *CompactionObservation) bool {
	if r == nil || r.lease != d.lease || !r.Captured || r.DocumentID != d.id || r.Authored != d.Request() || !sameEventScope(r.Scope, d.scope) || len(r.Requests) != 2 || len(r.Steps) != 2 {
		return false
	}
	revision, physical, err := compactionRequestValues(r.Authored)
	if err != nil || revision != r.Revision || physical != r.Physical {
		return false
	}
	for i, step := range r.Steps {
		v := step.Result
		if !sameEventScope(step.Scope, r.Scope) || step.Authored != r.Requests[i] || v.Request != r.Requests[i] || !v.Attempted || v.WriteOutcomeUnknown {
			return false
		}
		if !v.Completed {
			return i == 1 && (v.Code == "OutOfRange" || v.Code == "PermissionDenied") && step.Error != ""
		}
		if step.Error != "" || v.Header.ClusterID != r.Scope.Header.ClusterID || v.Header.MemberID != r.Scope.Header.MemberID || v.Header.Revision <= 0 || v.Header.RaftTerm == 0 || len(v.Native) == 0 || v.JSON == "" || v.ProjectionError != "" || v.JSONCaptureError != "" || v.Reply == nil {
			return false
		}
		if i == 0 && (v.Reply.Kind != "status" || v.Reply.Status == nil || v.Reply.Status.Version != r.Scope.Version) {
			return false
		}
		if i == 1 && (v.Reply.Kind != "range" || len(v.Reply.Entries) > 1 || v.Reply.More) {
			return false
		}
	}
	return true
}

func (s *State) PrepareCompaction(d *Document) {
	if !s.check(d) || !d.CompactionAvailable() || d.hasUnknownWrites() {
		return
	}
	if !compactionObservationReady(d, d.compactionObservation) {
		d.error = locales.Text("请先检查此 Compact revision")
		return
	}
	authored := d.Request()
	plan, err := d.lease.Prepare(authored)
	if err != nil || plan == nil || !plan.Writes() || !sameCompactionRequest(authored, plan.Snapshot()) {
		d.error = locales.Text("KV 请求无效")
		return
	}
	r := d.compactionObservation
	c := &CompactionReceipt{DocumentID: d.id, Scope: d.scope.Clone(), Revision: r.Revision, Physical: r.Physical, Observation: r.Clone()}
	// Reserve room in the existing complete 128 MiB operation-record envelope
	// for the bounded native Compact reply before authorizing a write.
	raw, err := compactionPreviewJSON(c)
	if err != nil || len(raw) > 64<<20 {
		d.error = locales.Text("完整 Compact 记录超过限额")
		return
	}
	d.error = ""
	d.confirmation = &confirmation{lease: d.lease, plan: plan, authored: authored, editor: authored, compaction: c, open: true}
	d.permit = nil
}

func validCompactionConfirmation(d *Document, p *confirmation) bool {
	r := p.compaction
	return r != nil && r.DocumentID == d.id && sameEventScope(r.Scope, d.scope) && p.plan.Writes() && sameCompactionRequest(p.authored, p.plan.Snapshot()) && compactionObservationReady(d, &r.Observation) && r.Revision == r.Observation.Revision && r.Physical == r.Observation.Physical
}

func (s *State) executeCompaction(d *Document, p *confirmation) {
	r := p.compaction.clone()
	s.executeManagement(d, p.plan, p.authored, nil)
	d.job.compaction = r
}

func (s *State) consumeCompactionObservation(d *Document, job *operation, out outcome) {
	if out.compactionObservation == nil {
		return
	}
	r := out.compactionObservation.Clone()
	// Retain the original receipt even if its editor/publication is stale. The
	// separate readiness gate prevents using it to authorize another request.
	if r.DocumentID == d.id && r.Authored == job.authored && sameEventScope(r.Scope, job.scope) {
		d.compactionObservation = &r
	}
}
