package kvworkspace

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
	"reflect"
)

func (d *Document) clearHTTPManagementPlan() {
	if d.httpManagement == nil {
		return
	}
	p := d.httpManagement.plan
	d.httpManagement.plan = nil
	d.httpManagement.checked = false
	if closable, ok := p.(interface{ Close() error }); ok {
		_ = closable.Close()
	}
}
func (s *State) prepareHTTPEnterpriseDeletion(d *Document, in contract.KVHTTPManagementRequest) {
	f, ok := d.lease.(contract.KVHTTPEnterpriseDeletionPlanning)
	if !ok {
		d.error = locales.Text("此连接不支持 Enterprise 删除影响预览")
		return
	}
	x := d.httpManagement
	revision := x.revision
	editor := d.Editor.Buffer.Version()
	s.start(d, "http-management-impact", contract.KVRequest{}, func(ctx context.Context) outcome {
		p, i, err := f.PrepareHTTPEnterpriseDeletion(ctx, in.Clone())
		v := i.Clone()
		return outcome{managementPlan: p, managementImpact: &v, err: err}
	})
	d.job.httpManagementRequest = in.Clone()
	d.job.managementRevision = revision
	d.job.editorRevision = editor
}
func (s *State) consumeHTTPEnterpriseDeletion(d *Document, job *operation, out outcome) {
	if d.httpManagement == nil {
		d.httpManagement = &httpManagementState{}
	}
	x := d.httpManagement
	p := out.managementPlan
	if out.managementImpact != nil {
		v := out.managementImpact.Clone()
		x.deletionImpact = &v
	}
	input, err := s.managementInput(d)
	if out.err != nil || err != nil || p == nil || job.managementRevision != x.revision || job.editorRevision != d.Editor.Buffer.Version() || !reflect.DeepEqual(input, job.httpManagementRequest) || d.lease != job.lease || !sameEventScope(job.scope, d.scope) || d.lease.CheckKVLease() != nil {
		if p != nil {
			_ = p.Close()
		}
		if out.err != nil {
			d.error = locales.Text("Enterprise 删除影响不完整或目标已变化；写入未派发")
		}
		return
	}
	if !sameEventScope(p.Scope(), d.scope) || !reflect.DeepEqual(p.Request(), input) {
		_ = p.Close()
		d.error = contract.ErrRevoked.Error()
		return
	}
	x.plan = p
	x.lease = d.lease
	x.editorRevision = editorVersion(d)
	x.checked = false
}
func editorVersion(d *Document) uint64 { return d.Editor.Buffer.Version() }

func (d *Document) revokePrivateToolPreparations() {
	d.clearHTTPManagementPlan()
	if x := d.httpManagement; x != nil {
		x.revealed = false
		x.exportConsent = false
	}
	if x := d.httpKVTools; x != nil {
		x.plan = nil
		x.checked = false
		x.consent = false
	}
	if x := d.httpArchive; x != nil {
		if x.restorePlan != nil {
			_ = x.restorePlan.Close()
			x.restorePlan = nil
		}
		x.capturePlan = nil
		x.checked = false
		x.revealed = false
		x.exportConsent = false
	}
}
