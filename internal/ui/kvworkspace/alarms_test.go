package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type alarmTestLease struct {
	*fakeLease
	mu               sync.Mutex
	alarms           []contract.KVAlarm
	requests         []contract.KVRequest
	mode             string
	writes           int
	entered, release chan struct{}
	last             contract.KVResult
}
type alarmTestPlan struct {
	owner   *alarmTestLease
	request contract.KVRequest
	write   bool
	used    atomic.Bool
}

func (p *alarmTestPlan) Snapshot() contract.KVRequest { return p.request }
func (p *alarmTestPlan) Writes() bool                 { return p.write }
func (*alarmTestLease) MaintenanceMethods() []string  { return []string{alarmNativeMethod} }
func (l *alarmTestLease) Prepare(r contract.KVRequest) (contract.KVPreparation, error) {
	if r.Method != alarmNativeMethod {
		return l.fakeLease.Prepare(r)
	}
	var v map[string]any
	if json.Unmarshal([]byte(r.JSON), &v) != nil {
		return nil, errors.New("invalid test request")
	}
	return &alarmTestPlan{owner: l, request: r, write: v["action"] == "DEACTIVATE"}, nil
}
func (l *alarmTestLease) LastResult() contract.KVResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last.Clone()
}
func (l *alarmTestLease) Execute(ctx context.Context, plan contract.KVPreparation) (contract.KVResult, error) {
	p, ok := plan.(*alarmTestPlan)
	if !ok {
		return l.fakeLease.Execute(ctx, plan)
	}
	if p.owner != l || p.used.Swap(true) {
		return contract.KVResult{}, contract.ErrKVPlanUsed
	}
	l.mu.Lock()
	l.requests = append(l.requests, p.request)
	v := contract.KVResult{Request: p.request, Attempted: true, Completed: true, Code: "OK", NativeCaptured: true, Native: []byte{0, 255, 3}, JSON: `{"alarms":[]}`, Header: l.scope.Header, Reply: &contract.KVReply{Kind: "alarm"}}
	var request struct{ Action, Alarm, MemberID string }
	var fields map[string]string
	_ = json.Unmarshal([]byte(p.request.JSON), &fields)
	request.Action, request.Alarm, request.MemberID = fields["action"], fields["alarm"], fields["memberID"]
	kind, _ := alarmTypeValue(request.Alarm)
	if !p.write {
		for _, a := range l.alarms {
			if kind == 0 || a.Type == kind {
				v.Reply.Alarms = append(v.Reply.Alarms, a.Clone())
			}
		}
		if l.mode == "foreign-read" && len(l.requests) > 1 {
			v.Header.ClusterID = 42
		}
		l.last = v.Clone()
		l.mu.Unlock()
		return v, nil
	}
	l.writes++
	mode, number := l.mode, l.writes
	if mode == "denied" && number == 2 {
		v.Completed, v.Code = false, "PermissionDenied"
		l.last = v.Clone()
		l.mu.Unlock()
		return v, errors.New("actual denied")
	}
	for i, a := range l.alarms {
		if a.Type == kind && request.MemberID == alarmDisarmMember(a) {
			v.Reply.Alarms = append(v.Reply.Alarms, a.Clone())
			l.alarms = append(l.alarms[:i], l.alarms[i+1:]...)
			break
		}
	}
	l.mu.Unlock()
	if mode == "wait" {
		close(l.entered)
		<-l.release // Explicitly wait for the original executor's acknowledgement.
	}
	var err error
	if mode == "unknown" && number == 2 || mode == "wait" && ctx.Err() != nil {
		v.Completed, v.WriteOutcomeUnknown, v.NativeCaptured, v.Native, v.JSON = false, true, false, nil, ""
		v.Code = "Unavailable"
		err = errors.New("original completed write acknowledgement withheld")
	}
	l.mu.Lock()
	l.last = v.Clone()
	l.mu.Unlock()
	return v, err
}
func alarmDisarmMember(a contract.KVAlarm) string {
	var fields map[string]string
	_ = json.Unmarshal([]byte(alarmDisarmRequest(a).JSON), &fields)
	return fields["memberID"]
}
func alarmTestState(t *testing.T) (*State, *Document, *alarmTestLease, *fakeProvider) {
	t.Helper()
	l := &alarmTestLease{fakeLease: &fakeLease{scope: testScope()}, alarms: []contract.KVAlarm{{MemberID: math.MaxUint64, Type: 1, NativeExtensions: []byte{0, 255}}, {MemberID: 3, Type: 2}, {MemberID: 4, Type: 1}, {MemberID: 5, Type: 91}}}
	p := &fakeProvider{lease: l.fakeLease, reserve: func(uint64) (contract.KVRequestLease, error) { return l, nil }}
	s := New(Dependencies{ProviderFor: func(int64) (contract.KVRequestsProvider, error) { return p, nil }})
	d := s.Open(1, 7, DefaultAlarmsRequest())
	t.Cleanup(s.CloseAll)
	return s, d, l, p
}
func prepareAlarmTestBatch(t *testing.T, s *State, d *Document, targets ...contract.KVAlarm) {
	t.Helper()
	s.SetAlarmSelection(d, 0, targets)
	s.ObserveAlarms(d)
	consume(t, s, d)
	s.PrepareAlarmBatch(d)
	if d.confirmation == nil {
		t.Fatal("original alarm confirmation unavailable", d.Error())
	}
}
func TestAlarmsTextOnlyExactUnsignedSelectionAndPrivateObservationAuthority(t *testing.T) {
	s, d, l, _ := alarmTestState(t)
	s.SetAlarmSelection(d, 0, []contract.KVAlarm{{MemberID: math.MaxUint64, Type: 1}})
	raw, err := EncodeRequest(d.Request())
	r, decode := DecodeRequest(raw)
	if err != nil || decode != nil || r != d.Request() {
		t.Fatal("exact request text changed", err, decode)
	}
	copy := s.Open(2, 7, r)
	s.PrepareAlarmBatch(copy)
	if copy.confirmation != nil || len(l.requests) != 0 {
		t.Fatal("file granted authority or performed I/O")
	}
	for _, text := range []string{
		`{"alarm":"NONE","targets":null}`, `{"alarm":"NONE","targets":[],"receipt":{}}`, `{"alarm":"NONE","alarm":"NONE","targets":[]}`,
		`{"alarm":"NONE","targets":[{"member_id":18446744073709551615,"alarm":"NOSPACE"}]}`,
		`{"alarm":"NONE","targets":[{"member_id":"18446744073709551616","alarm":"NOSPACE"}]}`,
		`{"alarm":"NONE","targets":[{"member_id":"0","alarm":"NOSPACE"}]}`,
		`{"alarm":"NONE","targets":[{"member_id":"3","alarm":"NONE"}]}`,
		`{"alarm":"91","targets":[]}`, `{"alarm":"NONE","member_id":"3","targets":[]}`,
		`{"alarm":"NOSPACE","targets":[{"member_id":"3","alarm":"CORRUPT"}]}`,
		`{"alarm":"NONE","targets":[{"member_id":"3","alarm":"NOSPACE"},{"member_id":"03","alarm":"NOSPACE"}]}`,
	} {
		if _, _, err := parseAlarmInputs(contract.KVRequest{Method: AlarmsMethod, JSON: text}); err == nil {
			t.Fatal("unsafe authored input accepted", text)
		}
	}
	s.ObserveAlarms(d)
	consume(t, s, d)
	observation, _ := d.AlarmObservation()
	copy.alarmState().observation = &observation
	s.PrepareAlarmBatch(copy)
	if copy.confirmation != nil {
		t.Fatal("copied DTO granted observation authority")
	}
	observation.Receipt.Result.Reply.Alarms[0].NativeExtensions[0] = 9
	if d.alarms.observation.Receipt.Result.Reply.Alarms[0].NativeExtensions[0] != 0 {
		t.Fatal("observation clone aliases native extensions")
	}
	for _, method := range s.MethodsFor(d) {
		if method.Path == AlarmsMethod {
			t.Fatal("private Alarm foundation publicly advertised")
		}
	}
}
func TestAlarmsCheckedBatchUsesFreshOriginalGetsAndStopsMissingOrChangedPair(t *testing.T) {
	for _, mode := range []string{"complete", "missing", "changed", "foreign-read", "editor-change", "lease-change"} {
		t.Run(mode, func(t *testing.T) {
			s, d, l, _ := alarmTestState(t)
			prepareAlarmTestBatch(t, s, d, contract.KVAlarm{MemberID: math.MaxUint64, Type: 1}, contract.KVAlarm{MemberID: 3, Type: 2})
			if len(l.requests) != 1 {
				t.Fatal("pure preparation performed I/O")
			}
			s.Confirm(d, false)
			if d.Busy() {
				t.Fatal("unchecked confirmation dispatched")
			}
			switch mode {
			case "missing":
				l.alarms = l.alarms[1:]
			case "changed":
				l.alarms[0].NativeExtensions = []byte{7}
			case "foreign-read":
				l.mode = mode
			case "editor-change":
				d.Editor.Buffer.Set(DefaultAlarmsRequest().JSON)
			case "lease-change":
				d.lease = &fakeLease{scope: testScope()}
			}
			s.Confirm(d, true)
			consume(t, s, d)
			if mode == "editor-change" || mode == "lease-change" {
				if l.writes != 0 || d.confirmation != nil {
					t.Fatal("stale confirmation executed")
				}
				return
			}
			b, ok := d.AlarmBatchReceipt()
			if !ok || !b.Ended {
				t.Fatal("original batch not retained")
			}
			if mode != "complete" {
				if l.writes != 0 || b.Targets[0].State != "not-executed" || b.Targets[0].Before == nil || b.Targets[0].Result != nil || b.Error == "" {
					t.Fatal("missing/changed/foreign target sent Disarm")
				}
				return
			}
			if l.writes != 2 || len(l.requests) != 5 || b.Targets[0].State != "succeeded" || b.Targets[1].State != "succeeded" || len(b.Targets[0].Before.Result.Reply.Alarms) != 4 || len(b.Targets[1].Before.Result.Reply.Alarms) != 3 {
				t.Fatal("fresh original sequence or removed ACK lost")
			}
			if d.alarms.authority != nil {
				t.Fatal("completed batch retained reusable authority")
			}
			b.Targets[0].Result.Result.Native[0] = 99
			if d.alarms.batch.Targets[0].Result.Result.Native[0] != 0 {
				t.Fatal("batch clone aliases original native evidence")
			}
		})
	}
}
func TestAlarmsPartialFailureUnknownAndLaterReadRemainSeparate(t *testing.T) {
	for _, mode := range []string{"denied", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, d, l, _ := alarmTestState(t)
			l.mode = mode
			prepareAlarmTestBatch(t, s, d, contract.KVAlarm{MemberID: math.MaxUint64, Type: 1}, contract.KVAlarm{MemberID: 3, Type: 2}, contract.KVAlarm{MemberID: 4, Type: 1})
			s.Confirm(d, true)
			consume(t, s, d)
			b, _ := d.AlarmBatchReceipt()
			want := "failed"
			if mode == "unknown" {
				want = "unknown"
			}
			if l.writes != 2 || b.Targets[0].State != "succeeded" || b.Targets[1].State != want || b.Targets[2].State != "not-executed" || b.Targets[2].Before != nil {
				t.Fatal("batch continued or partial outcomes lost")
			}
			s.ObserveAlarms(d)
			consume(t, s, d)
			if d.alarms.batch.Targets[1].State != want || d.hasUnknownWrites() != (mode == "unknown") {
				t.Fatal("later GET settled original write")
			}
			if mode == "unknown" {
				s.PrepareAlarmBatch(d)
				if d.confirmation != nil {
					t.Fatal("unknown allowed another Alarm write")
				}
				s.SetMethod(d, "/etcdserverpb.KV/Put")
				d.Editor.Buffer.Set(`{"key":"eA==","value":"eQ=="}`)
				s.Run(d)
				if d.confirmation != nil {
					t.Fatal("Alarm unknown allowed KV write")
				}
			}
			raw, err := s.AlarmsRecord(d)
			if err != nil || !bytes.Contains(raw, []byte(`"member_id": "18446744073709551615"`)) || !bytes.Contains(raw, []byte("original_disarm_receipt")) || !bytes.Contains(raw, []byte("not-executed")) {
				t.Fatal("complete unsigned archive lost", err)
			}
		})
	}
}
func TestAlarmsCancellationWaitsParkingPureRecoveryAndLeaveRevisions(t *testing.T) {
	s, d, l, p := alarmTestState(t)
	l.mode = "wait"
	l.entered = make(chan struct{})
	l.release = make(chan struct{})
	prepareAlarmTestBatch(t, s, d, contract.KVAlarm{MemberID: math.MaxUint64, Type: 1}, contract.KVAlarm{MemberID: 3, Type: 2})
	s.Confirm(d, true)
	await(t, l.entered)
	s.RequestCancel(d)
	s.Consume()
	if !d.Busy() || !d.CancelRequested() || s.GuardLeave(d, nil) == false {
		t.Fatal("cancel did not wait for original ACK")
	}
	active := false
	s.deps.Active = func(*Document) bool { return active }
	close(l.release)
	await(t, d.job.finished)
	s.Consume()
	if !d.Busy() || d.alarms.batch != nil {
		t.Fatal("parked outcome consumed")
	}
	active = true
	consume(t, s, d)
	if !d.hasUnknownWrites() || l.writes != 1 || d.alarms.batch.Targets[1].State != "not-executed" {
		t.Fatal("original cancelled partial batch lost")
	}
	old := d.alarms.unknown.Clone()
	recovered := &alarmTestLease{fakeLease: &fakeLease{scope: testScope()}, alarms: []contract.KVAlarm{{MemberID: 3, Type: 2}}}
	recovered.scope.Generation++
	p.recover = func(original contract.KVRequestLease) (contract.KVRecovery, error) {
		if original != l {
			t.Error("recovered another physical lease")
		}
		return contract.KVRecovery{Lease: recovered, Last: old.Result.Clone()}, nil
	}
	d.needsRecovery = true
	s.Recover(d)
	consume(t, s, d)
	if len(recovered.requests) != 0 || recovered.connects.Load() != 0 || d.alarms.authority != nil || !reflect.DeepEqual(*d.alarms.unknown, old) || !d.hasUnknownWrites() || len(d.alarms.recovered) != 1 {
		t.Fatal("recovery replayed or changed original unknown")
	}
	s.ObserveAlarms(d)
	consume(t, s, d)
	if !d.hasUnknownWrites() || len(recovered.requests) != 1 {
		t.Fatal("recovered explicit GET released unknown")
	}
	called := false
	s.GuardLeave(d, func(ui.Services) { called = true })
	s.DecideLeave(ui.Services{}, true)
	if !called || s.GuardLeave(d, nil) {
		t.Fatal("original leave permission not retained")
	}
	s.ObserveAlarms(d)
	consume(t, s, d)
	if !s.GuardLeave(d, nil) {
		t.Fatal("changed receipt reused leave permission")
	}
	s.DecideLeave(ui.Services{}, false)
	if !d.hasUnknownWrites() {
		t.Fatal("cancelled leave cleared evidence")
	}
	s.Close(d)
	s.Close(d)
	if d.alarms != nil || d.lease != nil || d.Editor != nil {
		t.Fatal("close retained alarm caches")
	}
}
func TestAlarmsArchiveReservationAndUnknownObservedTypes(t *testing.T) {
	s, d, l, _ := alarmTestState(t)
	s.ObserveAlarms(d)
	consume(t, s, d)
	if !alarmObservationReady(d) || d.alarms.observation.Receipt.Result.Reply.Alarms[3].Type != 91 {
		t.Fatal("unknown reply enum cannot be inspected")
	}
	s.SetAlarmSelection(d, 0, []contract.KVAlarm{{MemberID: 5, Type: 91}})
	if !strings.Contains(d.Error(), "invalid") {
		t.Fatal("unknown selected enum accepted")
	}
	if alarmArchiveRoom(alarmArchiveFor(d), nil, alarmArchiveLimit) || alarmArchiveRoom(alarmArchiveFor(d), nil, -1) {
		t.Fatal("archive reservation bypassed")
	}
	if len(l.requests) != 1 {
		t.Fatal("reservation performed native I/O")
	}
}

func TestAlarmsNativeCanonicalMemberIdentityAndAmbiguousAliases(t *testing.T) {
	original := contract.KVAlarm{MemberID: math.MaxUint64, Type: 1}
	request := alarmDisarmRequest(original)
	canonical := contract.KVRequest{Method: alarmNativeMethod, JSON: `{"action":"DEACTIVATE","memberID":"18446744073709551615","alarm":"NOSPACE"}`}
	if !sameAlarmNativeRequest(canonical, request) || !sameAlarmNativeRequest(contract.KVRequest{Method: alarmNativeMethod, JSON: `{}`}, alarmReadRequest(0)) {
		t.Fatal("official canonical memberID or omitted GET/NONE defaults rejected")
	}
	for _, raw := range []string{
		`{"action":"DEACTIVATE","memberID":"18446744073709551615","member_id":"1","alarm":"NOSPACE"}`,
		`{"action":"DEACTIVATE","memberID":18446744073709551615,"alarm":"NOSPACE"}`,
		`{"action":"DEACTIVATE","memberID":"18446744073709551615","alarm":"CORRUPT"}`,
		`{"action":"ACTIVATE","memberID":"18446744073709551615","alarm":"NOSPACE"}`,
	} {
		if sameAlarmNativeRequest(contract.KVRequest{Method: alarmNativeMethod, JSON: raw}, request) {
			t.Fatal("ambiguous or changed canonical request accepted", raw)
		}
	}
}

func TestAlarmsOpenRequestRevokesAuthorityAndExportCapturesCompleteOriginalRecord(t *testing.T) {
	s, d, _, _ := alarmTestState(t)
	prepareAlarmTestBatch(t, s, d, contract.KVAlarm{MemberID: math.MaxUint64, Type: 1})
	request, err := EncodeRequest(d.Request())
	if err != nil {
		t.Fatal(err)
	}
	unknown := Receipt{Scope: d.scope.Clone(), Authored: alarmDisarmRequest(contract.KVAlarm{MemberID: 3, Type: 2}), Result: contract.KVResult{WriteOutcomeUnknown: true, Native: []byte{0, 255}}}
	d.alarms.unknown = &unknown
	s.SetFiles(Files{OpenPath: func() (string, error) { return "original.nyakv", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(request)), nil }})
	s.OpenRequest(d)
	consume(t, s, d)
	if d.alarms.authority != nil || d.confirmation != nil || !reflect.DeepEqual(*d.alarms.unknown, unknown) || d.alarms.observation == nil {
		t.Fatal("request file restored authority or discarded original evidence")
	}
	expected, err := s.AlarmsRecord(d)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var written []byte
	s.SetFiles(Files{SavePath: func(string) (string, error) { close(entered); <-release; return "alarms.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var buffer bytes.Buffer
		err := write(&buffer)
		written = bytes.Clone(buffer.Bytes())
		return err
	}})
	s.ExportAlarmsRecord(d)
	await(t, entered)
	d.alarms.observation.Receipt.Result.Native = []byte{9}
	close(release)
	consume(t, s, d)
	if !bytes.Equal(expected, written) {
		t.Fatal("late export changed its complete original snapshot")
	}
}
