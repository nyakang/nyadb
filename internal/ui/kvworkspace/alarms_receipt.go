package kvworkspace

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

const alarmArchiveLimit = 512 << 20

type alarmRecord struct {
	Member     string `json:"member_id"`
	Type       string `json:"original_enum_number"`
	Name       string `json:"alarm"`
	Extensions []byte `json:"native_extensions"`
}
type alarmObservationRecord struct {
	Document string           `json:"original_document_id"`
	Scope    leaseScopeRecord `json:"original_scope"`
	Filter   string           `json:"authored_type_filter"`
	Receipt  json.RawMessage  `json:"original_get_receipt"`
}
type alarmBatchTargetRecord struct {
	Alarm   alarmRecord     `json:"original_alarm"`
	Request requestRecord   `json:"exact_disarm_request"`
	Before  json.RawMessage `json:"fresh_original_get"`
	Result  json.RawMessage `json:"original_disarm_receipt"`
	State   string          `json:"state"`
}
type alarmBatchRecord struct {
	Document    string                   `json:"original_document_id"`
	Scope       leaseScopeRecord         `json:"original_scope"`
	Authored    requestRecord            `json:"authored_selection"`
	Observation *alarmObservationRecord  `json:"original_observation"`
	Targets     []alarmBatchTargetRecord `json:"targets"`
	Ended       bool                     `json:"ended"`
	Error       string                   `json:"error"`
}
type alarmsArchive struct {
	Observation *AlarmObservation
	Batch       *AlarmBatchReceipt
	Unknown     *Receipt
	Recovered   []Receipt
}

func alarmArchiveFor(d *Document) alarmsArchive {
	a := d.alarms
	if a == nil {
		return alarmsArchive{}
	}
	out := alarmsArchive{Recovered: make([]Receipt, len(a.recovered))}
	if a.observation != nil {
		copy := a.observation.Clone()
		out.Observation = &copy
	}
	if a.batch != nil {
		copy := a.batch.Clone()
		out.Batch = &copy
	}
	if a.unknown != nil {
		copy := a.unknown.Clone()
		out.Unknown = &copy
	}
	for i := range a.recovered {
		out.Recovered[i] = a.recovered[i].Clone()
	}
	return out
}
func alarmReceiptData(r *Receipt) (json.RawMessage, error) {
	if r == nil {
		return nil, nil
	}
	raw, err := encodeOperationReceipt(*r)
	return json.RawMessage(raw), err
}
func alarmObservationData(r *AlarmObservation) (*alarmObservationRecord, error) {
	if r == nil {
		return nil, nil
	}
	raw, err := alarmReceiptData(&r.Receipt)
	return &alarmObservationRecord{strconv.FormatUint(r.DocumentID, 10), leaseScope(r.Scope), alarmTypeName(r.Filter), raw}, err
}
func alarmBatchData(b *AlarmBatchReceipt) (*alarmBatchRecord, error) {
	if b == nil {
		return nil, nil
	}
	o, err := alarmObservationData(&b.Observation)
	if err != nil {
		return nil, err
	}
	r := &alarmBatchRecord{Document: strconv.FormatUint(b.DocumentID, 10), Scope: leaseScope(b.Scope), Authored: requestRecord{b.Authored.Method, b.Authored.JSON}, Observation: o, Ended: b.Ended, Error: b.Error}
	for _, target := range b.Targets {
		before, err := alarmReceiptData(target.Before)
		if err != nil {
			return nil, err
		}
		result, err := alarmReceiptData(target.Result)
		if err != nil {
			return nil, err
		}
		a := target.Alarm
		r.Targets = append(r.Targets, alarmBatchTargetRecord{alarmRecord{strconv.FormatUint(a.MemberID, 10), strconv.FormatInt(int64(a.Type), 10), alarmTypeName(a.Type), a.NativeExtensions}, requestRecord{target.Request.Method, target.Request.JSON}, before, result, target.State})
	}
	return r, nil
}
func encodeAlarmsArchive(a alarmsArchive) ([]byte, error) {
	v := struct {
		Format      string                  `json:"format"`
		Observation *alarmObservationRecord `json:"latest_original_get"`
		Batch       *alarmBatchRecord       `json:"last_checked_batch"`
		Unknown     json.RawMessage         `json:"independent_unknown_write_receipt"`
		Recovered   []json.RawMessage       `json:"original_recovered_evidence"`
		Semantics   string                  `json:"semantics"`
	}{Format: "nyadb-original-alarms-v1", Semantics: "Request files and receipt DTOs grant no authority. Every exact Disarm uses the original physical lease and a fresh complete GET before dispatch. Stop on missing/changed pairs, failure, unknown or cancellation; no rollback, retry or replay. GET is cluster-wide and ignores member filters. Alarm member IDs may belong to removed members. Observations do not provide CAS or alarm incarnation protection. A removed-alarm ACK, an already-absent native ACK, and a later empty GET are separate evidence. Later reads and recovery never settle an earlier unknown ACK."}
	var err error
	v.Observation, err = alarmObservationData(a.Observation)
	if err != nil {
		return nil, err
	}
	v.Batch, err = alarmBatchData(a.Batch)
	if err != nil {
		return nil, err
	}
	v.Unknown, err = alarmReceiptData(a.Unknown)
	if err != nil {
		return nil, err
	}
	for _, receipt := range a.Recovered {
		r, err := alarmReceiptData(&receipt)
		if err != nil {
			return nil, err
		}
		v.Recovered = append(v.Recovered, r)
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if len(raw) > alarmArchiveLimit {
		return nil, jsondocument.ErrBounds
	}
	return raw, err
}
func alarmArchiveRoom(a alarmsArchive, b *AlarmBatchReceipt, reserve int) bool {
	if b != nil {
		a.Batch = b
	}
	raw, err := encodeAlarmsArchive(a)
	return err == nil && reserve >= 0 && len(raw) <= alarmArchiveLimit-reserve
}
func (s *State) AlarmsRecord(d *Document) ([]byte, error) {
	if !s.owned(d) {
		return nil, jsondocument.ErrInvalid
	}
	return encodeAlarmsArchive(alarmArchiveFor(d))
}
func (s *State) ExportAlarmsRecord(d *Document) {
	if !s.active(d) || d.FileBusy() || !d.alarmContent() || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil {
		return
	}
	raw, err := s.AlarmsRecord(d)
	if err != nil {
		d.error = err.Error()
		return
	}
	f := s.deps.Files
	s.fileJob(d, "export", func(ctx context.Context) fileResult {
		path, err := f.SavePath("alarms.nyakv-receipt.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
