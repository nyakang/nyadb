package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type destroyTestPlan struct {
	request contract.KVRequest
	preview contract.KVHTTPSessionDestroyImpact
	closed  atomic.Bool
}

func (p *destroyTestPlan) Snapshot() contract.KVRequest                { return p.request }
func (*destroyTestPlan) LocalRecordID() string                         { return "original-client-record" }
func (*destroyTestPlan) Writes() bool                                  { return true }
func (p *destroyTestPlan) Impact() contract.KVHTTPSessionDestroyImpact { return p.preview.Clone() }
func (p *destroyTestPlan) Close() error                                { p.closed.Store(true); return nil }

type destroyTestLease struct {
	*sessionKeysTestLease
	plan    *destroyTestPlan
	prepare func(context.Context, contract.KVHTTPSessionDestroyImpactRequest) (contract.KVHTTPSessionDestroyPreparation, error)
	last    contract.KVResult
}

func (l *destroyTestLease) PrepareHTTPSessionDestroy(ctx context.Context, r contract.KVHTTPSessionDestroyImpactRequest) (contract.KVHTTPSessionDestroyPreparation, error) {
	return l.prepare(ctx, r)
}
func (l *destroyTestLease) LastResult() contract.KVResult { return l.last.Clone() }

func destroyTestState(t *testing.T) (*State, *Document, *destroyTestLease) {
	s, d, keys := sessionKeysTestState(t)
	request := contract.KVHTTPSessionDestroyImpactRequest{ID: sessionKeysTestID, MaxRows: 10000, MaxBytes: 32 << 20}
	o := contract.KVHTTPSessionDestroyImpact{Scope: d.scope.Clone(), Request: request, Bytes: 8192, Complete: true, Keys: sessionKeysTestObservation(d.scope, contract.KVHTTPSessionKeysRequest{ID: request.ID, MaxRows: request.MaxRows, MaxBytes: request.MaxBytes}, 53)}
	o.Session = sessionBrowserReceipt([]contract.KVHTTPSession{{ID: sessionKeysTestID, Behavior: "delete", PresentFields: []string{"ID", "Behavior"}}}).Result
	o.Session.Request = contract.KVRequest{Method: contract.KVConsulHTTPRequestMethod, JSON: `{"method":"GET","path":"/v1/session/info/` + sessionKeysTestID + `"}`}
	o.Session.Attempted, o.Session.Completed = true, true
	o.Session.HTTP.Body = []byte(`[{"ID":"` + sessionKeysTestID + `","Behavior":"delete","future":null}]`)
	o.Session.HTTP.Kind = "read"
	o.Session.HTTP.Request = contract.KVHTTPRequest{Method: "GET", Path: "/v1/session/info/" + sessionKeysTestID}
	o.Session.HTTP.Attempted, o.Session.HTTP.Ended = true, true
	for i := range o.Keys.Root.HTTP.Entries {
		o.Keys.Root.HTTP.Entries[i].PresentFields = append(o.Keys.Root.HTTP.Entries[i].PresentFields, "ModifyIndex")
	}
	p := &destroyTestPlan{request: contract.KVRequest{Method: contract.KVConsulHTTPRequestMethod, JSON: `{"method":"PUT","path":"/v1/session/destroy/` + sessionKeysTestID + `"}`}, preview: o}
	l := &destroyTestLease{sessionKeysTestLease: keys, plan: p}
	l.prepare = func(context.Context, contract.KVHTTPSessionDestroyImpactRequest) (contract.KVHTTPSessionDestroyPreparation, error) {
		return p, nil
	}
	d.lease = l
	return s, d, l
}

func TestHTTPSessionDestroyPreviewParksOriginalSelectionAndExecutesOnceWithCompleteRecords(t *testing.T) {
	s, d, l := destroyTestState(t)
	started, release := make(chan struct{}), make(chan struct{})
	l.prepare = func(_ context.Context, r contract.KVHTTPSessionDestroyImpactRequest) (contract.KVHTTPSessionDestroyPreparation, error) {
		if r != l.plan.preview.Request {
			t.Error("changed captured request")
		}
		close(started)
		<-release
		return l.plan, nil
	}
	original := d.Request()
	s.PrepareSelectedHTTPSessionDestroy(d)
	finished := d.job.finished
	await(t, started)
	d.httpSessionBrowser.selectRow(d.last, 0, 1)
	active := false
	s.deps.Active = func(*Document) bool { return active }
	close(release)
	await(t, finished)
	s.Consume()
	if d.job == nil || d.confirmation != nil || l.plan.closed.Load() {
		t.Fatal("parked authority consumed")
	}
	active = true
	s.Consume()
	if d.confirmation == nil || d.confirmation.destroy.preview.Request.ID != sessionKeysTestID || d.Request() != original {
		t.Fatal("preview lost original selection or editor", d.Error())
	}
	l.execute = func(_ context.Context, p contract.KVPreparation) (contract.KVResult, error) {
		if p != l.plan || l.plan.closed.Load() {
			t.Error("authority revoked before executor")
		}
		return contract.KVResult{Request: p.Snapshot(), Attempted: true, Completed: true, HTTP: &contract.KVHTTPResult{Body: []byte("false"), Request: contract.KVHTTPRequest{Method: "PUT", Path: "/v1/session/destroy/" + sessionKeysTestID}, Acknowledged: true, Captured: true, EOF: true, Status: 200}, HTTPSessionDestroy: &contract.KVHTTPSessionDestroyEvidence{LocalRecordID: l.plan.LocalRecordID(), Preview: l.plan.preview.Clone(), Recheck: l.plan.preview.Clone(), Revalidated: true}}, nil
	}
	s.Confirm(d, false)
	if d.Busy() || l.executes.Load() != 0 {
		t.Fatal("unchecked confirmation executed")
	}
	s.Confirm(d, true)
	s.Confirm(d, true)
	consume(t, s, d)
	if l.executes.Load() != 1 || !l.plan.closed.Load() || d.confirmation != nil || string(d.last.Result.HTTP.Body) != "false" {
		t.Fatal("plan replayed or native false rewritten")
	}
	raw, err := s.OperationRecord(d)
	if err != nil || !bytes.Contains(raw, []byte(`"ModifyIndex": "9007199254740993"`)) {
		t.Fatal("original unsigned key versions lost", err)
	}
	var record struct {
		Preview, Recheck struct {
			Root struct{ HTTP struct{ Body []byte } }
		}
		Write struct{ HTTP struct{ Body []byte } } `json:"original_put"`
	}
	if json.Unmarshal(raw, &record) != nil || !bytes.Equal(record.Preview.Root.HTTP.Body, l.plan.preview.Keys.Root.HTTP.Body) || !bytes.Equal(record.Recheck.Root.HTTP.Body, l.plan.preview.Keys.Root.HTTP.Body) || string(record.Write.HTTP.Body) != "false" {
		t.Fatal("complete original steps lost")
	}
	clone := d.last.Clone()
	clone.Result.HTTPSessionDestroy.Preview.Keys.Root.HTTP.Body[0] = '!'
	if bytes.Equal(clone.Result.HTTPSessionDestroy.Preview.Keys.Root.HTTP.Body, d.last.Result.HTTPSessionDestroy.Preview.Keys.Root.HTTP.Body) {
		t.Fatal("receipt aliases captured bytes")
	}
}

func TestHTTPSessionDestroyPendingPlansRevokeOnEditsReplacementDisconnectAndClose(t *testing.T) {
	for _, action := range []string{"cancel", "edit-revert", "method", "replace", "disconnect", "close"} {
		t.Run(action, func(t *testing.T) {
			s, d, l := destroyTestState(t)
			s.PrepareSelectedHTTPSessionDestroy(d)
			consume(t, s, d)
			if d.confirmation == nil {
				t.Fatal(d.Error())
			}
			switch action {
			case "cancel":
				s.CancelConfirmation(d)
			case "edit-revert":
				original := d.Request().JSON
				d.Editor.Buffer.Set(original + " ")
				d.Editor.Buffer.Set(original)
				s.Consume()
			case "method":
				s.SetMethod(d, "Range")
			case "replace":
				s.Run(d)
				consume(t, s, d)
			case "disconnect":
				s.DisconnectProfile(d.profileID)
			case "close":
				s.Close(d)
			}
			if !l.plan.closed.Load() {
				t.Fatal("unused original plan leaked")
			}
			s.Confirm(d, true)
			if action != "replace" && l.executes.Load() != 0 {
				t.Fatal("revoked destroy authority replayed")
			}
		})
	}
}

func TestHTTPSessionDestroyCancelledAndFailedPreviewsRetainEvidenceWithoutAuthority(t *testing.T) {
	for _, action := range []string{"cancel", "partial", "acl", "foreign-scope", "changed-editor", "unknown", "close-unconsumed"} {
		t.Run(action, func(t *testing.T) {
			s, d, l := destroyTestState(t)
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			l.prepare = func(ctx context.Context, _ contract.KVHTTPSessionDestroyImpactRequest) (contract.KVHTTPSessionDestroyPreparation, error) {
				close(started)
				if action == "cancel" {
					<-ctx.Done()
					close(canceled)
				}
				<-release
				return l.plan, nil
			}
			if action == "unknown" {
				d.unknown = &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true}}
				s.PrepareSelectedHTTPSessionDestroy(d)
				if d.job != nil {
					t.Fatal("unknown write admitted another plan")
				}
				return
			}
			s.PrepareSelectedHTTPSessionDestroy(d)
			finished := d.job.finished
			await(t, started)
			switch action {
			case "cancel":
				s.RequestCancel(d)
				await(t, canceled)
				s.Consume()
				if !d.Busy() {
					t.Fatal("cancellation did not await executor")
				}
			case "partial":
				l.plan.preview.Complete = false
			case "acl":
				l.plan.preview.Keys.Root.HTTP.Metadata.ACLFiltered = new(true)
			case "foreign-scope":
				l.plan.preview.Scope.Generation++
			case "changed-editor":
				d.Editor.Buffer.Set(d.Request().JSON + " ")
			}
			close(release)
			await(t, finished)
			if action == "close-unconsumed" {
				s.Close(d)
			} else {
				s.Consume()
			}
			if !l.plan.closed.Load() || d.confirmation != nil || l.executes.Load() != 0 {
				t.Fatal("invalid late preview retained write authority")
			}
			if action != "foreign-scope" && action != "close-unconsumed" && (d.last.Result.HTTPSessionDestroy == nil || len(d.last.Result.HTTPSessionDestroy.Preview.Session.HTTP.Body) == 0) {
				t.Fatal("original failed/cancelled preview evidence lost")
			}
		})
	}
	// A provider can return no authority but preserve its exact failed preview.
	s, d, l := destroyTestState(t)
	l.last = contract.KVResult{Request: l.plan.request, HTTPSessionDestroy: &contract.KVHTTPSessionDestroyEvidence{LocalRecordID: l.plan.LocalRecordID(), Preview: l.plan.preview.Clone()}}
	l.prepare = func(context.Context, contract.KVHTTPSessionDestroyImpactRequest) (contract.KVHTTPSessionDestroyPreparation, error) {
		l.last.HTTPSessionDestroy.LocalRecordID = "new-failed-record"
		return nil, errors.New("partial")
	}
	s.PrepareSelectedHTTPSessionDestroy(d)
	consume(t, s, d)
	if d.confirmation != nil || d.last.Result.HTTPSessionDestroy.LocalRecordID != "new-failed-record" || !reflect.DeepEqual(d.last.Result.HTTPSessionDestroy.Preview, l.plan.preview) {
		t.Fatal("failed preparation receipt lost or authorized")
	}
}
