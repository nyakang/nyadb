package mysql

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *SessionAdapter) Maintenance() contract.MaintenanceOperations {
	return contract.MaintenanceOperations{
		Actions: func(t contract.SchemaDesignTarget) []contract.MaintenanceAction {
			if s.check(t.Object) != nil || t.Generation != s.Generation() || t.Object.Kind != "table" {
				return nil
			}
			actions := []contract.MaintenanceAction{{ID: "ANALYZE", Label: "分析表", Mutates: true}, {ID: "CHECK", Label: "检查表"}}
			if s.kind != connections.TiDB && s.kind != connections.OceanBase {
				actions = append(actions, contract.MaintenanceAction{ID: "OPTIMIZE", Label: "优化表", Mutates: true}, contract.MaintenanceAction{ID: "REPAIR", Label: "修复表", Mutates: true})
			}
			return actions
		},
		Plan: func(_ context.Context, r contract.MaintenanceRequest) (contract.MaintenancePlan, error) {
			for _, a := range s.Maintenance().Actions(r.Target) {
				if a.ID == r.Action {
					db, e := quotedMySQLIdentifier(r.Target.Object.Catalog)
					if e != nil {
						return contract.MaintenancePlan{}, e
					}
					table, e := quotedMySQLIdentifier(r.Target.Object.Name)
					if e != nil {
						return contract.MaintenancePlan{}, e
					}
					op := r.Action
					if s.kind == connections.TiDB && op == "CHECK" {
						op = "ADMIN CHECK"
					}
					return contract.MaintenancePlan{Request: r, SQL: op + " TABLE " + db + "." + table}, nil
				}
			}
			return contract.MaintenancePlan{}, contract.ErrUnsupported
		},
		Execute: func(ctx context.Context, p contract.MaintenancePlan) contract.MaintenanceOutcome {
			actual, e := s.Maintenance().Plan(ctx, p.Request)
			if e != nil || actual != p {
				return contract.MaintenanceOutcome{State: "not_executed", Error: contract.ErrRevoked}
			}
			v, e := s.manager.MaintainTable(WithMySQLSessionGeneration(ctx, p.Request.Target.Generation), s.id, p.Request.Target.Object.Catalog, p.Request.Target.Object.Name, p.Request.Action)
			state := "completed"
			if e != nil {
				state = "uncertain"
			}
			return contract.MaintenanceOutcome{State: state, Messages: v, Error: e}
		},
	}
}
