package dbruntime

import (
	"context"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *session) SupportsNamespaceManagement(o contract.ObjectRef, action string) bool {
	if !s.SupportsTaskDocument(o) {
		return false
	}
	p, ok := s.raw.(contract.NamespaceManagementProvider)
	return ok && p.SupportsNamespaceManagement(o, action)
}
func (d *taskDocument) SupportsNamespaceManagement(action string) bool {
	if d.check() != nil {
		return false
	}
	p, ok := d.raw.(contract.NamespaceManagementLease)
	return ok && p.SupportsNamespaceManagement(action)
}
func (d *taskDocument) ReadNamespace(ctx context.Context, ref contract.NamespaceRef) (contract.NamespaceRecord, error) {
	if err := d.check(); err != nil {
		return contract.NamespaceRecord{}, err
	}
	p, ok := d.raw.(contract.NamespaceManagementLease)
	if !ok {
		return contract.NamespaceRecord{}, contract.ErrUnsupported
	}
	return p.ReadNamespace(ctx, ref)
}
func (d *taskDocument) PrepareNamespaceMutation(action string, draft contract.NamespaceDraft, before []contract.NamespaceRecord) (contract.TaskMutationPlan, error) {
	if err := d.check(); err != nil {
		return contract.TaskMutationPlan{}, err
	}
	p, ok := d.raw.(contract.NamespaceManagementLease)
	if !ok {
		return contract.TaskMutationPlan{}, contract.ErrUnsupported
	}
	return p.PrepareNamespaceMutation(action, draft, slices.Clone(before))
}
