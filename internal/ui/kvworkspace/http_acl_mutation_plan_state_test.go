package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type aclWorkspacePlanningLease struct {
	*aclMutationStateLease
	prepare func(context.Context, contract.KVHTTPACLMutationRequest, contract.KVHTTPACLImpactLimits) (contract.KVHTTPACLMutationPlan, contract.KVHTTPACLImpact, error)
	execute func(context.Context, contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error)
}

func (l *aclWorkspacePlanningLease) PrepareHTTPACLMutationPlan(ctx context.Context, r contract.KVHTTPACLMutationRequest, limits contract.KVHTTPACLImpactLimits) (contract.KVHTTPACLMutationPlan, contract.KVHTTPACLImpact, error) {
	return l.prepare(ctx, r, limits)
}
func (l *aclWorkspacePlanningLease) ExecuteHTTPACLMutation(ctx context.Context, p contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error) {
	if l.execute != nil {
		return l.execute(ctx, p)
	}
	return l.aclMutationStateLease.ExecuteHTTPACLMutation(ctx, p)
}

func TestHTTPACLMutationPreviewParksAndRevokesOriginalAuthority(t *testing.T) {
	for _, change := range []string{"park", "cancel", "editor", "revision", "scope", "disconnect", "close"} {
		t.Run(change, func(t *testing.T) {
			s, d, base, _ := aclMutationWorkspace(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var plan *aclMutationStatePlan
			lease := &aclWorkspacePlanningLease{aclMutationStateLease: base}
			lease.prepare = func(ctx context.Context, r contract.KVHTTPACLMutationRequest, limits contract.KVHTTPACLImpactLimits) (contract.KVHTTPACLMutationPlan, contract.KVHTTPACLImpact, error) {
				p, o, e := base.PrepareHTTPACLMutationPlan(ctx, r, limits)
				plan = p.(*aclMutationStatePlan)
				close(entered)
				<-release
				return p, o, e // deliberately return success even if cancelled
			}
			d.lease = lease
			s.SetHTTPACLMutationInput(d, mutationStateInput())
			s.PrepareHTTPACLMutation(d)
			job := d.job
			await(t, entered)
			if !d.Busy() || d.httpACLMutation.confirmation != nil || base.mutations.Load() != 0 {
				t.Fatal("preview dispatched a write or invented completion")
			}
			s.ConfirmHTTPACLMutation(d, true)
			switch change {
			case "park":
				s.deps.Active = func(*Document) bool { return false }
			case "cancel":
				s.RequestCancel(d)
			case "editor":
				d.Editor.Buffer.Set("changed")
			case "revision":
				d.Editor.Buffer.Set("changed")
				d.Editor.Buffer.Set(job.authored.JSON)
			case "scope":
				d.scope.Generation++
			case "disconnect":
				s.DisconnectProfile(d.profileID)
			}
			if change == "cancel" || change == "disconnect" {
				s.Consume()
				if !d.Busy() {
					t.Fatal("cancel did not wait for original preview executor")
				}
			}
			once.Do(func() { close(release) })
			await(t, job.finished)
			if change == "close" {
				s.Close(d)
				if !plan.closed.Load() || d.httpACLMutation != nil {
					t.Fatal("closing parked preview retained authority or evidence")
				}
				return
			}
			s.Consume()
			if change == "park" {
				if !d.Busy() || d.httpACLMutation.observation != nil {
					t.Fatal("parked preview entered inactive document")
				}
				s.deps.Active = func(*Document) bool { return true }
				s.Consume()
				if d.httpACLMutation.confirmation == nil || plan.closed.Load() {
					t.Fatal("original parked preview was not admitted on return")
				}
				s.CancelConfirmation(d)
			} else if d.httpACLMutation.confirmation != nil {
				t.Fatal("stale preview gained authority")
			}
			if !plan.closed.Load() || d.httpACLMutation.observation == nil || base.mutations.Load() != 0 {
				t.Fatal("preview cleanup lost original evidence or executed")
			}
		})
	}
}

func TestHTTPACLMutationFailedPreviewAndRecheckKeepSeparateCompleteEvidence(t *testing.T) {
	for _, stage := range []string{"preview", "recheck"} {
		t.Run(stage, func(t *testing.T) {
			s, d, base, _ := aclMutationWorkspace(t)
			s.OpenHTTPACLMutationForm(d)
			runACLMutation(t, s, d, mutationStateInput())
			consume(t, s, d)
			original := d.httpACLMutation.last.Clone()
			before, _ := s.OperationRecord(d)
			lease := &aclWorkspacePlanningLease{aclMutationStateLease: base}
			lease.prepare = func(ctx context.Context, r contract.KVHTTPACLMutationRequest, limits contract.KVHTTPACLImpactLimits) (contract.KVHTTPACLMutationPlan, contract.KVHTTPACLImpact, error) {
				p, o, e := base.PrepareHTTPACLMutationPlan(ctx, r, limits)
				if stage == "preview" {
					p.Close()
					o.Complete = false
					return nil, o, errors.New("synthetic-private-provider-error")
				}
				return p, o, e
			}
			lease.execute = func(ctx context.Context, p contract.KVHTTPACLMutationPreparation) (contract.KVHTTPACLMutationResult, error) {
				o := p.(contract.KVHTTPACLMutationPlan).Impact()
				recheck := o.Clone()
				recheck.Complete = false
				return contract.KVHTTPACLMutationResult{LocalRecordID: p.LocalRecordID(), Scope: lease.Scope(), Request: p.Request(), Impact: &contract.KVHTTPACLMutationImpactEvidence{Preview: o, Recheck: recheck}}, errors.New("synthetic-private-provider-error")
			}
			d.lease = lease
			s.PrepareHTTPACLMutation(d)
			consume(t, s, d)
			if stage == "recheck" {
				if d.httpACLMutation.confirmation == nil {
					t.Fatal("recheck plan unavailable")
				}
				s.ConfirmHTTPACLMutation(d, true)
				consume(t, s, d)
			}
			x := d.httpACLMutation
			if x.observation == nil || x.confirmation != nil || !reflect.DeepEqual(*x.last, original) || base.mutations.Load() != 1 || x.unknown || strings.Contains(d.Error(), "synthetic-private") {
				t.Fatal("failed observation replaced last attempted write, leaked secrets or authorized write")
			}
			after, _ := s.OperationRecord(d)
			if !bytes.Equal(before, after) {
				t.Fatal("private impact entered generic export")
			}
			var exported bytes.Buffer
			s.SetFiles(Files{SavePath: func(string) (string, error) { return "owned-memory.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error { return write(&exported) }})
			if !s.prepareHTTPACLMutationTransfer(d, "export-acl-write-impact") || s.confirmHTTPACLMutationTransfer(ui.Services{}, d, false) || exported.Len() != 0 {
				t.Fatal("private impact export bypassed consent")
			}
			if !s.confirmHTTPACLMutationTransfer(ui.Services{}, d, true) {
				t.Fatal("impact export unavailable")
			}
			consume(t, s, d)
			var decoded struct {
				Kind        string
				Observation httpACLMutationObservation
			}
			if json.Unmarshal(exported.Bytes(), &decoded) != nil || decoded.Kind != "consul-original-acl-impact" || !reflect.DeepEqual(decoded.Observation, *x.observation) {
				t.Fatal("export changed complete original observations")
			}
			if _, err := decodeHTTPACLMutationInput(exported.Bytes()); err == nil {
				t.Fatal("evidence became authored write input")
			}
			if !s.GuardLeave(d, nil) {
				t.Fatal("private evidence lost leave guard")
			}
		})
	}
}

func TestHTTPACLMutationPreviewRejectsAlteredEvidenceAndReleasesPlans(t *testing.T) {
	for _, change := range []string{"body", "scope", "partial", "filtered", "rows", "references", "creation", "generic-kind"} {
		t.Run(change, func(t *testing.T) {
			s, d, base, _ := aclMutationWorkspace(t)
			lease := &aclWorkspacePlanningLease{aclMutationStateLease: base}
			var plan *aclMutationStatePlan
			lease.prepare = func(ctx context.Context, r contract.KVHTTPACLMutationRequest, limits contract.KVHTTPACLImpactLimits) (contract.KVHTTPACLMutationPlan, contract.KVHTTPACLImpact, error) {
				p, o, _ := base.PrepareHTTPACLMutationPlan(ctx, r, limits)
				plan = p.(*aclMutationStatePlan)
				switch change {
				case "generic-kind":
					o.Reads[0].Result.Receipt.HTTP.Kind = "read"
				case "body":
					o.Reads[0].Result.Receipt.HTTP.Body[0] = 'x'
				case "scope":
					o.Scope.Generation++
				case "partial":
					o.Reads[0].Result.Receipt.HTTP.EOF = false
				case "filtered":
					o.Reads[0].Result.Receipt.HTTP.Metadata.ACLFiltered = new(true)
				case "rows":
					o.Rows++
				case "references":
					o.References = []contract.KVHTTPACLImpactReference{{Resource: "token", Identity: "foreign", Locations: []contract.KVHTTPACLImpactLocation{{Read: 0, Row: 0}}}}
				case "creation":
					o.Creation.Definition = []byte(`{}`)
				}
				plan.impact = o.Clone()
				return p, o, nil
			}
			d.lease = lease
			s.SetHTTPACLMutationInput(d, mutationStateInput())
			s.PrepareHTTPACLMutation(d)
			consume(t, s, d)
			if d.httpACLMutation.confirmation != nil || !plan.closed.Load() || base.mutations.Load() != 0 {
				t.Fatal("invalid original observation gained authority")
			}
		})
	}
}
