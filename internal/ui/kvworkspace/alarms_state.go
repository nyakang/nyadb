package kvworkspace

import (
	"context"
	"errors"
	"reflect"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/locales"
)

// AlarmObservation is complete evidence, independent of the private authority
// retained by its original document. A copy cannot authorize a new batch.
type AlarmObservation struct {
	DocumentID uint64
	Scope      contract.KVScope
	Filter     int32
	Receipt    Receipt
}

func (r AlarmObservation) Clone() AlarmObservation {
	r.Scope = r.Scope.Clone()
	r.Receipt = r.Receipt.Clone()
	return r
}

type AlarmBatchTarget struct {
	Alarm   contract.KVAlarm
	Request contract.KVRequest
	Before  *Receipt
	Result  *Receipt
	State   string // succeeded, failed, unknown, not-executed
}
type AlarmBatchReceipt struct {
	DocumentID  uint64
	Scope       contract.KVScope
	Authored    contract.KVRequest
	Observation AlarmObservation
	Targets     []AlarmBatchTarget
	Ended       bool
	Error       string
}

func (r AlarmBatchReceipt) Clone() AlarmBatchReceipt {
	r.Scope = r.Scope.Clone()
	r.Observation = r.Observation.Clone()
	r.Targets = slices.Clone(r.Targets)
	for i := range r.Targets {
		t := &r.Targets[i]
		t.Alarm = t.Alarm.Clone()
		if t.Before != nil {
			copy := t.Before.Clone()
			t.Before = &copy
		}
		if t.Result != nil {
			copy := t.Result.Clone()
			t.Result = &copy
		}
	}
	return r
}

type alarmAuthority struct {
	lease       contract.KVRequestLease
	observation *AlarmObservation
}
type alarmState struct {
	observation                                 *AlarmObservation
	authority                                   *alarmAuthority
	batch                                       *AlarmBatchReceipt
	unknown                                     *Receipt
	recovered                                   []Receipt
	revision                                    uint64
	showResults, showJSON, showUnknown          bool
	row, resultRow, recordView                  int
	columns, resultColumns, confirmationColumns ui.ListState
	preview                                     string
	previewError                                string
	previewRevision                             uint64
	previewCaptured                             bool
	inputRequest                                contract.KVRequest
	inputFilter                                 int32
	inputTargets                                []contract.KVAlarm
	inputError                                  error
	inputCaptured                               bool
	observedPairs                               map[[2]uint64]bool
	viewWidth, viewHeight                       float32
}
type alarmConfirmation struct {
	authority     *alarmAuthority
	receipt       AlarmBatchReceipt
	reads, writes []contract.KVPreparation
	readRequests  []contract.KVRequest
}
type alarmOutcome struct {
	observation *AlarmObservation
	batch       *AlarmBatchReceipt
}

func (d *Document) AlarmsAvailable() bool {
	p, ok := d.lease.(contract.KVMaintenance)
	return ok && slices.Contains(p.MaintenanceMethods(), alarmNativeMethod)
}
func (d *Document) alarmState() *alarmState {
	if d.alarms == nil {
		d.alarms = &alarmState{row: -1, resultRow: -1}
	}
	return d.alarms
}
func (d *Document) AlarmObservation() (AlarmObservation, bool) {
	if d.alarms == nil || d.alarms.observation == nil {
		return AlarmObservation{}, false
	}
	return d.alarms.observation.Clone(), true
}
func (d *Document) AlarmBatchReceipt() (AlarmBatchReceipt, bool) {
	if d.alarms == nil || d.alarms.batch == nil {
		return AlarmBatchReceipt{}, false
	}
	return d.alarms.batch.Clone(), true
}
func (d *Document) AlarmUnknownReceipt() (Receipt, bool) {
	if d.alarms == nil || d.alarms.unknown == nil {
		return Receipt{}, false
	}
	return d.alarms.unknown.Clone(), true
}
func (d *Document) alarmContent() bool {
	a := d.alarms
	return a != nil && (a.observation != nil || a.batch != nil || a.unknown != nil || len(a.recovered) != 0)
}
func (d *Document) alarmRevision() uint64 {
	if d.alarms == nil {
		return 0
	}
	return d.alarms.revision
}
func (d *Document) releaseAlarmAuthority() {
	if d.alarms != nil {
		d.alarms.authority = nil
		d.alarms.revision++
	}
	if d.confirmation != nil && d.confirmation.alarms != nil {
		d.clearConfirmation()
	}
}

// Validate the complete original read before projecting it into selectable
// pairs. Unknown response enums/extensions remain evidence; no endpoint is
// inferred from an alarm's member ID, which may belong to a removed member.
func validAlarmRead(r Receipt, scope contract.KVScope, request contract.KVRequest) bool {
	v := r.Result
	if !sameEventScope(r.Scope, scope) || r.Authored != request || v.Request != request || r.Error != "" || !v.Attempted || !v.Completed || v.WriteOutcomeUnknown || !v.NativeCaptured || v.JSON == "" || v.JSONCaptureError != "" || v.ProjectionError != "" || v.Header.ClusterID != scope.Header.ClusterID || v.Header.MemberID != scope.Header.MemberID || v.Header.RaftTerm == 0 || v.Reply == nil || v.Reply.Kind != "alarm" {
		return false
	}
	seen := map[[2]uint64]bool{}
	for _, a := range v.Reply.Alarms {
		pair := [2]uint64{a.MemberID, uint64(uint32(a.Type))}
		if a.MemberID == 0 || a.Type == 0 || seen[pair] {
			return false
		}
		seen[pair] = true
	}
	return true
}
func alarmObservationReady(d *Document) bool {
	a := d.alarms
	if a == nil || a.authority == nil || a.observation == nil || a.authority.observation != a.observation || a.authority.lease != d.lease || a.observation.DocumentID != d.id || !sameEventScope(a.observation.Scope, d.scope) {
		return false
	}
	filter, _, err := parseAlarmInputs(d.Request())
	return err == nil && filter == a.observation.Filter && validAlarmRead(a.observation.Receipt, d.scope, a.observation.Receipt.Authored)
}
func findAlarm(r Receipt, selected contract.KVAlarm) (contract.KVAlarm, bool) {
	if r.Result.Reply != nil {
		for _, a := range r.Result.Reply.Alarms {
			if a.MemberID == selected.MemberID && a.Type == selected.Type {
				return a.Clone(), true
			}
		}
	}
	return contract.KVAlarm{}, false
}

func (s *State) ObserveAlarms(d *Document) {
	if !s.check(d) || !d.AlarmsAvailable() {
		return
	}
	filter, _, err := parseAlarmInputs(d.Request())
	if err != nil {
		d.error = err.Error()
		return
	}
	if !alarmArchiveRoom(alarmArchiveFor(d), nil, 128<<20) {
		d.error = jsondocument.ErrBounds.Error()
		return
	}
	lease := d.lease
	plan, err := lease.Prepare(alarmReadRequest(filter))
	if err != nil || plan == nil || plan.Writes() || !sameAlarmNativeRequest(plan.Snapshot(), alarmReadRequest(filter)) {
		d.error = jsondocument.ErrInvalid.Error()
		return
	}
	capture := AlarmObservation{DocumentID: d.id, Scope: d.scope.Clone(), Filter: filter}
	request, authored := plan.Snapshot(), d.Request()
	d.releaseAlarmAuthority()
	s.start(d, "alarm-observe", authored, func(ctx context.Context) outcome {
		result, err := lease.Execute(ctx, plan)
		capture.Receipt = Receipt{Scope: capture.Scope.Clone(), Authored: request, Result: result.Clone()}
		if err != nil {
			capture.Receipt.Error = err.Error()
		}
		return outcome{alarms: &alarmOutcome{observation: &capture}, err: err}
	})
}

func (s *State) PrepareAlarmBatch(d *Document) {
	if !s.check(d) || !d.AlarmsAvailable() || d.hasUnknownWrites() {
		return
	}
	if !alarmObservationReady(d) {
		d.error = locales.Text("请先读取原连接的告警列表")
		return
	}
	_, selected, err := parseAlarmInputs(d.Request())
	if err != nil || len(selected) == 0 {
		d.error = jsondocument.ErrInvalid.Error()
		return
	}
	a := d.alarms
	p := &alarmConfirmation{authority: a.authority, receipt: AlarmBatchReceipt{DocumentID: d.id, Scope: d.scope.Clone(), Authored: d.Request(), Observation: a.observation.Clone()}}
	for _, target := range selected {
		original, found := findAlarm(a.observation.Receipt, target)
		if !found {
			d.error = contract.ErrRevoked.Error()
			return
		}
		read, re := d.lease.Prepare(alarmReadRequest(0))
		write, we := d.lease.Prepare(alarmDisarmRequest(original))
		if re != nil || we != nil || read == nil || write == nil || read.Writes() || !write.Writes() || !sameAlarmNativeRequest(read.Snapshot(), alarmReadRequest(0)) || !sameAlarmNativeRequest(write.Snapshot(), alarmDisarmRequest(original)) {
			d.error = jsondocument.ErrInvalid.Error()
			return
		}
		p.reads = append(p.reads, read)
		p.readRequests = append(p.readRequests, read.Snapshot())
		p.writes = append(p.writes, write)
		p.receipt.Targets = append(p.receipt.Targets, AlarmBatchTarget{Alarm: original, Request: write.Snapshot(), State: "not-executed"})
	}
	if !alarmArchiveRoom(alarmArchiveFor(d), &p.receipt, 256<<20) {
		d.error = jsondocument.ErrBounds.Error()
		return
	}
	d.error = ""
	d.clearConfirmation()
	d.confirmation = &confirmation{lease: d.lease, authored: d.Request(), editor: d.Request(), alarms: p, open: true}
	d.permit = nil
}

func validAlarmConfirmation(d *Document, c *confirmation) bool {
	p := c.alarms
	if p == nil || !alarmObservationReady(d) || p.authority != d.alarms.authority || p.receipt.DocumentID != d.id || p.receipt.Authored != d.Request() || !sameEventScope(p.receipt.Scope, d.scope) || !reflect.DeepEqual(p.receipt.Observation, *d.alarms.observation) {
		return false
	}
	_, selected, err := parseAlarmInputs(d.Request())
	if err != nil || len(selected) == 0 || len(selected) != len(p.receipt.Targets) || len(p.reads) != len(selected) || len(p.readRequests) != len(selected) || len(p.writes) != len(selected) {
		return false
	}
	for i, target := range p.receipt.Targets {
		original, found := findAlarm(d.alarms.observation.Receipt, selected[i])
		if !found || !reflect.DeepEqual(target.Alarm, original) || p.reads[i].Writes() || p.reads[i].Snapshot() != p.readRequests[i] || !sameAlarmNativeRequest(p.readRequests[i], alarmReadRequest(0)) || !p.writes[i].Writes() || p.writes[i].Snapshot() != target.Request || !sameAlarmNativeRequest(target.Request, alarmDisarmRequest(original)) {
			return false
		}
	}
	return true
}

func alarmStepReceipt(scope contract.KVScope, request contract.KVRequest, result contract.KVResult, err error) *Receipt {
	r := &Receipt{Scope: scope.Clone(), Authored: request, Result: result.Clone()}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}
func (s *State) executeAlarmBatch(d *Document, c *confirmation) {
	p := c.alarms
	batch, archive := p.receipt.Clone(), alarmArchiveFor(d)
	lease := c.lease
	reads, writes := slices.Clone(p.reads), slices.Clone(p.writes)
	d.clearConfirmation()
	s.start(d, "alarm-batch", c.authored, func(ctx context.Context) outcome {
		var lastErr error
		for i := range batch.Targets {
			target := &batch.Targets[i]
			if lastErr = ctx.Err(); lastErr != nil {
				break
			}
			// A 16 MiB native reply may expand into a much larger complete
			// receipt. Reserve a read and both original/unknown ACK records.
			if !alarmArchiveRoom(archive, &batch, 384<<20) {
				lastErr = jsondocument.ErrBounds
				break
			}
			before, err := lease.Execute(ctx, reads[i])
			target.Before = alarmStepReceipt(batch.Scope, reads[i].Snapshot(), before, err)
			if err != nil || !validAlarmRead(*target.Before, batch.Scope, reads[i].Snapshot()) {
				lastErr = errors.Join(err, contract.ErrRevoked)
				break
			}
			fresh, found := findAlarm(*target.Before, target.Alarm)
			if !found || !reflect.DeepEqual(fresh, target.Alarm) {
				lastErr = contract.ErrRevoked
				break
			}
			if lastErr = ctx.Err(); lastErr != nil {
				break
			}
			if !alarmArchiveRoom(archive, &batch, 256<<20) {
				lastErr = jsondocument.ErrBounds
				break
			}
			result, err := lease.Execute(ctx, writes[i])
			if result.Request != target.Request {
				err = errors.Join(err, contract.ErrRevoked)
			}
			target.Result = alarmStepReceipt(batch.Scope, target.Request, result, err)
			target.State = "failed"
			if result.WriteOutcomeUnknown {
				target.State = "unknown"
			} else if result.Completed {
				target.State = "succeeded"
			}
			// Preserve a genuine completed ACK even with a late publication
			// error, but never continue a batch after that error.
			if err != nil || !result.Completed || result.WriteOutcomeUnknown || !result.NativeCaptured || result.JSONCaptureError != "" || result.ProjectionError != "" {
				lastErr = errors.Join(err, contract.ErrRevoked)
				break
			}
		}
		batch.Ended = true
		if lastErr != nil {
			batch.Error = lastErr.Error()
		}
		return outcome{alarms: &alarmOutcome{batch: &batch}, err: lastErr}
	})
}

func (s *State) consumeAlarms(d *Document, job *operation, out *alarmOutcome) {
	a := d.alarmState()
	if r := out.observation; r != nil && r.DocumentID == d.id && sameEventScope(r.Scope, job.scope) {
		d.releaseAlarmAuthority()
		copy := r.Clone()
		a.observation = &copy
		a.row, a.showResults = -1, false
		s.AlarmsSplitPercent = 70
		a.observedPairs = map[[2]uint64]bool{}
		if copy.Receipt.Result.Reply != nil {
			for _, alarm := range copy.Receipt.Result.Reply.Alarms {
				a.observedPairs[alarmPair(alarm)] = true
			}
		}
		if job.lease == d.lease && !job.canceled && !d.needsRecovery && sameEventScope(copy.Scope, d.scope) && validAlarmRead(copy.Receipt, copy.Scope, copy.Receipt.Authored) {
			a.authority = &alarmAuthority{lease: job.lease, observation: a.observation}
		}
	}
	if r := out.batch; r != nil && r.DocumentID == d.id && sameEventScope(r.Scope, job.scope) && r.Authored == job.authored {
		copy := r.Clone()
		a.batch = &copy
		a.showResults, a.recordView, a.showUnknown, a.resultRow = true, 1, false, 0
		s.AlarmsSplitPercent = 30
		for _, target := range copy.Targets {
			if target.Result != nil && target.Result.Result.WriteOutcomeUnknown && a.unknown == nil {
				unknown := target.Result.Clone()
				a.unknown = &unknown
			}
		}
		for i, target := range copy.Targets {
			if target.State == "failed" || target.State == "unknown" || target.State == "not-executed" {
				a.resultRow = i
				break
			}
		}
		d.releaseAlarmAuthority()
	}
	a.revision++
	d.selectedAlarms, d.selectedMembers = true, false
	d.permit = nil
}

func (s *State) consumeAlarmRecovery(d *Document, job *operation, result *contract.KVResult) {
	if result == nil || result.Request.Method != alarmNativeMethod || !result.Attempted {
		return
	}
	a := d.alarmState()
	r := Receipt{Scope: job.scope.Clone(), Authored: result.Request, Result: result.Clone()}
	for _, old := range a.recovered {
		if reflect.DeepEqual(old, r) {
			return
		}
	}
	a.recovered = append(a.recovered, r)
	if r.Result.WriteOutcomeUnknown && a.unknown == nil {
		unknown := r.Clone()
		a.unknown = &unknown
	}
	a.revision++
	d.permit = nil
}
