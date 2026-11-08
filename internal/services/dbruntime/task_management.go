package dbruntime

import (
	"context"
	"slices"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (d *taskDocument) management() (contract.TaskManagementLease, error) {
	p, ok := d.raw.(contract.TaskManagementLease)
	if !ok {
		return nil, contract.ErrUnsupported
	}
	return p, nil
}
func (d *taskDocument) SupportsTaskManagement() bool {
	if d.check() != nil {
		return false
	}
	p, err := d.management()
	return err == nil && p.SupportsTaskManagement()
}
func (d *taskDocument) PrepareTaskMutation(action string, records []contract.TaskRecord) (contract.TaskMutationPlan, error) {
	if err := d.check(); err != nil {
		return contract.TaskMutationPlan{}, err
	}
	p, err := d.management()
	if err != nil {
		return contract.TaskMutationPlan{}, err
	}
	return p.PrepareTaskMutation(action, slices.Clone(records))
}
func (d *taskDocument) MutateTasks(ctx context.Context, plan contract.TaskMutationPlan) (contract.TaskMutationResult, error) {
	if err := d.check(); err != nil {
		return contract.TaskMutationResult{State: "not_submitted"}, err
	}
	p, err := d.management()
	if err != nil {
		return contract.TaskMutationResult{State: "not_submitted"}, err
	}
	plan.Records = slices.Clone(plan.Records)
	return p.MutateTasks(ctx, plan)
}
func (d *taskDocument) ObserveTaskMutation(ctx context.Context, receipt contract.TaskMutationReceipt, bound time.Duration) (contract.TaskMutationResult, error) {
	if err := d.check(); err != nil {
		return contract.TaskMutationResult{Receipt: receipt}, err
	}
	p, err := d.management()
	if err != nil {
		return contract.TaskMutationResult{Receipt: receipt}, err
	}
	receipt.Targets = slices.Clone(receipt.Targets)
	return p.ObserveTaskMutation(ctx, receipt, bound)
}
func (s *session) RecoverTaskDocument(o contract.ObjectRef, previous contract.TaskDocumentLease) (contract.TaskRecovery, error) {
	if err := s.require(s.Capabilities().Metadata, o.ProfileID); err != nil {
		return contract.TaskRecovery{}, err
	}
	old, ok := previous.(*taskDocument)
	if !ok || old.object != o || old.session.id != s.id {
		return contract.TaskRecovery{}, contract.ErrTaskMutationUsed
	}
	p, err := contract.Require[contract.TaskRecoveryProvider](s.raw)
	if err != nil {
		return contract.TaskRecovery{}, err
	}
	r, err := p.RecoverTaskDocument(o, old.raw)
	if err != nil {
		return r, err
	}
	r.Lease = &taskDocument{session: s, object: o, raw: r.Lease}
	r.Receipt.Targets = slices.Clone(r.Receipt.Targets)
	return r, nil
}
