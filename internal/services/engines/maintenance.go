package engines

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) Maintenance() contract.MaintenanceOperations {
	return contract.MaintenanceOperations{Actions: s.maintenanceActions, Plan: s.maintenancePlan, Execute: s.maintain}
}
func (s *Session) maintenanceTarget(t contract.SchemaDesignTarget) error {
	if s.isClosed() || t.Generation != s.generation {
		return contract.ErrRevoked
	}
	o := t.Object
	if o.Kind != "table" && o.Kind != "namespace" {
		return contract.ErrUnsupported
	}
	if o.Name == "" {
		o.Name = "_"
	}
	return s.check(o)
}
func (s *Session) namespaceReadOnly(schema string) bool {
	if s.readOnly {
		return true
	}
	if d, ok := s.strategy.(sqliteDialect); ok {
		if d.options.ReadOnly {
			return true
		}
		for _, a := range d.options.Attachments {
			if a.Name == schema {
				return a.ReadOnly
			}
		}
	}
	return false
}
func (s *Session) maintenanceActions(t contract.SchemaDesignTarget) []contract.MaintenanceAction {
	if s.kind == connections.CockroachDB || s.kind.IsGaussFamily() || s.maintenanceTarget(t) != nil {
		return nil
	}
	var actions []contract.MaintenanceAction
	if s.kind == connections.PostgreSQL {
		if t.Object.Kind != "table" || t.Object.Name == "" {
			return nil
		}
		actions = []contract.MaintenanceAction{{ID: "VACUUM", Label: "VACUUM", Mutates: true}, {ID: "ANALYZE", Label: "分析表", Mutates: true, Transactional: true}, {ID: "VACUUM ANALYZE", Label: "VACUUM ANALYZE", Mutates: true}}
		if t.Object.Name != "" {
			actions = append(actions, contract.MaintenanceAction{ID: "REINDEX", Label: "重建索引", Mutates: true, Transactional: true})
		}
	} else {
		actions = []contract.MaintenanceAction{{ID: "VACUUM", Label: "VACUUM", Mutates: true}, {ID: "ANALYZE", Label: "分析表", Mutates: true, Transactional: true}, {ID: "integrity_check", Label: "完整性检查"}, {ID: "foreign_key_check", Label: "外键检查"}}
	}
	if s.namespaceReadOnly(t.Object.Schema) {
		out := actions[:0]
		for _, a := range actions {
			if !a.Mutates {
				out = append(out, a)
			}
		}
		actions = out
	}
	return actions
}
func (s *Session) maintenancePlan(ctx context.Context, r contract.MaintenanceRequest) (contract.MaintenancePlan, error) {
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	if err := s.maintenanceTarget(r.Target); err != nil {
		return contract.MaintenancePlan{}, err
	}
	var action *contract.MaintenanceAction
	for _, a := range s.maintenanceActions(r.Target) {
		if a.ID == r.Action {
			copy := a
			action = &copy
			break
		}
	}
	if action == nil {
		return contract.MaintenancePlan{}, contract.ErrUnsupported
	}
	o := r.Target.Object
	if o.Name != "" {
		t, err := s.Structure(ctx, o)
		if err != nil {
			return contract.MaintenancePlan{}, err
		}
		if t.Object.Kind != "table" {
			return contract.MaintenancePlan{}, contract.ErrUnsupported
		}
	}
	q := ""
	if s.kind == connections.PostgreSQL {
		if o.Name == "" {
			return contract.MaintenancePlan{}, contract.ErrUnsupported
		} // PG schema has no native VACUUM target.
		var version int
		var ordinary, allowed bool
		if err := s.db.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::integer,
 c.relkind='r' AND NOT c.relispartition AND NOT EXISTS(SELECT 1 FROM pg_inherits WHERE inhrelid=c.oid),
 r.rolsuper OR pg_has_role(current_user,c.relowner,'USAGE')
 OR COALESCE(pg_has_role(current_user,to_regrole('pg_maintain'),'USAGE'),false)
 OR ($2<>'REINDEX' AND pg_has_role(current_user,d.datdba,'USAGE'))
 FROM pg_class c CROSS JOIN pg_roles r CROSS JOIN pg_database d
 WHERE c.oid=to_regclass($1) AND r.rolname=current_user AND d.datname=current_database()`, qualified(o), r.Action).Scan(&version, &ordinary, &allowed); err != nil {
			return contract.MaintenancePlan{}, err
		}
		if version < 140000 || version >= 190000 || !ordinary {
			return contract.MaintenancePlan{}, contract.ErrUnsupported
		}
		if !allowed {
			return contract.MaintenancePlan{}, errors.New("insufficient privilege to maintain the target table")
		}
		q = r.Action + " " + qualified(o)
		if r.Action == "REINDEX" {
			q = "REINDEX TABLE " + qualified(o)
		}
	} else {
		switch r.Action {
		case "VACUUM":
			q = "VACUUM " + quote(o.Schema)
		case "ANALYZE":
			q = "ANALYZE " + quote(o.Schema)
			if o.Name != "" {
				q = "ANALYZE " + qualified(o)
			}
		case "integrity_check", "foreign_key_check":
			q = "PRAGMA " + quote(o.Schema) + "." + r.Action
			if o.Name != "" {
				q += "(" + quote(o.Name) + ")"
			}
		}
	}
	return contract.MaintenancePlan{Request: r, SQL: q, Transactional: action.Transactional}, nil
}
func (s *Session) maintain(ctx context.Context, plan contract.MaintenancePlan) (out contract.MaintenanceOutcome) {
	out.State = "not_executed"
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	checked, err := s.maintenancePlan(ctx, plan.Request)
	if err != nil || checked != plan {
		out.Error = errors.Join(err, func() error {
			if checked != plan {
				return errors.New("maintenance plan changed")
			}
			return nil
		}())
		return
	}
	if s.kind == connections.SQLite {
		release, err := s.schema.acquire(ctx, true)
		if err != nil {
			out.Error = err
			return
		}
		defer release()
	}
	c, err := s.db.Conn(ctx)
	if err != nil {
		out.Error = err
		return
	}
	defer c.Close()
	if strings.HasPrefix(plan.SQL, "PRAGMA ") {
		rows, err := c.QueryContext(ctx, plan.SQL)
		if err != nil {
			out.Error = err
			return
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			out.Error = err
			return
		}
		for rows.Next() {
			values := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range values {
				ptr[i] = &values[i]
			}
			if err = rows.Scan(ptr...); err != nil {
				out.Error = err
				return
			}
			if len(out.Messages) >= 1000 {
				out.Error = errors.New("maintenance diagnostic limit exceeded")
				return
			}
			out.Messages = append(out.Messages, contract.MaintenanceMessage{Table: plan.Request.Target.Object.Name, Operation: plan.Request.Action, Type: "result", Message: fmt.Sprint(values...)})
		}
		out.Error = rows.Err()
	} else if plan.Transactional {
		tx, err := c.BeginTx(ctx, nil)
		if err != nil {
			out.Error = err
			return
		}
		if _, err = tx.ExecContext(ctx, plan.SQL); err != nil {
			out.Error = err
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				out.State = "uncertain"
				out.Error = errors.Join(err, rollbackErr)
			} else {
				out.State = "rolled_back"
			}
			return
		}
		if err = tx.Commit(); err != nil {
			out.State, out.Error = "uncertain", err
			return
		}
	} else {
		_, out.Error = c.ExecContext(ctx, plan.SQL)
	}
	if out.Error != nil {
		out.State = "uncertain"
		return
	}
	out.State = "completed"
	if len(out.Messages) == 0 {
		out.Messages = []contract.MaintenanceMessage{{Table: plan.Request.Target.Object.Name, Operation: plan.Request.Action, Type: "status", Message: "completed"}}
	}
	return
}
