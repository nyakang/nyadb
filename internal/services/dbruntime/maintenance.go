package dbruntime

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *session) Maintenance() contract.MaintenanceOperations {
	if !s.alive() {
		return contract.MaintenanceOperations{}
	}
	p, err := contract.Require[contract.MaintenanceProvider](s.raw)
	if err != nil {
		return contract.MaintenanceOperations{}
	}
	raw := p.Maintenance()
	if raw.Plan == nil || raw.Execute == nil || raw.Actions == nil {
		return contract.MaintenanceOperations{}
	}
	check := func(t contract.SchemaDesignTarget) error {
		if err := s.schemaDesignTarget(t); err != nil {
			return err
		}
		return s.require(s.Capabilities().Maintenance, t.Object.ProfileID)
	}
	return contract.MaintenanceOperations{
		Actions: func(t contract.SchemaDesignTarget) []contract.MaintenanceAction {
			if check(t) != nil {
				return nil
			}
			t.Generation = s.raw.Generation()
			return append([]contract.MaintenanceAction(nil), raw.Actions(t)...)
		},
		Plan: func(ctx context.Context, r contract.MaintenanceRequest) (contract.MaintenancePlan, error) {
			if err := check(r.Target); err != nil {
				return contract.MaintenancePlan{}, err
			}
			r.Target.Generation = s.raw.Generation()
			plan, err := raw.Plan(ctx, r)
			if err == nil && plan.Request != r {
				return contract.MaintenancePlan{}, contract.ErrRevoked
			}
			plan.Request.Target.Generation = s.generation
			return plan, err
		},
		Execute: func(ctx context.Context, plan contract.MaintenancePlan) contract.MaintenanceOutcome {
			if err := check(plan.Request.Target); err != nil {
				return contract.MaintenanceOutcome{State: "not_executed", Error: err}
			}
			plan.Request.Target.Generation = s.raw.Generation()
			return raw.Execute(ctx, plan)
		},
	}
}
func (m *Manager) Maintenance(id int64) contract.MaintenanceOperations {
	s, err := m.Session(id)
	if err != nil {
		return contract.MaintenanceOperations{}
	}
	return s.(*session).Maintenance()
}
