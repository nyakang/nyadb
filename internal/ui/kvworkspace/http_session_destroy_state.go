package kvworkspace

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/locales"
)

type httpSessionDestroyConfirmation struct {
	plan     contract.KVHTTPSessionDestroyPreparation
	preview  contract.KVHTTPSessionDestroyImpact
	recordID string
	behavior string
	page     int
}

// All confirmation replacement paths release unused original authority. An
// executor transfers its plan out of this field before starting the worker.
func (d *Document) clearConfirmation() {
	if d.httpACLMutation != nil {
		d.clearHTTPACLMutationConfirmation()
	}
	if p := d.confirmation; p != nil && p.destroy != nil && p.destroy.plan != nil {
		_ = p.destroy.plan.Close()
		p.destroy.plan = nil
	}
	d.confirmation = nil
}

func (d *Document) HTTPSessionDestroyAvailable() bool {
	_, ok := d.lease.(contract.KVHTTPSessionDestroyPlanning)
	return !d.closed && d.scope.HTTP.Protocol == "consul-http" && ok
}

func validDestroyRequest(r contract.KVRequest, id string) bool {
	var fields map[string]json.RawMessage
	var method, path string
	return r.Method == contract.KVConsulHTTPRequestMethod && jsondocument.Decode([]byte(r.JSON), &fields, 4096) == nil && len(fields) == 2 && json.Unmarshal(fields["method"], &method) == nil && json.Unmarshal(fields["path"], &path) == nil && method == "PUT" && path == "/v1/session/destroy/"+id
}

// Defensive presentation validation grants no native authority. The owning
// provider alone revalidates complete native evidence before its one PUT.
func destroyPreviewBehavior(o contract.KVHTTPSessionDestroyImpact) (string, bool) {
	h := o.Session.HTTP
	if !o.Complete || !validHTTPSessionUUID(o.Request.ID) || o.Bytes < 1 || o.Bytes > o.Request.MaxBytes || o.Keys.Request != (contract.KVHTTPSessionKeysRequest{ID: o.Request.ID, MaxRows: o.Request.MaxRows, MaxBytes: o.Request.MaxBytes}) || !reflect.DeepEqual(o.Scope, o.Keys.Scope) || !o.Keys.Complete || !validHTTPSessionKeysProjection(o.Keys) || h == nil || h.Kind != "read" || h.Request.Method != "GET" || h.Request.Path != "/v1/session/info/"+o.Request.ID || h.Request.Body != nil || len(h.Request.Query) != 0 || h.Status != 200 || !o.Session.Attempted || !o.Session.Completed || o.Session.WriteOutcomeUnknown || !h.Attempted || !h.Ended || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded || h.WriteOutcomeUnknown || h.CaptureError != "" || h.ProjectionError != "" || o.Session.ProjectionError != "" || h.Sessions == nil || len(h.Sessions.Rows) != 1 || h.Metadata.ACLFiltered != nil && *h.Metadata.ACLFiltered {
		return "", false
	}
	row := h.Sessions.Rows[0]
	if row.ID != o.Request.ID || !slices.Contains(row.PresentFields, "ID") || !slices.Contains(row.PresentFields, "Behavior") || slices.Contains(row.NullFields, "ID") || slices.Contains(row.NullFields, "Behavior") || row.Behavior != "release" && row.Behavior != "delete" {
		return "", false
	}
	keys := make(map[string]bool, len(o.Keys.Matches))
	for _, i := range o.Keys.Matches {
		key := o.Keys.Root.HTTP.Entries[i]
		if key.Key == "" || keys[key.Key] || !slices.Contains(key.PresentFields, "ModifyIndex") {
			return "", false
		}
		keys[key.Key] = true
	}
	return row.Behavior, true
}

func (s *State) PrepareSelectedHTTPSessionDestroy(d *Document) {
	if !s.check(d) || !d.HTTPSessionDestroyAvailable() || d.httpSessionBrowser == nil || d.hasUnknownWrites() {
		return
	}
	r := s.selectedReceipt(d)
	row, ok := d.httpSessionBrowser.selected(r)
	if !ok || !validHTTPSessionUUID(row.ID) || r.Scope.Generation != d.scope.Generation || !sameKVRecoveryIdentity(r.Scope, d.scope) {
		d.error = locales.Text("所选 Session 不属于当前原连接观察")
		return
	}
	d.clearConfirmation()
	lease := d.lease
	facet := lease.(contract.KVHTTPSessionDestroyPlanning)
	input := contract.KVHTTPSessionDestroyImpactRequest{ID: row.ID, MaxRows: contract.KVHTTPSessionKeysMaxRows, MaxBytes: contract.KVHTTPSessionKeysMaxBytes}
	editorRequest, revision := d.Request(), d.Editor.Buffer.Version()
	previousID := ""
	if e := lease.LastResult().HTTPSessionDestroy; e != nil {
		previousID = e.LocalRecordID
	}
	s.start(d, "http-session-destroy-preview", editorRequest, func(ctx context.Context) outcome {
		plan, err := facet.PrepareHTTPSessionDestroy(ctx, input)
		result := contract.KVResult{}
		if plan != nil {
			result.Request = plan.Snapshot()
			result.HTTPSessionDestroy = &contract.KVHTTPSessionDestroyEvidence{LocalRecordID: plan.LocalRecordID(), Preview: plan.Impact()}
		} else if err != nil {
			last := lease.LastResult()
			if e := last.HTTPSessionDestroy; e != nil && e.LocalRecordID != "" && e.LocalRecordID != previousID && e.Preview.Request == input {
				result = last
			}
		}
		return outcome{destroyPlan: plan, result: result, err: err}
	})
	d.job.destroyRequest, d.job.editorRevision = input, revision
}

func (s *State) consumeHTTPSessionDestroyPreview(d *Document, job *operation, r outcome) {
	plan := r.destroyPlan
	defer func() {
		if plan != nil {
			_ = plan.Close()
		}
	}()
	e := r.result.HTTPSessionDestroy
	if e == nil || e.LocalRecordID == "" || len(e.LocalRecordID) > 128 || e.Preview.Request != job.destroyRequest || !reflect.DeepEqual(e.Preview.Scope, job.scope) || !validDestroyRequest(r.result.Request, job.destroyRequest.ID) {
		d.error = locales.Text("KV 请求已更改，请重新预览")
		return
	}
	receipt := &Receipt{Scope: job.scope.Clone(), Authored: r.result.Request, Result: r.result.Clone()}
	if r.err != nil {
		receipt.Error = r.err.Error()
	}
	d.last, d.selectedUnknown = receipt, false
	d.httpSessions, d.httpSessionKeys = false, false
	d.httpSessionBrowser, d.httpSessionKeysBrowser = nil, nil
	behavior, valid := destroyPreviewBehavior(e.Preview)
	if r.err != nil || job.canceled || plan == nil || !valid || plan.LocalRecordID() != e.LocalRecordID || plan.Snapshot() != r.result.Request || !plan.Writes() || !reflect.DeepEqual(plan.Impact(), e.Preview) || job.lease != d.lease || !reflect.DeepEqual(job.scope, d.scope) || d.Request() != job.authored || d.Editor.Buffer.Version() != job.editorRevision || d.needsRecovery || d.hasUnknownWrites() {
		if r.err == nil {
			d.error = locales.Text("KV 请求已更改，请重新预览")
		}
		return
	}
	d.clearConfirmation()
	revision := job.editorRevision
	d.confirmation = &confirmation{lease: job.lease, plan: plan, authored: r.result.Request, editor: job.authored, editorRevision: &revision, open: true, destroy: &httpSessionDestroyConfirmation{plan: plan, preview: e.Preview.Clone(), recordID: e.LocalRecordID, behavior: behavior}}
	plan = nil
}

func validHTTPSessionDestroyConfirmation(d *Document, p *confirmation) bool {
	x := p.destroy
	return x != nil && x.plan != nil && x.plan == p.plan && x.plan.LocalRecordID() == x.recordID && validDestroyRequest(p.authored, x.preview.Request.ID) && x.plan.Snapshot() == p.authored && reflect.DeepEqual(x.preview.Scope, d.scope) && reflect.DeepEqual(x.plan.Impact(), x.preview)
}

func (s *State) executeHTTPSessionDestroy(d *Document, p *confirmation) {
	plan, lease, authored := p.destroy.plan, p.lease, p.authored
	p.destroy.plan = nil
	d.clearConfirmation()
	s.start(d, "execute", authored, func(ctx context.Context) outcome {
		defer plan.Close()
		r, err := lease.Execute(ctx, plan)
		return outcome{result: r, err: err}
	})
}
